#ifndef HIDBRIDGE_DESCRIPTORS_H
#define HIDBRIDGE_DESCRIPTORS_H

#include <stdint.h>

/* bMaxPacketSize0 in the device descriptor; tusb_config.h sizes EP0 from it. */
#define HB_EP0_SIZE 64

/* The consumer-control report carries one 16-bit usage, and its descriptor
   declares usages 0 to this one. A target builds a record for every usage
   declared: with the whole 16-bit range macOS held 65,533 of them, 21 MB in
   its registry, and Linux cuts a range off at 12,288 with a warning. 0x3FF
   is what TinyUSB's and the HID Usage Tables' own examples use, and it is
   above every usage the consumer page defines for media, application launch
   and application control. */
#define HB_CONSUMER_USAGE_MAX 0x03ff

/* USB descriptor lookup by wValue/wIndex, shared by the native tests and the
   TinyUSB callbacks below. Strings are built on demand in one static buffer. */
const uint8_t *hb_descriptor(uint16_t value, uint16_t index, uint16_t *size);
uint8_t const *tud_descriptor_device_cb(void);
uint8_t const *tud_descriptor_configuration_cb(uint8_t index);
uint8_t const *tud_hid_descriptor_report_cb(uint8_t instance);
uint16_t const *tud_descriptor_string_cb(uint8_t index, uint16_t langid);

#endif
