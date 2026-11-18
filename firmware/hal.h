#ifndef HIDBRIDGE_HAL_H
#define HIDBRIDGE_HAL_H

#include "protocol.h"

/* Called only from the cooperative main loop, never from an interrupt. */
void hal_write(const uint8_t *wire, uint8_t len);
uint8_t hal_identity(uint8_t body[HB_BODY]);
bool hal_report(uint8_t kind, const uint8_t *data, uint8_t len);
/* True only after all previously submitted reports reached the target. */
bool hal_reports_idle(void);
/* Retain zero reports until USB can send them; precede any subsequent input. */
void hal_release(uint8_t mask);
void hal_attach(bool attach);
bool hal_wakeup(void);
void hal_bootloader(void);

#endif
