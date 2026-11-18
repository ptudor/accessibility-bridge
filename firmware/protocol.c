#include "protocol.h"
#include <string.h>

uint16_t hb_crc16(const uint8_t *data, uint8_t len)
{
    uint16_t crc = 0xffff;
    while (len--) {
        crc ^= (uint16_t)*data++ << 8;
        for (uint8_t bit = 0; bit < 8; ++bit)
            crc = (crc & 0x8000) ? (crc << 1) ^ 0x1021 : crc << 1;
    }
    return crc;
}
uint8_t hb_encode(const hb_frame *f, uint8_t wire[HB_WIRE])
{
    if (f->len > HB_BODY)
        return 0;
    uint8_t raw[HB_BODY + 4];
    raw[0] = f->seq;
    raw[1] = f->type;
    memcpy(raw + 2, f->body, f->len);
    uint16_t crc = hb_crc16(raw, f->len + 2);
    raw[f->len + 2] = crc >> 8;
    raw[f->len + 3] = crc;
    uint8_t code = 1, index = 0, size = 1;
    for (uint8_t i = 0; i < f->len + 4; ++i) {
        if (raw[i]) {
            wire[size++] = raw[i];
            ++code;
        } else {
            wire[index] = code;
            index = size++;
            code = 1;
        }
    }
    wire[index] = code;
    wire[size++] = 0;
    return size;
}

int8_t hb_decode_byte(hb_decoder *d, uint8_t byte, hb_frame *f)
{
    if (byte) {
        if (d->len == sizeof(d->data))
            d->overflow = true;
        else
            d->data[d->len++] = byte;
        return 0;
    }
    uint8_t len = d->len;
    bool overflow = d->overflow;
    d->len = 0;
    d->overflow = false;
    if (overflow)
        return -HB_FRAMING;
    if (!len)
        return 0;
    /* Decode in place: the write cursor never overtakes the read cursor. */
    uint8_t in = 0, out = 0;
    while (in < len) {
        uint8_t code = d->data[in++];
        if (!code || code - 1 > len - in)
            return -HB_FRAMING;
        for (uint8_t i = 1; i < code; ++i)
            d->data[out++] = d->data[in++];
        if (code != 255 && in < len)
            d->data[out++] = 0;
    }
    if (out < 4 || out > HB_BODY + 4)
        return -HB_FRAMING;
    uint16_t crc = (uint16_t)d->data[out - 2] << 8 | d->data[out - 1];
    if (hb_crc16(d->data, out - 2) != crc)
        return -HB_CRC;
    f->seq = d->data[0];
    f->type = d->data[1];
    f->len = out - 4;
    memcpy(f->body, d->data + 2, f->len);
    return 1;
}
