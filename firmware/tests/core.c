/* Native tests of the shared core (protocol.c, bridge.c) and the descriptor
   table, with HAL stubs. */
#include "bridge.h"
#include "hal.h"
#include "descriptors.h"
#include "identity.h"
#include <assert.h>
#include <stdio.h>
#include <string.h>

static hb_bridge b;
static hb_frame frames[512];
static unsigned nframes, nreports, releases, wakes, boots;
static uint8_t report[8], report_kind, report_len;
static bool ready, accepts;

void hal_write(const uint8_t *wire, uint8_t len)
{
    hb_decoder d = {0};
    assert(nframes < 512);
    for (uint8_t i = 0; i < len; ++i) {
        int8_t r = hb_decode_byte(&d, wire[i], &frames[nframes]);
        assert(r >= 0);
        if (r) ++nframes;
    }
}
uint8_t hal_identity(uint8_t body[HB_BODY])
{
    memcpy(body, HB_IDENTITY_TEXT, sizeof(HB_IDENTITY_TEXT) - 1);
    return sizeof(HB_IDENTITY_TEXT) - 1;
}
bool hal_report(uint8_t kind, const uint8_t *data, uint8_t len)
{
    if (!accepts) return false;
    ++nreports; report_kind = kind; report_len = len;
    memcpy(report, data, len);
    return true;
}
bool hal_reports_idle(void) { return ready; }
void hal_release(uint8_t mask) { releases |= mask; }
void hal_attach(bool attach) { (void)attach; }
bool hal_wakeup(void) { ++wakes; return true; }
void hal_bootloader(void) { ++boots; }

static void reset(void)
{
    hb_init(&b); b.usb = HB_CONFIGURED;
    nframes = nreports = releases = wakes = boots = 0;
    ready = accepts = true;
}
static void command(uint8_t seq, uint8_t type, const uint8_t *body, uint8_t len, uint32_t now)
{
    hb_frame f = {.seq = seq, .type = type, .len = len};
    if (len) memcpy(f.body, body, len);
    uint8_t wire[HB_WIRE], size = hb_encode(&f, wire);
    assert(size);
    for (uint8_t i = 0; i < size; ++i) hb_receive(&b, wire[i], now);
}
static void hello(uint8_t seq, uint32_t now)
{
    uint8_t version = HB_VERSION;
    command(seq, HB_HELLO, &version, 1, now);
}
static hb_frame *last(void) { assert(nframes); return &frames[nframes - 1]; }
static void expect_nak(uint8_t code, uint8_t expected)
{
    assert(last()->type == HB_NAK && last()->body[0] == code && last()->body[1] == expected);
}

static void test_framing(void)
{
    assert(hb_crc16((const uint8_t *)"123456789", 9) == 0x29b1);
    for (unsigned size = 0; size <= 64; ++size) {
        for (unsigned pattern = 0; pattern < 256; ++pattern) {
            hb_frame f = {.seq = pattern, .type = HB_KEY_REPORT, .len = size}, out;
            for (unsigned i = 0; i < size; ++i) f.body[i] = i * pattern;
            uint8_t wire[HB_WIRE], n = hb_encode(&f, wire);
            assert(n <= 70 && wire[n - 1] == 0);
            hb_decoder d = {0};
            for (uint8_t i = 0; i < n; ++i)
                assert(hb_decode_byte(&d, wire[i], &out) == (i == n - 1));
            assert(out.seq == f.seq && out.type == f.type && out.len == f.len);
            assert(!memcmp(out.body, f.body, size));
        }
    }
    hb_decoder d = {0}; hb_frame out;
    for (unsigned i = 0; i < 300; ++i) hb_decode_byte(&d, 255, &out);
    assert(hb_decode_byte(&d, 0, &out) == -HB_FRAMING);
    assert(hb_decode_byte(&d, 0, &out) == 0);
    hb_decode_byte(&d, 5, &out);
    assert(hb_decode_byte(&d, 0, &out) == -HB_FRAMING);
    /* Noise must remain bounded under ASan/UBSan and recover on a delimiter. */
    uint32_t rng = 17;
    for (unsigned i = 0; i < 200000; ++i) {
        rng = rng * 1664525u + 1013904223u;
        hb_decode_byte(&d, rng >> 24, &out);
    }
    (void)hb_decode_byte(&d, 0, &out);
    assert(!d.len && !d.overflow);
}

static void test_sequences(void)
{
    reset();
    command(10, HB_STATUS, NULL, 0, 0); expect_nak(HB_NO_SESSION, 0);
    hello(253, 0); assert(b.expected == 254 && last()->body[3] == 16);
    command(254, 0x77, NULL, 0, 1); expect_nak(HB_TYPE, 255);
    command(254, HB_KEY_DOWN, NULL, 0, 2); expect_nak(HB_TYPE, 255);
    assert(b.rx_errors == 1); /* Replayed refusal must not increment again. */
    command(255, HB_MOUSE, NULL, 0, 3); expect_nak(HB_LENGTH, 0);
    uint8_t wait[] = {0,0};
    command(0, HB_WAIT, wait, 2, 4); expect_nak(HB_STATE, 1);
    command(0, HB_HEARTBEAT, NULL, 0, 5); expect_nak(HB_STATE, 1);
    command(33, HB_MOUSE, NULL, 0, 6); expect_nak(HB_SEQUENCE, 1);
    for (uint8_t i = 1; i <= 9; ++i) command(i, HB_HEARTBEAT, NULL, 0, i + 6);
    command(1, HB_HEARTBEAT, NULL, 0, 16); expect_nak(HB_SEQUENCE, 10);
    command(2, HB_HEARTBEAT, NULL, 0, 17); assert(last()->type == HB_ACK);
    /* A HELLO after another HELLO cannot replay responses from an older session. */
    hello(5, 20);
    command(4, HB_HEARTBEAT, NULL, 0, 21); expect_nak(HB_SEQUENCE, 6);
    uint8_t key = 4;
    command(6, HB_KEY_DOWN, &key, 1, 22);
    hb_tick(&b, 22); assert(nreports == 1 && hb_any_held(&b));
    command(6, HB_KEY_DOWN, &key, 1, 23);
    hb_tick(&b, 23); assert(nreports == 1);
    hello(5, 24); assert(!hb_any_held(&b) && releases == 1);
}

static void test_queue_and_timing(void)
{
    reset(); hello(0, 0);
    uint8_t wait[] = {0x10,0x27};
    command(1, HB_WAIT, wait, 2, 0); hb_tick(&b, 0);
    assert(b.waiting && b.count == 0);
    for (uint8_t i = 2; i < 18; ++i) command(i, HB_SYNC, NULL, 0, 0);
    assert(b.count == 16);
    for (uint32_t t = 100; t <= 10000; t += 100) {
        command(18, HB_SYNC, NULL, 0, t); expect_nak(HB_QUEUE_FULL, 18);
        hb_tick(&b, t);
    }
    assert(!b.waiting && !b.watchdogs && b.count == 15);
    command(18, HB_SYNC, NULL, 0, 10001); assert(last()->type == HB_ACK);
    command(19, HB_RELEASE_ALL, NULL, 0, 10002); assert(b.count == 0 && !b.waiting);
    reset(); hello(0, 0);
    uint8_t key = 4;
    command(1, HB_KEY_DOWN, &key, 1, 0);
    command(2, HB_KEY_UP, &key, 1, 0);
    command(3, HB_SYNC, NULL, 0, 0);
    accepts = false; hb_tick(&b, 0); assert(!hb_any_held(&b) && b.count == 3);
    accepts = true; hb_tick(&b, 0); assert(nreports == 1 && report[2] == 4);
    ready = false; hb_tick(&b, 10); assert(nreports == 1 && b.count == 2);
    ready = true; hb_tick(&b, 11); assert(nreports == 2 && report[2] == 0);
    ready = false; hb_tick(&b, 12); assert(last()->type != HB_EVENT_DONE);
    ready = true; hb_tick(&b, 13); assert(last()->type == HB_EVENT_DONE && last()->body[0] == 3);
}

static void test_watchdog(void)
{
    reset(); hello(0, 0);
    uint8_t key = 4;
    command(1, HB_KEY_DOWN, &key, 1, 10); hb_tick(&b, 10);
    hb_tick(&b, 509); assert(hb_any_held(&b));
    command(1, HB_KEY_DOWN, &key, 1, 509); /* Duplicate feeds it. */
    hb_tick(&b, 1008); assert(hb_any_held(&b));
    hb_tick(&b, 1009); assert(!hb_any_held(&b) && b.watchdogs == 1 && releases == 1);
    hb_tick(&b, 2000); assert(b.watchdogs == 1);
    reset(); hello(0, UINT32_MAX - 100);
    uint8_t wait[] = {0x10,0x27};
    command(1, HB_WAIT, wait, 2, UINT32_MAX - 100); hb_tick(&b, UINT32_MAX - 100);
    hb_tick(&b, 398); assert(b.waiting);
    hb_tick(&b, 399); assert(!b.waiting && b.watchdogs == 1);
    command(b.expected, HB_STATUS, NULL, 0, 400);
    assert(b.uptime == ((uint64_t)UINT32_MAX + 401) / 1000);
    reset(); hello(0, 0);
    ready = false; command(1, HB_SYNC, NULL, 0, 0);
    hb_tick(&b, 500); assert(!b.count && b.watchdogs == 1);
}

static void test_reports_and_bus(void)
{
    reset(); hello(0, 0);
    for (uint8_t i = 4; i < 11; ++i) {
        command(b.expected, HB_KEY_DOWN, &i, 1, 0); hb_tick(&b, 0);
    }
    assert(b.dropped == 1 && nreports == 6);
    uint8_t modifier = 0xe7;
    command(b.expected, HB_KEY_DOWN, &modifier, 1, 0); hb_tick(&b, 0);
    assert(report[0] == 0x80);
    uint8_t keys[] = {1,255,4,5,0,0,0,0};
    command(b.expected, HB_KEY_REPORT, keys, 8, 0); hb_tick(&b, 0); assert(!report[1]);
    uint8_t mouse[] = {255,128,127,128};
    command(b.expected, HB_MOUSE, mouse, 4, 0); hb_tick(&b, 0);
    assert(report_kind == 1 && report_len == 4 && report[0] == 7 && report[1] == 129 && report[3] == 129);
    hb_protocol(&b, 1, 0);
    command(b.expected, HB_MOUSE, mouse, 4, 0); hb_tick(&b, 0); assert(report_len == 3);
    uint8_t pointer[] = {7,255,127,0,0,1};
    command(b.expected, HB_POINTER, pointer, 6, 0); hb_tick(&b, 0);
    assert(report_kind == 2 && report[0] == 1 && report[2] == 255 && report[3] == 127);
    uint8_t consumer[] = {0xe9,0,1};
    command(b.expected, HB_CONSUMER, consumer, 3, 0); hb_tick(&b, 0);
    assert(report_kind == 3 && report[0] == 2 && report[1] == 0xe9);
    hb_usb(&b, HB_SUSPENDED, HB_CAUSE_SUSPEND);
    assert(!hb_any_held(&b) && releases == 15);
    command(b.expected, HB_KEY_DOWN, keys + 2, 1, 0); expect_nak(HB_STATE, b.expected);
    hb_wakeup_enabled(&b, true);
    command(b.expected, HB_KEY_DOWN, keys + 2, 1, 0);
    hb_tick(&b, 0); assert(wakes == 1 && b.count == 1);
    for (uint32_t t = 100; t <= 2000; t += 100) {
        command(b.expected, HB_HEARTBEAT, NULL, 0, t); hb_tick(&b, t);
    }
    assert(!b.count && !b.watchdogs && last()->type == HB_EVENT_RELEASED && last()->body[0] == HB_RELEASE_SUSPEND);
    command(b.expected, HB_KEY_DOWN, keys + 2, 1, 2000); hb_tick(&b, 2000);
    hb_usb(&b, HB_CONFIGURED, HB_CAUSE_RESUME); hb_tick(&b, 2001); assert(hb_any_held(&b));
    hb_usb(&b, HB_ATTACHED, HB_CAUSE_RESET);
    assert(!hb_any_held(&b) && !b.wakeup_enabled && !b.mouse_boot);
    hb_leds(&b, 5); assert(last()->type == HB_EVENT_LEDS && b.leds == 5);
    unsigned boot_ack = nframes;
    command(b.expected, HB_BOOTLOADER, NULL, 0, 2002);
    assert(frames[boot_ack].type == HB_ACK);
    assert(boots == 1 && b.bootloader);
    unsigned old = nframes;
    command(b.expected, HB_STATUS, NULL, 0, 2003); assert(nframes == old);
}

static void test_descriptors(void)
{
    uint16_t len;
    const uint8_t *d = hb_descriptor(0x0100, 0, &len);
    assert(len == 18 && d[7] == HB_EP0_SIZE && d[17] == 1);
    /* bcdDevice carries the firmware version as two BCD digits per byte. */
    assert(d[12] == ((HB_FW_MINOR / 10) << 4 | HB_FW_MINOR % 10) &&
           d[13] == ((HB_FW_MAJOR / 10) << 4 | HB_FW_MAJOR % 10));
    const uint8_t *config = hb_descriptor(0x0200, 0, &len);
    assert(len == 84 && config[2] == 84 && config[4] == 3 && config[7] == 0xa0);
    for (uint8_t iface = 0; iface < 3; ++iface) {
        const uint8_t *intf = config + 9 + iface * 25;
        assert(intf[2] == iface && intf[5] == 3);
        assert(intf[6] == (iface < 2) && intf[7] == (iface < 2 ? iface + 1 : 0));
        assert(intf[20] == 0x81 + iface && intf[24] == 1);
        uint16_t n;
        d = hb_descriptor(0x2200, iface, &n);
        assert(d && n == intf[16]);
        /* Parse HID items independently to verify every IN/OUT report bit count. */
        unsigned bits[3][2] = {{0}}, width = 0, count = 0, id = 0;
        uint32_t low = 0, high = 0, ceiling = 0;
        for (uint16_t i = 0; i < n;) {
            uint8_t tag = d[i++], size = tag & 3;
            if (size == 3) size = 4;
            assert(i + size <= n);
            uint32_t val = 0;
            for (uint8_t j = 0; j < size; ++j) val |= (uint32_t)d[i++] << (8 * j);
            if (tag == 0x75) width = val;
            if (tag == 0x95) count = val;
            if (tag == 0x85) { id = val; assert(id < 3); }
            if (tag == 0x81 || tag == 0x91) bits[id][tag == 0x91] += width * count;
            if ((tag & 0xfc) == 0x18) low = val;
            if ((tag & 0xfc) == 0x28) high = val;
            if ((tag & 0xfc) == 0x24) ceiling = val;
            /* A target keeps a record per declared usage, so no range may be
               wide: the keyboard's 256 keys are the widest but one, and the
               consumer array stops at HB_CONSUMER_USAGE_MAX with a logical
               maximum to match. */
            if ((tag & 0xfc) == 0x28) assert(high - low < 1024);
            if (tag == 0x81 && iface == 2 && id == 2)
                assert(!low && high == HB_CONSUMER_USAGE_MAX && ceiling == HB_CONSUMER_USAGE_MAX);
        }
        if (iface == 0) assert(bits[0][0] == 64 && bits[0][1] == 8);
        if (iface == 1) assert(bits[0][0] == 32 && bits[0][1] == 0);
        if (iface == 2) assert(bits[1][0] == 48 && bits[2][0] == 16);
    }
    for (uint8_t i = 0; i < 4; ++i) {
        d = hb_descriptor(0x0300 | i, 0x0409, &len);
        assert(d && d[0] == len && d[1] == 3 && !(len % 2));
    }
    /* Strings come from identity.h and the board serial, as UTF-16LE. */
    d = hb_descriptor(0x0301, 0x0409, &len);
    assert(len == 2 + 2 * (sizeof(HB_MANUFACTURER) - 1) && d[2] == 'T' && d[3] == 0);
    /* The product string is the public name, whole and in order. */
    static const char product[] = "Accessibility Bridge " HB_BOARD_REVISION;
    d = hb_descriptor(0x0302, 0x0409, &len);
    assert(len == 2 + 2 * (sizeof(product) - 1));
    for (size_t i = 0; i < sizeof(product) - 1; ++i)
        assert(d[2 + 2 * i] == product[i] && d[3 + 2 * i] == 0);
    d = hb_descriptor(0x0303, 0x0409, &len);
    assert(len == 2 + 2 * 16 && d[2] == '0' && d[3] == 0 && d[32] == 'F' && d[33] == 0);
    assert(!hb_descriptor(0x0304, 0x0409, &len) && !len);
    assert(!hb_descriptor(0x2200, 3, &len) && !len);
}

int main(void)
{
    test_framing(); test_sequences(); test_queue_and_timing(); test_watchdog();
    test_reports_and_bus(); test_descriptors();
    puts("firmware core: framing, retries, queue, USB backpressure, watchdog, reports and descriptors passed");
    return 0;
}
