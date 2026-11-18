#ifndef HIDBRIDGE_PROTOCOL_H
#define HIDBRIDGE_PROTOCOL_H

#include <stdbool.h>
#include <stdint.h>

/* Macros, not enumerators: identity.h stringifies them and CMake reads them. */
#define HB_FW_MAJOR 0
#define HB_FW_MINOR 2

enum {
    HB_VERSION = 1,
    HB_WINDOW = 8, HB_QUEUE = 16, HB_BODY = 64, HB_WIRE = 70,
    HB_WATCHDOG_MS = 500, HB_WAKEUP_MS = 2000,
    HB_HELLO = 0x01, HB_STATUS = 0x02, HB_IDENTITY = 0x03,
    HB_KEY_DOWN = 0x10, HB_KEY_UP = 0x11, HB_KEY_REPORT = 0x12,
    HB_MOUSE = 0x20, HB_POINTER = 0x21, HB_CONSUMER = 0x22,
    HB_RELEASE_ALL = 0x30, HB_WAIT = 0x31, HB_SYNC = 0x32,
    HB_HEARTBEAT = 0x33, HB_USB_DETACH = 0x40, HB_USB_ATTACH = 0x41,
    HB_WAKEUP = 0x42, HB_BOOTLOADER = 0x43,
    HB_ACK = 0x80, HB_NAK = 0x81,
    HB_EVENT_USB = 0x90, HB_EVENT_PROTOCOL = 0x91, HB_EVENT_LEDS = 0x92,
    HB_EVENT_WAKEUP = 0x93, HB_EVENT_DONE = 0x94,
    HB_EVENT_RELEASED = 0x95, HB_EVENT_HELD = 0x96,
    HB_FRAMING = 1, HB_CRC = 2, HB_SEQUENCE = 3, HB_TYPE = 4,
    HB_LENGTH = 5, HB_QUEUE_FULL = 6, HB_STATE = 7, HB_NO_SESSION = 8,
    HB_DETACHED = 0, HB_ATTACHED = 1, HB_CONFIGURED = 2, HB_SUSPENDED = 3,
    HB_CAUSE_POWER = 0, HB_CAUSE_RESET = 1, HB_CAUSE_CONFIGURED = 2,
    HB_CAUSE_SUSPEND = 3, HB_CAUSE_RESUME = 4, HB_CAUSE_DETACH = 5,
    HB_CAUSE_ATTACH = 6, HB_CAUSE_WAKEUP = 7,
    HB_RELEASE_WATCHDOG = 1, HB_RELEASE_RESET = 2, HB_RELEASE_SUSPEND = 3,
    HB_RELEASE_DETACH = 4, HB_RELEASE_COMMAND = 5,
    HB_REPORT_KEYBOARD = 0, HB_REPORT_MOUSE = 1,
    HB_REPORT_POINTER = 2, HB_REPORT_CONSUMER = 3
};

typedef struct {
    uint8_t seq, type, len, body[HB_BODY];
} hb_frame;

typedef struct {
    uint8_t data[HB_WIRE - 1], len;
    bool overflow;
} hb_decoder;

uint16_t hb_crc16(const uint8_t *data, uint8_t len);
uint8_t hb_encode(const hb_frame *frame, uint8_t wire[HB_WIRE]);
/* Returns 0 for more input/idle, 1 for a frame, or -NAK code. */
int8_t hb_decode_byte(hb_decoder *decoder, uint8_t byte, hb_frame *frame);

#endif
