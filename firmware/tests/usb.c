/* Native test of the TinyUSB adapter (usb.c) driving the real core. The device
   stack below is a model of what usb.c depends on in TinyUSB 0.18 (usbd.c and
   hid_device.c in Pico SDK 2.3.1): which flags a bus event changes when the
   interrupt raises it, which when tud_task() handles it, and the callback that
   follows. usb.c keeps its state in statics, so this is one continuous run. */
#include "board.h"
#include "hal.h"
#include "tusb.h"
#include "pico/stdlib.h"
#include <assert.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

hb_bridge bridge;

enum { BUS_RESET, BUS_SETUP, BUS_CONFIGURE, BUS_SUSPEND, BUS_RESUME };
static struct {
    bool pullup, connected, suspended, remote_wakeup_en, busy[3];
    uint8_t cfg_num, protocol[3];
    uint8_t queue[32], head, tail;
    uint8_t packet[3][8], length[3];
    unsigned packets[3], wakeups;
} dev;
static uint32_t clock_ms;
static hb_frame frames[512];
static unsigned nframes, mark;

/* dcd_event_handler(): the interrupt drops suspend and resume unless a SETUP
   has arrived since the last reset, and flips the suspended flag at once. */
static void bus(uint8_t event)
{
    if (event == BUS_SUSPEND || event == BUS_RESUME) {
        if (!dev.connected) return;
        dev.suspended = event == BUS_SUSPEND;
    }
    assert((uint8_t)(dev.tail - dev.head) < sizeof(dev.queue));
    dev.queue[dev.tail++ % sizeof(dev.queue)] = event;
}

/* tud_task_ext(): every queued event, in order, inside one call. */
void tud_task(void)
{
    while (dev.head != dev.tail) {
        switch (dev.queue[dev.head++ % sizeof(dev.queue)]) {
        case BUS_RESET: /* usbd_reset() clears the device state and every driver. */
            dev.connected = dev.suspended = dev.remote_wakeup_en = false;
            dev.cfg_num = 0;
            memset(dev.protocol, 0, sizeof(dev.protocol));
            memset(dev.busy, 0, sizeof(dev.busy));
            break;
        case BUS_SETUP: /* Any request, GET_DESCRIPTOR for one. */
            dev.connected = true;
            break;
        case BUS_CONFIGURE: /* SET_CONFIGURATION 1; hidd_open() selects report protocol. */
            dev.connected = true;
            if (dev.cfg_num != 1) {
                dev.cfg_num = 1;
                memset(dev.protocol, HID_PROTOCOL_REPORT, sizeof(dev.protocol));
                tud_mount_cb();
            }
            break;
        case BUS_SUSPEND:
            if (dev.connected) tud_suspend_cb(dev.remote_wakeup_en);
            break;
        case BUS_RESUME:
            if (dev.connected) tud_resume_cb();
            break;
        default: abort();
        }
    }
}

bool tud_rhport_init(uint8_t rhport, const tusb_rhport_init_t *init)
{
    assert(rhport == 0 && init->role == TUSB_ROLE_DEVICE && init->speed == TUSB_SPEED_FULL);
    dev.pullup = true; /* dcd_init() ends in dcd_connect(). */
    return true;
}
bool tud_mounted(void) { return dev.cfg_num != 0; }
bool tud_suspended(void) { return dev.suspended; }
bool tud_connect(void) { dev.pullup = true; return true; }
bool tud_disconnect(void) { dev.pullup = false; return true; }
bool tud_remote_wakeup(void)
{
    if (!dev.suspended || !dev.remote_wakeup_en) return false;
    ++dev.wakeups;
    return true;
}
bool tud_hid_n_ready(uint8_t instance)
{
    return dev.cfg_num && !dev.suspended && !dev.busy[instance];
}
bool tud_hid_n_report(uint8_t instance, uint8_t report_id, void const *report, uint16_t len)
{
    /* usb.c must ask tud_hid_n_ready() first, and fit CFG_TUD_HID_EP_BUFSIZE. */
    assert(instance < 3 && !dev.busy[instance] && len + (report_id != 0) <= 8);
    uint8_t *p = dev.packet[instance];
    if (report_id) *p++ = report_id;
    memcpy(p, report, len);
    dev.length[instance] = (uint8_t)(len + (report_id != 0));
    dev.busy[instance] = true;
    ++dev.packets[instance];
    return true;
}
uint8_t tud_hid_n_get_protocol(uint8_t instance) { return dev.protocol[instance]; }
void sleep_ms(uint32_t ms) { clock_ms += ms; }
void panic(const char *fmt, ...) { (void)fmt; abort(); }

uint32_t board_millis(void) { return clock_ms; }
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
uint8_t hal_identity(uint8_t body[HB_BODY]) { body[0] = 'x'; return 1; }
void hal_bootloader(void) { usb_prepare_bootloader(); }

/* The target polls an endpoint: its pending report is delivered. */
static void poll(uint8_t instance) { dev.busy[instance] = false; }

/* hidd_control_xfer_cb(), GET_REPORT: the stack writes a non-zero report ID
   itself and hands the callback the rest of an 8-byte buffer. */
static uint16_t get_report(uint8_t instance, uint8_t id, hid_report_type_t type,
                           uint16_t wlength, uint8_t out[8])
{
    uint16_t room = wlength < 8 ? wlength : 8, sent = 0;
    memset(out, 0xee, 8);
    if (id && room > 1) { out[sent++] = id; --room; }
    return sent + tud_hid_get_report_cb(instance, id, type, out + sent, room);
}

/* hidd_control_xfer_cb(), SET_PROTOCOL: stored first, then reported. */
static void set_protocol(uint8_t instance, uint8_t protocol)
{
    dev.protocol[instance] = protocol;
    tud_hid_set_protocol_cb(instance, protocol);
}

static void command(uint8_t type, const uint8_t *body, uint8_t len)
{
    hb_frame f = {.seq = bridge.expected, .type = type, .len = len};
    if (len) memcpy(f.body, body, len);
    uint8_t wire[HB_WIRE], n = hb_encode(&f, wire);
    for (uint8_t i = 0; i < n; ++i) hb_receive(&bridge, wire[i], clock_ms);
}

/* The next frame the board sent to the control computer, in order. */
#define EXPECT(type_, a, b) do { \
    assert(mark < nframes); \
    const hb_frame *f_ = &frames[mark++]; \
    assert(f_->type == (type_)); \
    assert(f_->len < 1 || f_->body[0] == (a)); \
    assert(f_->len < 2 || f_->body[1] == (b)); \
} while (0)
#define QUIET() assert(mark == nframes)
#define SKIP() (mark = nframes)

int main(void)
{
    uint8_t version = HB_VERSION, key = 4, second = 5, reply[8];
    unsigned sent;

    /* First enumeration: a device that was only attached reports no reset. */
    hb_init(&bridge); usb_init();
    assert(dev.pullup && bridge.usb == HB_ATTACHED);
    bus(BUS_RESET); bus(BUS_SETUP); usb_task(); QUIET();
    bus(BUS_CONFIGURE); usb_task();
    EXPECT(HB_EVENT_USB, HB_CONFIGURED, HB_CAUSE_CONFIGURED); QUIET();
    assert(bridge.usb == HB_CONFIGURED && hal_reports_idle());

    /* A press and its release take separate polls. */
    command(HB_HELLO, &version, 1);
    command(HB_KEY_DOWN, &key, 1); hb_tick(&bridge, clock_ms);
    assert(dev.packets[0] == 1 && dev.length[0] == 8 && dev.packet[0][2] == 4);
    command(HB_KEY_UP, &key, 1); hb_tick(&bridge, clock_ms);
    assert(bridge.count == 1 && !hal_reports_idle());
    poll(0); hb_tick(&bridge, clock_ms);
    assert(dev.packets[0] == 2 && dev.packet[0][2] == 0);
    poll(0);

    /* SET_IDLE repeats the held keyboard report every rate * 4 ms; 0 stops it. */
    command(HB_KEY_DOWN, &key, 1); hb_tick(&bridge, clock_ms); poll(0);
    assert(tud_hid_set_idle_cb(0, 1) && !tud_hid_set_idle_cb(3, 1));
    sent = dev.packets[0];
    clock_ms += 3; usb_task(); assert(dev.packets[0] == sent);
    clock_ms += 1; usb_task();
    assert(dev.packets[0] == sent + 1 && dev.packet[0][2] == 4); poll(0);
    assert(tud_hid_set_idle_cb(0, 0));
    clock_ms += 100; usb_task(); assert(dev.packets[0] == sent + 1);

    /* The boot mouse: deltas are never replayed, and boot protocol drops the wheel. */
    uint8_t mouse[] = {1,127,129,4};
    command(HB_MOUSE, mouse, 4); hb_tick(&bridge, clock_ms);
    assert(dev.length[1] == 4 && !memcmp(dev.packet[1], mouse, 4)); poll(1);
    assert(get_report(1, 0, HID_REPORT_TYPE_INPUT, 4, reply) == 4);
    assert(reply[0] == 1 && !reply[1] && !reply[2] && !reply[3]);
    set_protocol(1, HID_PROTOCOL_BOOT); assert(bridge.mouse_boot);
    command(HB_MOUSE, mouse, 4); hb_tick(&bridge, clock_ms);
    assert(dev.length[1] == 3); poll(1);
    assert(get_report(1, 0, HID_REPORT_TYPE_INPUT, 8, reply) == 3 && reply[0] == 1 && !reply[1]);

    /* The keyboard's LED output report, and nothing else, reaches the bridge. */
    uint8_t leds = 0x1f, other = 0;
    tud_hid_set_report_cb(0, 0, HID_REPORT_TYPE_OUTPUT, &leds, 1);
    assert(bridge.leds == 0x1f);
    tud_hid_set_report_cb(1, 0, HID_REPORT_TYPE_OUTPUT, &other, 1);
    tud_hid_set_report_cb(0, 0, HID_REPORT_TYPE_FEATURE, &other, 1);
    tud_hid_set_report_cb(0, 0, HID_REPORT_TYPE_OUTPUT, &other, 0);
    assert(bridge.leds == 0x1f);
    assert(get_report(0, 0, HID_REPORT_TYPE_OUTPUT, 1, reply) == 1 && reply[0] == 0x1f);
    assert(!get_report(0, 0, HID_REPORT_TYPE_FEATURE, 8, reply));
    assert(!get_report(2, 0, HID_REPORT_TYPE_INPUT, 8, reply));

    /* Interface 2's two report IDs share one endpoint and take separate polls. */
    uint8_t pointer[] = {1,0xff,0x7f,1,0,2}, consumer[] = {0xe9,0,1};
    command(HB_POINTER, pointer, 6); command(HB_CONSUMER, consumer, 3);
    hb_tick(&bridge, clock_ms);
    assert(dev.length[2] == 7 && dev.packet[2][0] == 1 && !memcmp(dev.packet[2] + 1, pointer, 6));
    hb_tick(&bridge, clock_ms); assert(bridge.count == 1);
    poll(2); hb_tick(&bridge, clock_ms);
    assert(dev.length[2] == 3 && dev.packet[2][0] == 2 && dev.packet[2][1] == 0xe9 && !dev.packet[2][2]);
    /* GET_REPORT: TinyUSB places the ID, the callback the body; the wheel is not replayed. */
    assert(get_report(2, 1, HID_REPORT_TYPE_INPUT, 7, reply) == 7);
    assert(reply[0] == 1 && reply[1] == 1 && reply[2] == 0xff && reply[3] == 0x7f &&
           reply[4] == 1 && !reply[5] && !reply[6]);
    assert(get_report(2, 2, HID_REPORT_TYPE_INPUT, 3, reply) == 3);
    assert(reply[0] == 2 && reply[1] == 0xe9 && !reply[2]);

    /* RELEASE_ALL: zero reports wait behind a pending packet and precede later input. */
    command(HB_RELEASE_ALL, NULL, 0); usb_task();
    assert(!hal_reports_idle());
    assert(!dev.packet[0][2] && dev.length[1] == 3 && !dev.packet[1][0]);
    assert(dev.packet[2][0] == 2 && dev.packet[2][1] == 0xe9);
    sent = dev.packets[0];
    command(HB_KEY_DOWN, &second, 1); hb_tick(&bridge, clock_ms);
    assert(bridge.count == 1 && dev.packets[0] == sent);
    poll(0); poll(1); poll(2); usb_task();
    /* The pointer lets go where it is (0x7fff, 1), never at (0, 0). */
    assert(dev.length[2] == 7 && dev.packet[2][0] == 1 && !dev.packet[2][1]);
    assert(dev.packet[2][2] == 0xff && dev.packet[2][3] == 0x7f && dev.packet[2][4] == 1 &&
           !dev.packet[2][5] && !dev.packet[2][6]);
    hb_tick(&bridge, clock_ms); assert(bridge.count == 1);
    poll(2); usb_task();
    assert(dev.length[2] == 3 && dev.packet[2][0] == 2 && !dev.packet[2][1] && !dev.packet[2][2]);
    poll(2); assert(hal_reports_idle());
    hb_tick(&bridge, clock_ms);
    assert(dev.packets[0] == sent + 1 && dev.packet[0][2] == 5); poll(0);

    /* Suspend releases everything. Remote wakeup waits out the 5 ms USB asks
       for, and on resume the zero report goes out before the queued key. */
    dev.remote_wakeup_en = true; /* SET_FEATURE(DEVICE_REMOTE_WAKEUP) */
    SKIP(); bus(BUS_SUSPEND); usb_task();
    EXPECT(HB_EVENT_RELEASED, HB_RELEASE_SUSPEND, 0);
    EXPECT(HB_EVENT_HELD, 0, 0);
    EXPECT(HB_EVENT_USB, HB_SUSPENDED, HB_CAUSE_SUSPEND);
    EXPECT(HB_EVENT_WAKEUP, 1, 0); QUIET();
    assert(!hb_any_held(&bridge) && bridge.usb == HB_SUSPENDED && bridge.wakeup_enabled);
    uint32_t suspended = clock_ms;
    command(HB_KEY_DOWN, &key, 1); hb_tick(&bridge, clock_ms);
    assert(dev.wakeups == 1 && clock_ms == suspended + 5 && bridge.count == 1);
    SKIP(); bus(BUS_RESUME); usb_task();
    EXPECT(HB_EVENT_USB, HB_CONFIGURED, HB_CAUSE_WAKEUP); QUIET();
    assert(dev.packet[0][2] == 0);
    hb_tick(&bridge, clock_ms); assert(bridge.count == 1);
    poll(0); hb_tick(&bridge, clock_ms);
    assert(dev.packet[0][2] == 4); poll(0);

    /* A resume the host started reports RESUME, not WAKEUP. */
    bus(BUS_SUSPEND); usb_task();
    SKIP(); bus(BUS_RESUME); usb_task();
    EXPECT(HB_EVENT_USB, HB_CONFIGURED, HB_CAUSE_RESUME); QUIET();
    poll(0);

    /* A bus reset seen on its own clears what the target had set. */
    assert(tud_hid_set_idle_cb(0, 1));
    sent = dev.packets[0];
    bus(BUS_RESET); usb_task();
    EXPECT(HB_EVENT_RELEASED, HB_RELEASE_RESET, 0);
    EXPECT(HB_EVENT_USB, HB_ATTACHED, HB_CAUSE_RESET); QUIET();
    assert(bridge.usb == HB_ATTACHED && !bridge.wakeup_enabled && !bridge.mouse_boot && !bridge.leds);
    bus(BUS_CONFIGURE); usb_task();
    EXPECT(HB_EVENT_USB, HB_CONFIGURED, HB_CAUSE_CONFIGURED); QUIET();
    clock_ms += 100; usb_task(); assert(dev.packets[0] == sent);

    /* Reset and SET_CONFIGURATION handled inside one tud_task(): both reported, in order. */
    bus(BUS_RESET); bus(BUS_CONFIGURE); usb_task();
    EXPECT(HB_EVENT_RELEASED, HB_RELEASE_RESET, 0);
    EXPECT(HB_EVENT_USB, HB_ATTACHED, HB_CAUSE_RESET);
    EXPECT(HB_EVENT_USB, HB_CONFIGURED, HB_CAUSE_CONFIGURED); QUIET();

    /* The same from suspend, where the reset also ends the suspend. */
    bus(BUS_SUSPEND); usb_task();
    SKIP(); bus(BUS_RESET); usb_task();
    EXPECT(HB_EVENT_RELEASED, HB_RELEASE_RESET, 0);
    EXPECT(HB_EVENT_USB, HB_ATTACHED, HB_CAUSE_RESET); QUIET();
    bus(BUS_CONFIGURE); usb_task();
    bus(BUS_SUSPEND); usb_task();
    SKIP(); bus(BUS_RESET); bus(BUS_CONFIGURE); usb_task();
    EXPECT(HB_EVENT_RELEASED, HB_RELEASE_RESET, 0);
    EXPECT(HB_EVENT_USB, HB_ATTACHED, HB_CAUSE_RESET);
    EXPECT(HB_EVENT_USB, HB_CONFIGURED, HB_CAUSE_CONFIGURED); QUIET();

    /* Suspended while only addressed: the resume returns to ATTACHED. */
    bus(BUS_RESET); usb_task();
    bus(BUS_SETUP); usb_task();
    bus(BUS_SUSPEND); usb_task();
    assert(bridge.usb == HB_SUSPENDED);
    SKIP(); bus(BUS_RESUME); usb_task();
    EXPECT(HB_EVENT_RELEASED, HB_RELEASE_RESET, 0);
    EXPECT(HB_EVENT_USB, HB_ATTACHED, HB_CAUSE_RESUME); QUIET();
    bus(BUS_CONFIGURE); usb_task();
    EXPECT(HB_EVENT_USB, HB_CONFIGURED, HB_CAUSE_CONFIGURED); QUIET();

    /* USB_DETACH. Off the bus, nothing the controller raises may replace
       DETACHED: USB_ATTACH acts only on DETACHED, so the board would stay off. */
    command(HB_KEY_DOWN, &key, 1); hb_tick(&bridge, clock_ms); poll(0);
    SKIP(); command(HB_USB_DETACH, NULL, 0);
    EXPECT(HB_EVENT_RELEASED, HB_RELEASE_DETACH, 0);
    EXPECT(HB_EVENT_HELD, 0, 0);
    EXPECT(HB_EVENT_USB, HB_DETACHED, HB_CAUSE_DETACH);
    EXPECT(HB_ACK, 0, 0); QUIET();
    assert(!dev.pullup && bridge.usb == HB_DETACHED);
    bus(BUS_SUSPEND); usb_task();                   /* a suspend with no reset before it */
    bus(BUS_RESUME); usb_task();
    bus(BUS_RESET); bus(BUS_CONFIGURE); usb_task(); /* a SET_CONFIGURATION already queued */
    bus(BUS_RESET); usb_task();                     /* the open bus read as a reset */
    QUIET(); assert(bridge.usb == HB_DETACHED);
    command(HB_USB_ATTACH, NULL, 0);
    EXPECT(HB_EVENT_RELEASED, HB_RELEASE_RESET, 0);
    EXPECT(HB_EVENT_USB, HB_ATTACHED, HB_CAUSE_ATTACH);
    EXPECT(HB_ACK, 0, 0); QUIET();
    assert(dev.pullup && bridge.usb == HB_ATTACHED);
    bus(BUS_RESET); bus(BUS_CONFIGURE); usb_task();
    EXPECT(HB_EVENT_USB, HB_CONFIGURED, HB_CAUSE_CONFIGURED); QUIET();
    /* The detach's zero report was kept and is the first thing the target sees. */
    assert(dev.packet[0][2] == 0); poll(0);

    /* More bus events inside one tud_task() than the adapter holds: detach, safely. */
    for (uint8_t i = 0; i < 5; ++i) { bus(BUS_SUSPEND); bus(BUS_RESUME); }
    usb_task();
    EXPECT(HB_EVENT_RELEASED, HB_RELEASE_DETACH, 0);
    EXPECT(HB_EVENT_USB, HB_DETACHED, HB_CAUSE_DETACH); QUIET();
    assert(!dev.pullup && bridge.usb == HB_DETACHED);
    command(HB_USB_ATTACH, NULL, 0); assert(dev.pullup);
    bus(BUS_RESET); bus(BUS_CONFIGURE); usb_task();
    assert(bridge.usb == HB_CONFIGURED);

    /* BOOTLOADER leaves the bus before the ROM loader takes the port. */
    command(HB_BOOTLOADER, NULL, 0);
    assert(!dev.pullup && bridge.bootloader && bridge.usb == HB_DETACHED);

    puts("USB adapter: enumeration, report ordering, class requests, idle, deferred release, "
         "suspend and wakeup, reset inference, detach and overflow passed");
    return 0;
}
