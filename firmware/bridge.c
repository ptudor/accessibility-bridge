#include "bridge.h"
#include "hal.h"
#include <string.h>

static uint16_t le16(const uint8_t *p)
{
    return (uint16_t)p[0] | (uint16_t)p[1] << 8;
}
static void put16(uint8_t *p, uint16_t value)
{
    p[0] = value;
    p[1] = value >> 8;
}

static void send(const hb_frame *f)
{
    uint8_t wire[HB_WIRE];
    hal_write(wire, hb_encode(f, wire));
}

static void event(hb_bridge *b, uint8_t type, uint8_t a, uint8_t c, uint8_t len)
{
    hb_frame f = {.seq = b->events++, .type = type, .len = len, .body = {a, c}};
    send(&f);
}

static bool held_any(const hb_held *h)
{
    if (h->mouse_buttons || h->pointer_buttons || h->consumer)
        return true;
    for (uint8_t i = 0; i < 8; ++i)
        if (h->keyboard[i])
            return true;
    return false;
}

bool hb_any_held(const hb_bridge *b) { return held_any(&b->held); }
bool hb_busy(const hb_bridge *b) { return hb_any_held(b) || b->count || b->waiting; }

static void release(hb_bridge *b, uint8_t reason)
{
    uint8_t mask = 0;
    for (uint8_t i = 0; i < 8; ++i)
        if (b->held.keyboard[i])
            mask |= 1 << HB_REPORT_KEYBOARD;
    if (b->held.mouse_buttons) mask |= 1 << HB_REPORT_MOUSE;
    if (b->held.pointer_buttons) mask |= 1 << HB_REPORT_POINTER;
    if (b->held.consumer) mask |= 1 << HB_REPORT_CONSUMER;
    memset(&b->held, 0, sizeof(b->held));
    b->head = b->count = 0;
    b->waiting = b->waking = false;
    hal_release(mask);
    event(b, HB_EVENT_RELEASED, reason, 0, 1);
    if (mask) event(b, HB_EVENT_HELD, 0, 0, 1);
}

void hb_init(hb_bridge *b)
{
    memset(b, 0, sizeof(*b));
    /* HID 1.11: reset selects report protocol. BIOS selects boot explicitly. */
    b->usb = HB_ATTACHED;
}

void hb_usb(hb_bridge *b, uint8_t state, uint8_t cause)
{
    if (state != HB_CONFIGURED) {
        uint8_t reason = HB_RELEASE_RESET;
        if (cause == HB_CAUSE_SUSPEND) reason = HB_RELEASE_SUSPEND;
        if (cause == HB_CAUSE_DETACH || cause == HB_CAUSE_POWER)
            reason = HB_RELEASE_DETACH;
        release(b, reason);
    }
    b->usb = state;
    if (state != HB_SUSPENDED) b->waking = false;
    if (cause == HB_CAUSE_RESET || state == HB_DETACHED) {
        b->wakeup_enabled = false;
        b->keyboard_boot = b->mouse_boot = false;
        b->leds = 0;
    }
    event(b, HB_EVENT_USB, state, cause, 2);
}

void hb_leds(hb_bridge *b, uint8_t leds)
{
    b->leds = leds;
    event(b, HB_EVENT_LEDS, leds, 0, 1);
}

void hb_wakeup_enabled(hb_bridge *b, bool enabled)
{
    b->wakeup_enabled = enabled;
    event(b, HB_EVENT_WAKEUP, enabled, 0, 1);
}

void hb_protocol(hb_bridge *b, uint8_t iface, uint8_t protocol)
{
    if (iface == 0) b->keyboard_boot = protocol == 0;
    else b->mouse_boot = protocol == 0;
    event(b, HB_EVENT_PROTOCOL, iface, protocol, 2);
}

static void remember(hb_bridge *b, const hb_frame *f)
{
    uint8_t slot = f->seq % HB_WINDOW;
    b->replies[slot] = *f;
    b->reply_valid |= 1 << slot;
}

static void nak(hb_bridge *b, uint8_t seq, uint8_t code, bool consumed)
{
    if (code <= HB_LENGTH) ++b->rx_errors;
    hb_frame f = {.seq = seq, .type = HB_NAK, .len = 2,
                  .body = {code, b->expected}};
    if (consumed) remember(b, &f);
    send(&f);
}

static int8_t body_length(uint8_t type)
{
    switch (type) {
    case HB_HELLO: case HB_KEY_DOWN: case HB_KEY_UP: return 1;
    case HB_KEY_REPORT: return 8;
    case HB_MOUSE: return 4;
    case HB_POINTER: return 6;
    case HB_CONSUMER: return 3;
    case HB_WAIT: return 2;
    case HB_STATUS: case HB_IDENTITY: case HB_RELEASE_ALL: case HB_SYNC:
    case HB_HEARTBEAT: case HB_USB_DETACH: case HB_USB_ATTACH:
    case HB_WAKEUP: case HB_BOOTLOADER: return 0;
    default: return -1;
    }
}

static bool input(uint8_t type)
{
    return type == HB_KEY_DOWN || type == HB_KEY_UP || type == HB_KEY_REPORT ||
           type == HB_MOUSE || type == HB_POINTER || type == HB_CONSUMER;
}

static void update_clock(hb_bridge *b, uint32_t now)
{
    uint32_t elapsed = now - b->clock;
    b->clock = now;
    if (!elapsed) return;
    b->uptime += elapsed / 1000;
    b->subsecond += elapsed % 1000;
    if (b->subsecond >= 1000) { ++b->uptime; b->subsecond -= 1000; }
}

static void status(hb_bridge *b, hb_frame *reply)
{
    reply->len = 14;
    uint8_t *p = reply->body;
    p[0] = b->usb;
    p[1] = b->wakeup_enabled | (hb_any_held(b) << 1) |
           (b->keyboard_boot << 2) | (b->mouse_boot << 3);
    p[2] = b->leds;
    p[3] = b->count;
    uint32_t seconds = b->uptime;
    for (uint8_t i = 0; i < 4; ++i) p[4 + i] = seconds >> (8 * i);
    put16(p + 8, b->watchdogs);
    put16(p + 10, b->rx_errors);
    put16(p + 12, b->dropped);
}

static void handle(hb_bridge *b, const hb_frame *f, uint32_t now)
{
    if (b->bootloader) return;
    update_clock(b, now);
    hb_frame reply = {.seq = f->seq, .type = HB_ACK};
    int8_t len = body_length(f->type);
    /* The simulator treats any one-byte HELLO as version negotiation. */
    if (f->type == HB_HELLO && f->len == 1) {
        release(b, HB_RELEASE_COMMAND);
        b->reply_valid = 0;
        b->session = true;
        b->expected = f->seq + 1;
        b->last_frame = now;
        reply.len = 4;
        reply.body[0] = HB_VERSION;
        reply.body[1] = HB_FW_MAJOR;
        reply.body[2] = HB_FW_MINOR;
        reply.body[3] = HB_QUEUE;
        remember(b, &reply);
        send(&reply);
        return;
    }
    if (!b->session) { nak(b, f->seq, HB_NO_SESSION, false); return; }
    if (f->seq != b->expected) {
        uint8_t slot = f->seq % HB_WINDOW;
        if ((uint8_t)(b->expected - f->seq) <= HB_WINDOW &&
            (b->reply_valid & (1 << slot)) && b->replies[slot].seq == f->seq) {
            b->last_frame = now;
            send(&b->replies[slot]);
        } else nak(b, f->seq, HB_SEQUENCE, false);
        return;
    }
    b->last_frame = now;
    uint8_t error = 0;
    bool queued = input(f->type) || f->type == HB_WAIT || f->type == HB_SYNC;
    if (len < 0) error = HB_TYPE;
    else if (f->len != len) error = HB_LENGTH;
    else if (f->type == HB_WAIT && (!le16(f->body) || le16(f->body) > 10000))
        error = HB_STATE;
    else if (input(f->type) && b->usb != HB_CONFIGURED &&
             !(b->usb == HB_SUSPENDED && b->wakeup_enabled)) error = HB_STATE;
    else if (queued && b->count == HB_QUEUE) error = HB_QUEUE_FULL;
    if (error) {
        if (error != HB_QUEUE_FULL) ++b->expected;
        nak(b, f->seq, error, error != HB_QUEUE_FULL);
        return;
    }
    ++b->expected;
    if (queued) {
        hb_queued *q = &b->queue[(b->head + b->count++) % HB_QUEUE];
        q->seq = f->seq;
        q->type = f->type;
        memcpy(q->body, f->body, f->len);
    } else switch (f->type) {
    case HB_STATUS: status(b, &reply); break;
    case HB_IDENTITY: reply.len = hal_identity(reply.body); break;
    case HB_RELEASE_ALL: release(b, HB_RELEASE_COMMAND); break;
    case HB_USB_DETACH:
        hb_usb(b, HB_DETACHED, HB_CAUSE_DETACH);
        hal_attach(false);
        break;
    case HB_USB_ATTACH:
        if (b->usb == HB_DETACHED) {
            hb_usb(b, HB_ATTACHED, HB_CAUSE_ATTACH);
            hal_attach(true);
        }
        break;
    case HB_WAKEUP:
        reply.len = 1;
        reply.body[0] = b->usb == HB_SUSPENDED && b->wakeup_enabled && hal_wakeup();
        break;
    case HB_BOOTLOADER:
        /* Drain the ACK before the UART is disabled or the USB device detaches. */
        remember(b, &reply);
        send(&reply);
        hb_usb(b, HB_DETACHED, HB_CAUSE_DETACH);
        b->bootloader = true;
        hal_bootloader();
        return;
    default: break;
    }
    remember(b, &reply);
    send(&reply);
}

void hb_receive(hb_bridge *b, uint8_t byte, uint32_t now)
{
    hb_frame frame;
    int8_t result = hb_decode_byte(&b->decoder, byte, &frame);
    if (result < 0) nak(b, 0, -result, false);
    else if (result > 0) handle(b, &frame, now);
}

static uint8_t clamp(uint8_t value) { return value == 128 ? 129 : value; }

static bool execute(hb_bridge *b, const hb_queued *q)
{
    hb_held next = b->held;
    uint8_t report[8] = {0}, size = 0, kind = HB_REPORT_KEYBOARD;
    uint8_t u = q->body[0];
    switch (q->type) {
    case HB_KEY_DOWN: case HB_KEY_UP:
        if (u >= 0xe0 && u <= 0xe7) {
            if (q->type == HB_KEY_DOWN) next.keyboard[0] |= 1 << (u - 0xe0);
            else next.keyboard[0] &= ~(1 << (u - 0xe0));
        } else if (q->type == HB_KEY_UP) {
            for (uint8_t i = 2; i < 8; ++i)
                if (next.keyboard[i] == u) next.keyboard[i] = 0;
        } else {
            bool placed = false;
            for (uint8_t i = 2; i < 8; ++i)
                if (next.keyboard[i] == u) placed = true;
            for (uint8_t i = 2; i < 8 && !placed; ++i)
                if (!next.keyboard[i]) { next.keyboard[i] = u; placed = true; }
            if (!placed) { ++b->dropped; return true; }
        }
        memcpy(report, next.keyboard, 8);
        size = 8;
        break;
    case HB_KEY_REPORT:
        memcpy(next.keyboard, q->body, 8);
        next.keyboard[1] = 0;
        memcpy(report, next.keyboard, 8);
        size = 8;
        break;
    case HB_MOUSE:
        kind = HB_REPORT_MOUSE;
        next.mouse_buttons = report[0] = u & 7;
        for (uint8_t i = 1; i < 4; ++i) report[i] = clamp(q->body[i]);
        size = b->mouse_boot ? 3 : 4;
        break;
    case HB_POINTER:
        kind = HB_REPORT_POINTER;
        report[0] = 1;
        memcpy(report + 1, q->body, 6);
        next.pointer_buttons = report[1] &= 7;
        size = 7;
        break;
    case HB_CONSUMER:
        kind = HB_REPORT_CONSUMER;
        next.consumer = q->body[2] ? le16(q->body) : 0;
        report[0] = 2;
        put16(report + 1, next.consumer);
        size = 3;
        break;
    default: return true;
    }
    if (!hal_report(kind, report, size)) return false;
    bool was = hb_any_held(b);
    b->held = next;
    if (was != hb_any_held(b)) event(b, HB_EVENT_HELD, hb_any_held(b), 0, 1);
    return true;
}

void hb_tick(hb_bridge *b, uint32_t now)
{
    if (b->bootloader) return;
    update_clock(b, now);
    if (hb_busy(b) && (uint32_t)(now - b->last_frame) >= HB_WATCHDOG_MS) {
        ++b->watchdogs;
        release(b, HB_RELEASE_WATCHDOG);
        return;
    }
    if (b->waiting) {
        if ((uint32_t)(now - b->wait_start) < b->wait_ms) return;
        b->waiting = false;
    }
    if (!b->count) return;
    hb_queued *q = &b->queue[b->head];
    if (input(q->type) && b->usb == HB_SUSPENDED) {
        if (!b->wakeup_enabled) { release(b, HB_RELEASE_SUSPEND); return; }
        if (!b->waking) {
            b->waking = true;
            b->wake_start = now;
            (void)hal_wakeup();
        } else if ((uint32_t)(now - b->wake_start) >= HB_WAKEUP_MS) {
            release(b, HB_RELEASE_SUSPEND);
        }
        return;
    }
    /* USB backpressure also gates WAIT/SYNC, including across interfaces. */
    if (!hal_reports_idle()) return;
    if (input(q->type) && b->usb != HB_CONFIGURED) return;
    if (q->type == HB_WAIT) {
        b->waiting = true;
        b->wait_start = now;
        b->wait_ms = le16(q->body);
    } else if (q->type == HB_SYNC) {
        event(b, HB_EVENT_DONE, q->seq, 0, 1);
    } else if (!execute(b, q)) return;
    b->head = (b->head + 1) % HB_QUEUE;
    --b->count;
}
