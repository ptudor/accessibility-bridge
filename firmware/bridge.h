#ifndef HIDBRIDGE_BRIDGE_H
#define HIDBRIDGE_BRIDGE_H

#include "protocol.h"

typedef struct {
    uint8_t keyboard[8], mouse_buttons, pointer_buttons;
    uint16_t consumer;
} hb_held;

typedef struct {
    uint8_t seq, type, body[8];
} hb_queued;

typedef struct {
    hb_decoder decoder;
    hb_frame replies[HB_WINDOW];
    uint8_t reply_valid;
    hb_queued queue[HB_QUEUE];
    uint8_t head, count;
    hb_held held;
    uint8_t expected, events, usb, leds;
    bool session, wakeup_enabled, keyboard_boot, mouse_boot, bootloader;
    bool waiting, waking;
    uint32_t last_frame, wait_start, wake_start;
    uint32_t clock, uptime;
    uint16_t subsecond;
    uint16_t wait_ms, watchdogs, rx_errors, dropped;
} hb_bridge;

void hb_init(hb_bridge *b);
void hb_receive(hb_bridge *b, uint8_t byte, uint32_t now);
void hb_tick(hb_bridge *b, uint32_t now);
void hb_usb(hb_bridge *b, uint8_t state, uint8_t cause);
void hb_leds(hb_bridge *b, uint8_t leds);
void hb_wakeup_enabled(hb_bridge *b, bool enabled);
void hb_protocol(hb_bridge *b, uint8_t iface, uint8_t protocol);
bool hb_any_held(const hb_bridge *b);
bool hb_busy(const hb_bridge *b);

#endif
