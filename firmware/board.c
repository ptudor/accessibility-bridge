#include "board.h"
#include "board_pins.h"
#include "hal.h"
#include "identity.h"
#include "hardware/gpio.h"
#include "hardware/irq.h"
#include "hardware/regs/uart.h"
#include "hardware/sync.h"
#include "hardware/uart.h"
#include "hardware/watchdog.h"
#include "pico/bootrom.h"
#include "pico/stdlib.h"
#include "pico/unique_id.h"
#include <string.h>

#define HB_UART uart0
#define HB_UART_IRQ UART0_IRQ
#define HB_BAUD 1000000u
/* Deassert CTS# with half the ring still free: a whole 70-byte wire frame plus
   the few characters the USB UART finishes after CTS# rises fit several times. */
#define HB_RX_STOP 128u
#define HB_RX_RESUME 96u
/* Guards a stalled main loop; every deliberate wait in this file is shorter. */
#define HB_HW_WATCHDOG_MS 250u

static volatile uint8_t rx[256], rx_head, rx_tail;
static volatile bool rx_fault;

static void uart_rx_irq(void)
{
    while (uart_is_readable(HB_UART)) {
        uint32_t value = uart_get_hw(HB_UART)->dr;
        if (value & (UART_UARTDR_OE_BITS | UART_UARTDR_BE_BITS |
                     UART_UARTDR_PE_BITS | UART_UARTDR_FE_BITS))
            rx_fault = true;
        uint8_t head = rx_head;
        uint8_t next = head + 1;
        if (next == rx_tail) {
            rx_fault = true;
            gpio_put(HB_PIN_RTS_N, true);
            continue;
        }
        rx[head] = value;
        rx_head = next;
        if ((uint8_t)(next - rx_tail) >= HB_RX_STOP)
            gpio_put(HB_PIN_RTS_N, true);
    }
}

void board_init(void)
{
    /* GPIO3 is UART0's RTS pin on the schematic, but it is driven from the
       software ring: the hardware RTS watches the 32-byte FIFO, which the
       interrupt below empties, so it would never deassert. Deasserted (high)
       until the UART is ready, matching the isolator's default-high output. */
    gpio_init(HB_PIN_RTS_N);
    gpio_put(HB_PIN_RTS_N, true);
    gpio_set_dir(HB_PIN_RTS_N, GPIO_OUT);
    gpio_init(HB_PIN_LED_LINK);
    gpio_put(HB_PIN_LED_LINK, false);
    gpio_set_dir(HB_PIN_LED_LINK, GPIO_OUT);
    gpio_init(HB_PIN_LED_HELD);
    gpio_put(HB_PIN_LED_HELD, false);
    gpio_set_dir(HB_PIN_LED_HELD, GPIO_OUT);

    /* The receive pin goes to the UART before the UART is enabled. A UART
       input with no pin routed to it reads low: the peripheral sees the OR of
       the pins selected for it (RP2040 datasheet 2.19.2). A receiver enabled
       on a low line clocks in a break, and the settings below keep it
       enabled for some 30 us before the pins follow. Routing RX first prevents a startup break from corrupting the first frame. */
    gpio_set_function(HB_PIN_UART_RX, GPIO_FUNC_UART);
    /* 125 MHz clk_peri divides to exactly 1,000,000 (7 + 52/64). */
    uint actual = uart_init(HB_UART, HB_BAUD);
    if (actual != HB_BAUD) panic("UART cannot generate 1,000,000 baud");
    uart_set_format(HB_UART, 8, 1, UART_PARITY_NONE);
    uart_set_hw_flow(HB_UART, false, false);
    uart_set_fifo_enabled(HB_UART, true);
    gpio_set_function(HB_PIN_UART_TX, GPIO_FUNC_UART);
    irq_set_exclusive_handler(HB_UART_IRQ, uart_rx_irq);
    irq_set_enabled(HB_UART_IRQ, true);
    uart_set_irqs_enabled(HB_UART, true, false);
    gpio_put(HB_PIN_RTS_N, false);
}

uint32_t board_millis(void)
{
    return to_ms_since_boot(get_absolute_time());
}

int16_t board_read(void)
{
    int16_t result = -1;
    uint32_t saved = save_and_disable_interrupts();
    if (rx_fault) {
        /* Never execute a damaged frame or join bytes across an overrun. */
        rx_tail = rx_head;
        rx_fault = false;
        bridge.decoder.len = 0;
        bridge.decoder.overflow = true;
    }
    if (rx_tail != rx_head) result = rx[rx_tail++];
    if ((uint8_t)(rx_head - rx_tail) < HB_RX_RESUME)
        gpio_put(HB_PIN_RTS_N, false);
    restore_interrupts(saved);
    return result;
}

void hal_write(const uint8_t *wire, uint8_t len)
{
    uart_write_blocking(HB_UART, wire, len);
}

void board_serial(char *out, size_t size)
{
    if (!size) return;
    /* On RP2040 this is the external flash's 64-bit unique ID, read once at boot. */
    pico_get_unique_board_id_string(out, size);
    out[size - 1] = 0;
}

uint8_t hal_identity(uint8_t body[HB_BODY])
{
    static const char prefix[] = HB_IDENTITY_PREFIX;
    char serial[PICO_UNIQUE_BOARD_ID_SIZE_BYTES * 2 + 1];
    board_serial(serial, sizeof(serial));
    size_t a = sizeof(prefix) - 1, b = strlen(serial);
    _Static_assert(sizeof(prefix) - 1 + PICO_UNIQUE_BOARD_ID_SIZE_BYTES * 2 <= HB_BODY,
                   "identity exceeds protocol limit");
    memcpy(body, prefix, a);
    memcpy(body + a, serial, b);
    return (uint8_t)(a + b);
}

void board_leds(void)
{
    bool configured = bridge.usb == HB_CONFIGURED;
    gpio_put(HB_PIN_LED_LINK, configured);
    gpio_put(HB_PIN_LED_HELD, configured && hb_any_held(&bridge));
}

void board_idle(void) { tight_loop_contents(); }
void board_watchdog_enable(void) { watchdog_enable(HB_HW_WATCHDOG_MS, true); }
void board_watchdog_update(void) { watchdog_update(); }

void hal_bootloader(void)
{
    gpio_put(HB_PIN_RTS_N, true);
    uart_tx_wait_blocking(HB_UART);
    usb_prepare_bootloader();
    gpio_put(HB_PIN_LED_LINK, false);
    gpio_put(HB_PIN_LED_HELD, false);
    /* Nothing feeds the hardware watchdog during the hold below, and the hold
       is as long as its timeout: disarm it first, or the board reboots into
       this application instead of the ROM loader. */
    watchdog_disable();
    /* Hold the detach so the target tears the old device down before the
       loader attaches as a new one; a 20 ms gap can sit inside hub debounce. */
    sleep_ms(250);
    /* The ROM's mass-storage loader blinks LINK (active high) on activity. */
    rom_reset_usb_boot_extra(HB_PIN_LED_LINK, 0, false);
}
