#ifndef HIDBRIDGE_BOARD_H
#define HIDBRIDGE_BOARD_H

#include "bridge.h"
#include <stddef.h>

extern hb_bridge bridge;
void board_init(void);
uint32_t board_millis(void);
int16_t board_read(void);
void board_leds(void);
void board_idle(void);
void board_watchdog_enable(void);
void board_watchdog_update(void);
void board_serial(char *out, size_t size);
void usb_init(void);
void usb_task(void);
void usb_prepare_bootloader(void);

#endif
