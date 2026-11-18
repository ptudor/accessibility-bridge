#ifndef HIDBRIDGE_TUSB_CONFIG_H
#define HIDBRIDGE_TUSB_CONFIG_H

/* The Pico SDK's tinyusb_device target supplies both of these. */
#ifndef CFG_TUSB_MCU
#error CFG_TUSB_MCU must be supplied by the Pico SDK
#endif
#ifndef CFG_TUSB_OS
#error CFG_TUSB_OS must be supplied by the Pico SDK
#endif

#include "descriptors.h"

#define CFG_TUD_ENABLED 1
#define CFG_TUD_MAX_SPEED OPT_MODE_FULL_SPEED
/* RP2040-E5: on B0 and B1 silicon a device on a busy bus can fail to leave
   reset. The SDK's tinyusb_device target links the workaround and leaves it
   off; this turns it on. It reads the chip revision and does nothing on B2.
   On B0/B1 it borrows GPIO15 during each bus reset, which the carrier leaves
   unconnected for it (board_pins.h). */
#define TUD_OPT_RP2040_USB_DEVICE_ENUMERATION_FIX 1
/* Sized from the device descriptor's bMaxPacketSize0 so the two cannot drift. */
#define CFG_TUD_ENDPOINT0_SIZE HB_EP0_SIZE

#define CFG_TUD_HID 3
/* Largest report including its ID: the 8-byte boot keyboard; pointer is 1 + 6. */
#define CFG_TUD_HID_EP_BUFSIZE 8
#define CFG_TUD_CDC 0
#define CFG_TUD_MSC 0
#define CFG_TUD_MIDI 0
#define CFG_TUD_VENDOR 0

#endif
