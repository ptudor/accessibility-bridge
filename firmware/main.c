#include "board.h"

hb_bridge bridge;

int main(void)
{
    hb_init(&bridge);
    board_init();
    usb_init();
    board_watchdog_enable();
    for (;;) {
        usb_task();
        /* Bound RX work so a saturated link cannot starve USB or safety timers. */
        for (uint8_t i = 0; i < HB_WIRE; ++i) {
            int16_t byte = board_read();
            if (byte < 0) break;
            hb_receive(&bridge, (uint8_t)byte, board_millis());
        }
        hb_tick(&bridge, board_millis());
        board_leds();
        board_watchdog_update();
        board_idle();
    }
}
