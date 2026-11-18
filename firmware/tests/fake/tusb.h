/* The slice of TinyUSB's device API that usb.c uses, for the native adapter
   test (tests/usb.c). Names, values and signatures follow TinyUSB 0.18 as
   shipped in Pico SDK 2.3.1; the test implements the functions. */
#ifndef HIDBRIDGE_FAKE_TUSB_H
#define HIDBRIDGE_FAKE_TUSB_H

#include <stdbool.h>
#include <stdint.h>

typedef enum {
    HID_REPORT_TYPE_INVALID, HID_REPORT_TYPE_INPUT,
    HID_REPORT_TYPE_OUTPUT, HID_REPORT_TYPE_FEATURE
} hid_report_type_t;
enum { HID_PROTOCOL_BOOT, HID_PROTOCOL_REPORT };
typedef enum { TUSB_ROLE_INVALID, TUSB_ROLE_DEVICE, TUSB_ROLE_HOST } tusb_role_t;
typedef enum { TUSB_SPEED_FULL, TUSB_SPEED_LOW, TUSB_SPEED_HIGH } tusb_speed_t;
typedef struct {
    tusb_role_t role;
    tusb_speed_t speed;
} tusb_rhport_init_t;

bool tud_rhport_init(uint8_t rhport, const tusb_rhport_init_t *init);
void tud_task(void);
bool tud_mounted(void);
bool tud_suspended(void);
bool tud_connect(void);
bool tud_disconnect(void);
bool tud_remote_wakeup(void);
bool tud_hid_n_ready(uint8_t instance);
bool tud_hid_n_report(uint8_t instance, uint8_t report_id, void const *report, uint16_t len);
uint8_t tud_hid_n_get_protocol(uint8_t instance);

/* Callbacks usb.c provides. */
void tud_mount_cb(void);
void tud_suspend_cb(bool remote_wakeup_en);
void tud_resume_cb(void);
uint16_t tud_hid_get_report_cb(uint8_t instance, uint8_t report_id,
                               hid_report_type_t report_type,
                               uint8_t *buffer, uint16_t reqlen);
void tud_hid_set_report_cb(uint8_t instance, uint8_t report_id,
                           hid_report_type_t report_type,
                           uint8_t const *buffer, uint16_t bufsize);
void tud_hid_set_protocol_cb(uint8_t instance, uint8_t protocol);
bool tud_hid_set_idle_cb(uint8_t instance, uint8_t idle);

#endif
