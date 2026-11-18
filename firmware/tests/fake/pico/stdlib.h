/* The two Pico SDK calls usb.c makes, for the native adapter test. */
#ifndef HIDBRIDGE_FAKE_PICO_STDLIB_H
#define HIDBRIDGE_FAKE_PICO_STDLIB_H

#include <stdint.h>

void sleep_ms(uint32_t ms);
void panic(const char *fmt, ...) __attribute__((noreturn));

#endif
