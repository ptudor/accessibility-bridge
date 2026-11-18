#ifndef HIDBRIDGE_IDENTITY_H
#define HIDBRIDGE_IDENTITY_H

#include "protocol.h"

/* Shared private-test identity: https://pid.codes/1209/0001/.
   Non-unique. Use only for private tests. Replace before distributing devices. */
#define HB_USB_VID 0x1209
#define HB_USB_PID 0x0001
#define HB_USB_IDENTITY_ASSIGNED 0
#define HB_BOARD_REVISION "R1"
#define HB_MANUFACTURER "TUDOR"
#define HB_PRODUCT "Accessibility Bridge " HB_BOARD_REVISION
/* USB serial string before the flash unique ID has been read; never on the bus. */
#define HB_SERIAL_FALLBACK "AUTO"
#define HB_STR_(x) #x
#define HB_STR(x) HB_STR_(x)
#define HB_FW_TEXT HB_STR(HB_FW_MAJOR) "." HB_STR(HB_FW_MINOR)
#define HB_IDENTITY_PREFIX "accessibility-bridge " HB_BOARD_REVISION " fw " HB_FW_TEXT " sn "
#define HB_IDENTITY_TEXT HB_IDENTITY_PREFIX HB_SERIAL_FALLBACK
/* Provisional 100 mA ceiling; qualify the assembled board before release. */
#define HB_USB_POWER_MA 100

#if defined(HB_REQUIRE_ASSIGNED_IDENTITY) && \
    (!HB_USB_IDENTITY_ASSIGNED || HB_USB_VID == 0 || HB_USB_PID == 0 || \
     (HB_USB_VID == 0x1209 && HB_USB_PID <= 0x0010))
#error "Confirm the TARGET VID/PID allocation and set HB_USB_IDENTITY_ASSIGNED for distribution"
#endif

#endif
