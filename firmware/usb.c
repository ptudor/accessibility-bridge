#include "board.h"
#include "hal.h"
#include "tusb.h"
#include "pico/stdlib.h"
#include <string.h>

/* TinyUSB's device callbacks run inside tud_task() on the main loop, never in
   an interrupt, so none of this state needs atomics. */
static uint8_t current[4][8] = {{0}, {0}, {1}, {2}};
static const uint8_t sizes[4] = {8,4,7,3};
static uint8_t idle_rate[4], releases;
static uint32_t sent_at[4], suspended_at;
static uint8_t events[8][2], event_head, event_tail;
/* Last state handed to post(); hb_init() starts the bridge as attached too. */
static uint8_t posted = HB_ATTACHED;
static bool event_overflow, detached, remote_allowed, remote_signalled;

static void post(uint8_t state, uint8_t cause)
{
    /* DETACHED is the bridge's own state, set by command, and USB_ATTACH acts
       only on it. TinyUSB still delivers whatever the controller raises with
       the pull-up off, and a SET_CONFIGURATION already queued when it went:
       reporting either would replace DETACHED and leave the board off the bus. */
    if (detached) return;
    uint8_t next = (event_head + 1) & 7;
    if (next == event_tail) event_overflow = true;
    else {
        events[event_head][0] = state;
        events[event_head][1] = cause;
        event_head = next;
        posted = state;
    }
}

static uint8_t report_size(uint8_t kind)
{
    /* TinyUSB owns the protocol mode; it resets it with the interface. */
    if (kind == HB_REPORT_MOUSE && tud_hid_n_get_protocol(1) == HID_PROTOCOL_BOOT)
        return 3;
    return sizes[kind];
}

static int8_t report_kind(uint8_t instance, uint8_t id)
{
    if (instance < 2 && id == 0) return instance;
    if (instance == 2 && id >= 1 && id <= 2) return id + 1;
    return -1;
}

static bool send_report(uint8_t kind, const uint8_t *data, uint8_t len)
{
    if (detached || bridge.usb != HB_CONFIGURED) return false;
    uint8_t instance = kind < 2 ? kind : 2;
    uint8_t id = kind < 2 ? 0 : kind - 1;
    const uint8_t *payload = kind < 2 ? data : data + 1;
    uint8_t payload_len = kind < 2 ? len : len - 1;
    if (!tud_hid_n_ready(instance) ||
        !tud_hid_n_report(instance, id, payload, payload_len)) return false;
    sent_at[kind] = board_millis();
    return true;
}

bool hal_report(uint8_t kind, const uint8_t *data, uint8_t len)
{
    if (kind >= 4 || releases || !send_report(kind, data, len)) return false;
    memcpy(current[kind], data, len);
    /* Relative deltas must never be replayed by GET_REPORT or idle repeats. */
    if (kind == HB_REPORT_MOUSE) memset(current[kind] + 1, 0, 3);
    if (kind == HB_REPORT_POINTER) current[kind][6] = 0;
    return true;
}

bool hal_reports_idle(void)
{
    if (event_head != event_tail || event_overflow) return false;
    if (detached || bridge.usb != HB_CONFIGURED) return true;
    if (releases) return false;
    for (uint8_t instance = 0; instance < 3; ++instance)
        if (!tud_hid_n_ready(instance)) return false;
    return true;
}

void hal_release(uint8_t mask)
{
    releases |= mask;
    for (uint8_t i = 0; i < 4; ++i) if (mask & (1 << i)) {
        if (i == HB_REPORT_POINTER) {
            /* Let go where the pointer is: a release must not drag to (0,0).
               Bytes 2-5 keep the last position; the wheel byte is already 0. */
            current[i][1] = 0;
            continue;
        }
        memset(current[i], 0, sizeof(current[i]));
        if (i >= 2) current[i][0] = i - 1;
    }
}

void hal_attach(bool attach)
{
    detached = !attach;
    remote_signalled = false;
    /* bridge.c reports these transitions itself; keep the inference in step. */
    posted = attach ? HB_ATTACHED : HB_DETACHED;
    if (attach) (void)tud_connect();
    else {
        remote_allowed = false;
        (void)tud_disconnect();
    }
}

bool hal_wakeup(void)
{
    if (detached || bridge.usb != HB_SUSPENDED || !remote_allowed) return false;
    /* USB 2.0 7.1.7.7: at least 5 ms suspended before upstream resume. */
    uint32_t elapsed = board_millis() - suspended_at;
    if (elapsed < 5) sleep_ms(5 - elapsed);
    if (!tud_remote_wakeup()) return false;
    remote_signalled = true;
    return true;
}

void tud_mount_cb(void)
{
    /* TinyUSB has no bus-reset callback. A SET_CONFIGURATION arriving while
       the last reported state was still configured or suspended means the
       device left that state (bus reset, or SET_CONFIGURATION 0) inside one
       tud_task() call, where usb_task() below could not see it. */
    if (posted != HB_ATTACHED) post(HB_ATTACHED, HB_CAUSE_RESET);
    post(HB_CONFIGURED, HB_CAUSE_CONFIGURED);
}

/* No tud_umount_cb: the RP2040 driver forces VBUS detection and never reports
   an unplug, so TinyUSB only calls it for SET_CONFIGURATION 0, which usb_task()
   sees as the mounted flag dropping and reports as a reset. */

void tud_suspend_cb(bool remote_wakeup_en)
{
    suspended_at = board_millis();
    remote_allowed = remote_wakeup_en;
    post(HB_SUSPENDED, HB_CAUSE_SUSPEND);
}

void tud_resume_cb(void)
{
    uint8_t cause = remote_signalled ? HB_CAUSE_WAKEUP : HB_CAUSE_RESUME;
    remote_signalled = false;
    post(tud_mounted() ? HB_CONFIGURED : HB_ATTACHED, cause);
}

uint16_t tud_hid_get_report_cb(uint8_t instance, uint8_t report_id,
                               hid_report_type_t report_type,
                               uint8_t *buffer, uint16_t reqlen)
{
    if (!reqlen) return 0;
    if (report_type == HID_REPORT_TYPE_OUTPUT) {
        /* The boot keyboard's LED report reads back as the target last wrote it. */
        if (instance != 0 || report_id != 0) return 0;
        buffer[0] = bridge.leds;
        return 1;
    }
    if (report_type != HID_REPORT_TYPE_INPUT) return 0;
    int8_t kind = report_kind(instance, report_id);
    if (kind < 0) return 0;
    /* TinyUSB has already placed the report ID; hand it the body only. */
    uint8_t len = report_size((uint8_t)kind);
    const uint8_t *data = current[kind] + (kind >= 2);
    len -= kind >= 2;
    if (len > reqlen) len = reqlen;
    memcpy(buffer, data, len);
    return len;
}

void tud_hid_set_report_cb(uint8_t instance, uint8_t report_id,
                           hid_report_type_t report_type,
                           uint8_t const *buffer, uint16_t bufsize)
{
    if (instance == 0 && report_id == 0 && report_type == HID_REPORT_TYPE_OUTPUT && bufsize >= 1)
        hb_leds(&bridge, buffer[0]);
}

void tud_hid_set_protocol_cb(uint8_t instance, uint8_t protocol)
{
    if (instance >= 2 || protocol > 1) return;
    hb_protocol(&bridge, instance, protocol);
}

bool tud_hid_set_idle_cb(uint8_t instance, uint8_t idle)
{
    if (instance >= 3) return false;
    /* TinyUSB drops the request's report ID, so interface 2's rate is shared. */
    if (instance < 2) {
        idle_rate[instance] = idle;
        sent_at[instance] = board_millis();
    } else for (uint8_t i = 2; i < 4; ++i) {
        idle_rate[i] = idle;
        sent_at[i] = board_millis();
    }
    return true;
}

void usb_init(void)
{
    tusb_rhport_init_t init = {
        .role = TUSB_ROLE_DEVICE,
        .speed = TUSB_SPEED_FULL
    };
    /* dcd_init enables the D+ pull-up, so the device attaches here. */
    if (!tud_rhport_init(0, &init)) panic("TinyUSB initialization failed");
}

void usb_prepare_bootloader(void)
{
    detached = true;
    remote_allowed = false;
    (void)tud_disconnect();
}

void usb_task(void)
{
    bool was_mounted = tud_mounted();
    tud_task();
    /* A bus reset clears TinyUSB's whole device state, suspended included. */
    if (was_mounted && !tud_mounted() && !tud_suspended() && !detached)
        post(HB_ATTACHED, HB_CAUSE_RESET);
    if (event_overflow) {
        event_overflow = false;
        event_tail = event_head;
        hal_attach(false);
        hb_usb(&bridge, HB_DETACHED, HB_CAUSE_DETACH);
        return;
    }
    while (event_tail != event_head) {
        uint8_t state = events[event_tail][0];
        uint8_t cause = events[event_tail][1];
        event_tail = (event_tail + 1) & 7;
        if (cause == HB_CAUSE_RESET) {
            memset(idle_rate, 0, sizeof(idle_rate));
            remote_allowed = remote_signalled = false;
        }
        hb_usb(&bridge, state, cause);
        /* TinyUSB exposes the remote-wakeup feature only at suspend, which is
           the only time it matters; STATUS and EVENT_WAKEUP follow it then. */
        if (state == HB_SUSPENDED && bridge.wakeup_enabled != remote_allowed)
            hb_wakeup_enabled(&bridge, remote_allowed);
    }
    if (detached || bridge.usb != HB_CONFIGURED) return;
    for (uint8_t i = 0; i < 4; ++i) {
        if (releases & (1 << i)) {
            if (send_report(i, current[i], report_size(i))) releases &= ~(1 << i);
        } else if (!bridge.count && !bridge.waiting && hal_reports_idle() && idle_rate[i] &&
                   (uint32_t)(board_millis() - sent_at[i]) >= (uint16_t)idle_rate[i] * 4) {
            (void)send_report(i, current[i], report_size(i));
        }
    }
}
