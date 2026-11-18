#ifndef HIDBRIDGE_BOARD_PINS_H
#define HIDBRIDGE_BOARD_PINS_H

/* Reference carrier R1, original Pico H. GP25 drives the onboard LINK LED.
   GP2 drives the carrier's HELD LED. Leave GP15 free for the USB reset fix. */
#define HB_PIN_UART_TX 0
#define HB_PIN_UART_RX 1
#define HB_PIN_RTS_N 3
#define HB_PIN_LED_HELD 2
#define HB_PIN_LED_LINK 25

#endif
