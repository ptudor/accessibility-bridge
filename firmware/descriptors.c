#include "descriptors.h"
#include "board.h"
#include "identity.h"
#include <stddef.h>
#include <string.h>

#define LO(x) ((x) & 255)
#define HI(x) (((x) >> 8) & 255)
#define BCD(x) ((((x) / 10) << 4) | ((x) % 10))
/* Longest string descriptor body; the 16-character flash serial is the longest. */
#define HB_STRING_MAX 32

static const uint8_t keyboard[] = {
    0x05,0x01, 0x09,0x06, 0xa1,0x01, 0x05,0x07,
    0x19,0xe0, 0x29,0xe7, 0x15,0x00, 0x25,0x01,
    0x75,0x01, 0x95,0x08, 0x81,0x02,
    0x75,0x08, 0x95,0x01, 0x81,0x01,
    0x05,0x08, 0x19,0x01, 0x29,0x05, 0x75,0x01, 0x95,0x05, 0x91,0x02,
    0x75,0x03, 0x95,0x01, 0x91,0x01,
    0x05,0x07, 0x19,0x00, 0x2a,0xff,0x00, 0x15,0x00, 0x26,0xff,0x00,
    0x75,0x08, 0x95,0x06, 0x81,0x00, 0xc0
};
static const uint8_t mouse[] = {
    0x05,0x01, 0x09,0x02, 0xa1,0x01, 0x09,0x01, 0xa1,0x00,
    0x05,0x09, 0x19,0x01, 0x29,0x03, 0x15,0x00, 0x25,0x01,
    0x75,0x01, 0x95,0x03, 0x81,0x02, 0x75,0x05, 0x95,0x01, 0x81,0x01,
    0x05,0x01, 0x09,0x30, 0x09,0x31, 0x09,0x38,
    0x15,0x81, 0x25,0x7f, 0x75,0x08, 0x95,0x03, 0x81,0x06, 0xc0,0xc0
};
static const uint8_t extra[] = {
    0x05,0x01, 0x09,0x02, 0xa1,0x01, 0x85,0x01, 0x09,0x01, 0xa1,0x00,
    0x05,0x09, 0x19,0x01, 0x29,0x03, 0x15,0x00, 0x25,0x01,
    0x75,0x01, 0x95,0x03, 0x81,0x02, 0x75,0x05, 0x95,0x01, 0x81,0x01,
    0x05,0x01, 0x09,0x30, 0x09,0x31, 0x15,0x00, 0x26,0xff,0x7f,
    0x75,0x10, 0x95,0x02, 0x81,0x02,
    0x09,0x38, 0x15,0x81, 0x25,0x7f, 0x75,0x08, 0x95,0x01, 0x81,0x06,
    0xc0,0xc0,
    0x05,0x0c, 0x09,0x01, 0xa1,0x01, 0x85,0x02,
    0x15,0x00, 0x26,LO(HB_CONSUMER_USAGE_MAX),HI(HB_CONSUMER_USAGE_MAX),
    0x19,0x00, 0x2a,LO(HB_CONSUMER_USAGE_MAX),HI(HB_CONSUMER_USAGE_MAX),
    0x75,0x10, 0x95,0x01, 0x81,0x00, 0xc0
};

_Static_assert(HB_FW_MAJOR < 100 && HB_FW_MINOR < 100, "bcdDevice holds two digits each");
static const uint8_t device[] = {
    18,1, 0x00,0x02, 0,0,0,HB_EP0_SIZE,
    LO(HB_USB_VID),HI(HB_USB_VID), LO(HB_USB_PID),HI(HB_USB_PID),
    BCD(HB_FW_MINOR),BCD(HB_FW_MAJOR), 1,2,3,1
};
#define IFACE(n,sub,proto,report) \
    9,4,n,0,1,3,sub,proto,0, \
    9,0x21,0x11,0x01,0,1,0x22,LO(sizeof(report)),HI(sizeof(report)), \
    7,5,0x81+n,3,8,0,1
static const uint8_t configuration[] = {
    9,2,84,0,3,1,0,0xa0,HB_USB_POWER_MA / 2,
    IFACE(0,1,1,keyboard), IFACE(1,1,2,mouse), IFACE(2,0,0,extra)
};
static const uint16_t language[] = {0x0304,0x0409};

_Static_assert(sizeof(configuration) == 84, "configuration descriptor size");
/* string_descriptor() cuts a longer string short without a word. */
_Static_assert(sizeof(HB_MANUFACTURER) - 1 <= HB_STRING_MAX &&
               sizeof(HB_PRODUCT) - 1 <= HB_STRING_MAX, "USB string exceeds its descriptor");

/* UTF-16LE string descriptor built from the one ASCII source of each string.
   USB serialises control transfers, so a single buffer serves every caller. */
static const uint8_t *string_descriptor(uint8_t index, uint16_t *size)
{
    static uint16_t out[1 + HB_STRING_MAX];
    char serial[HB_STRING_MAX + 1];
    const char *ascii;
    if (index == 0) { *size = sizeof(language); return (const uint8_t *)language; }
    if (index == 1) ascii = HB_MANUFACTURER;
    else if (index == 2) ascii = HB_PRODUCT;
    else if (index == 3) { board_serial(serial, sizeof(serial)); ascii = serial; }
    else { *size = 0; return NULL; }
    size_t count = strlen(ascii);
    if (count > HB_STRING_MAX) count = HB_STRING_MAX;
    out[0] = (uint16_t)(0x0300 | (2 * count + 2));
    for (size_t i = 0; i < count; ++i) out[i + 1] = (uint8_t)ascii[i];
    *size = (uint16_t)(2 * count + 2);
    return (const uint8_t *)out;
}

const uint8_t *hb_descriptor(uint16_t value, uint16_t index, uint16_t *size)
{
    uint8_t type = value >> 8, number = value;
    const uint8_t *p = NULL;
    *size = 0;
    if (type == 1 && number == 0) { p = device; *size = sizeof(device); }
    if (type == 2 && number == 0) { p = configuration; *size = sizeof(configuration); }
    if (type == 0x21 && index < 3 && number == 0) {
        p = configuration + 18 + index * 25; *size = 9;
    }
    if (type == 0x22 && index < 3 && number == 0) {
        if (index == 0) { p = keyboard; *size = sizeof(keyboard); }
        if (index == 1) { p = mouse; *size = sizeof(mouse); }
        if (index == 2) { p = extra; *size = sizeof(extra); }
    }
    if (type == 3) p = string_descriptor(number, size);
    return p;
}

uint8_t const *tud_descriptor_device_cb(void) { return device; }

uint8_t const *tud_descriptor_configuration_cb(uint8_t index)
{
    return index == 0 ? configuration : NULL;
}

uint8_t const *tud_hid_descriptor_report_cb(uint8_t instance)
{
    uint16_t size;
    return hb_descriptor(0x2200, instance, &size);
}

uint16_t const *tud_descriptor_string_cb(uint8_t index, uint16_t langid)
{
    uint16_t size;
    (void)langid;
    return (uint16_t const *)string_descriptor(index, &size);
}
