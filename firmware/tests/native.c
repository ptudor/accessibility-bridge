/* Line transport for differential tests; compiles the actual firmware core. */
#include "bridge.h"
#include "hal.h"
#include "identity.h"
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static bool ready = true;
static void hex(const uint8_t *data, uint8_t len)
{
    for (uint8_t i = 0; i < len; ++i) printf("%02x", data[i]);
    putchar('\n');
}
void hal_write(const uint8_t *wire, uint8_t len) { printf("W "); hex(wire, len); }
uint8_t hal_identity(uint8_t body[HB_BODY])
{
    memcpy(body, HB_IDENTITY_TEXT, sizeof(HB_IDENTITY_TEXT) - 1);
    return sizeof(HB_IDENTITY_TEXT) - 1;
}
bool hal_report(uint8_t kind, const uint8_t *data, uint8_t len)
{
    if (!ready) return false;
    printf("R %u ", kind); hex(data, len); return true;
}
bool hal_reports_idle(void) { return ready; }
/* The contract's zero reports, as the Go simulator prints them. The board's
   adapter (usb.c) differs only in keeping the pointer's last position. */
void hal_release(uint8_t mask)
{
    const uint8_t sizes[] = {8,4,7,3};
    for (uint8_t i = 0; i < 4; ++i) if (mask & (1 << i)) {
        uint8_t zero[8] = {0};
        if (i >= 2) zero[0] = i - 1;
        printf("R %u ", i); hex(zero, sizes[i]);
    }
}
void hal_attach(bool attach) { (void)attach; }
bool hal_wakeup(void) { return true; }
void hal_bootloader(void) {}

int main(int argc, char **argv)
{
    if (argc == 2 && !strcmp(argv[1], "--identity")) {
        puts(HB_IDENTITY_TEXT);
        return 0;
    }
    if (argc != 1) return 2;
    hb_bridge b;
    hb_init(&b);
    b.usb = HB_CONFIGURED;
    char line[1024], cmd[16], data[512];
    uint32_t now = 0;
    while (fgets(line, sizeof(line), stdin)) {
        unsigned a = 0, c = 0;
        if (sscanf(line, "%15s", cmd) != 1) return 2;
        if (!strcmp(cmd, "wire")) {
            if (sscanf(line, "%*s %511s", data) != 1 || strlen(data) % 2) return 2;
            for (size_t i = 0; data[i]; i += 2) {
                char pair[] = {data[i], data[i + 1], 0};
                hb_receive(&b, strtoul(pair, NULL, 16), now);
            }
        } else if (!strcmp(cmd, "tick")) {
            if (sscanf(line, "%*s %u", &a) != 1) return 2;
            now = a;
            hb_tick(&b, now);
        } else {
            if (sscanf(line, "%*s %u %u", &a, &c) < 1) return 2;
            if (!strcmp(cmd, "usb")) hb_usb(&b, a, c);
            else if (!strcmp(cmd, "leds")) hb_leds(&b, a);
            else if (!strcmp(cmd, "wakeup")) hb_wakeup_enabled(&b, a);
            else if (!strcmp(cmd, "protocol")) hb_protocol(&b, a, c);
            else if (!strcmp(cmd, "ready")) ready = a;
            else return 2;
        }
        puts("OK");
        fflush(stdout);
    }
    return ferror(stdin) ? 1 : 0;
}
