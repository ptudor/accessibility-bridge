#include "board.h"
#include <string.h>

void board_serial(char *out, size_t size)
{
    static const char serial[] = "0123456789ABCDEF";
    if (!size) return;
    size_t count = sizeof(serial);
    if (count > size) count = size;
    memcpy(out, serial, count);
    out[size - 1] = 0;
}
