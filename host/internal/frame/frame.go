// Package frame implements the wire framing of docs/PROTOCOL.md: a payload
// of sequence number, type, body and CRC-16/CCITT-FALSE, COBS-encoded and
// delimited by 0x00. It is pure and shared by the host link and the
// simulator.
package frame

import (
	"errors"
	"fmt"
)

const (
	// MaxBody is the largest body a frame may carry.
	MaxBody = 64
	// MaxPayload is seq, type, MaxBody and the CRC.
	MaxPayload = MaxBody + 4
	// MaxWire is the longest encoded frame including its delimiter. COBS
	// adds one byte to payloads shorter than 254.
	MaxWire = MaxPayload + 2
	// Delimiter terminates every frame on the wire.
	Delimiter = 0
)

var (
	// ErrFraming is a frame that does not decode: bad COBS, too long, or
	// too short to hold the fixed fields.
	ErrFraming = errors.New("framing error")
	// ErrCRC is a frame whose CRC does not match its contents.
	ErrCRC = errors.New("crc mismatch")
	// ErrBodyTooLong refuses to encode a body over MaxBody.
	ErrBodyTooLong = errors.New("body exceeds 64 bytes")
)

// Frame is one decoded frame. Body aliases no caller memory after Decode.
type Frame struct {
	Seq  uint8
	Type uint8
	Body []byte
}

// CRC16 is CRC-16/CCITT-FALSE: polynomial 0x1021, initial value 0xFFFF, no
// reflection and no final XOR. The check value of "123456789" is 0x29B1.
func CRC16(data []byte) uint16 {
	crc := uint16(0xFFFF)
	for _, b := range data {
		crc ^= uint16(b) << 8
		for i := 0; i < 8; i++ {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// Encode returns the wire bytes of f, delimiter included.
func Encode(f Frame) ([]byte, error) {
	if len(f.Body) > MaxBody {
		return nil, fmt.Errorf("%w: %d", ErrBodyTooLong, len(f.Body))
	}
	payload := make([]byte, 0, len(f.Body)+4)
	payload = append(payload, f.Seq, f.Type)
	payload = append(payload, f.Body...)
	crc := CRC16(payload)
	payload = append(payload, byte(crc>>8), byte(crc))
	return append(cobsEncode(payload), Delimiter), nil
}

// Decode checks and splits one COBS-decoded payload.
func Decode(payload []byte) (Frame, error) {
	if len(payload) < 4 || len(payload) > MaxPayload {
		return Frame{}, fmt.Errorf("%w: payload of %d bytes", ErrFraming, len(payload))
	}
	n := len(payload) - 2
	want := uint16(payload[n])<<8 | uint16(payload[n+1])
	if CRC16(payload[:n]) != want {
		return Frame{}, ErrCRC
	}
	return Frame{Seq: payload[0], Type: payload[1], Body: append([]byte(nil), payload[2:n]...)}, nil
}

// DecodeWire decodes one complete frame without its delimiter.
func DecodeWire(wire []byte) (Frame, error) {
	payload, err := cobsDecode(wire)
	if err != nil {
		return Frame{}, err
	}
	return Decode(payload)
}

// Decoder splits a byte stream on delimiters. Empty frames are the idle
// and are skipped; an over-long frame is discarded up to its delimiter.
type Decoder struct {
	buf      []byte
	overflow bool
}

// Push feeds bytes and calls fn once per delimited frame, in order, with
// either the frame or the reason it was refused.
func (d *Decoder) Push(data []byte, fn func(Frame, error)) {
	for _, b := range data {
		if b != Delimiter {
			if len(d.buf) >= MaxWire-1 {
				d.overflow = true
				continue
			}
			d.buf = append(d.buf, b)
			continue
		}
		switch {
		case d.overflow:
			fn(Frame{}, fmt.Errorf("%w: frame longer than %d bytes", ErrFraming, MaxWire))
		case len(d.buf) == 0:
			// Idle or resynchronisation: nothing to report.
		default:
			fn(DecodeWire(d.buf))
		}
		d.buf = d.buf[:0]
		d.overflow = false
	}
}

func cobsEncode(src []byte) []byte {
	out := make([]byte, 1, len(src)+len(src)/254+2)
	codeIndex := 0
	code := byte(1)
	for _, b := range src {
		if b == 0 {
			out[codeIndex] = code
			codeIndex = len(out)
			out = append(out, 0)
			code = 1
			continue
		}
		out = append(out, b)
		code++
		if code == 0xFF {
			out[codeIndex] = code
			codeIndex = len(out)
			out = append(out, 0)
			code = 1
		}
	}
	out[codeIndex] = code
	return out
}

func cobsDecode(src []byte) ([]byte, error) {
	if len(src) == 0 {
		return nil, fmt.Errorf("%w: empty", ErrFraming)
	}
	out := make([]byte, 0, len(src))
	for i := 0; i < len(src); {
		code := int(src[i])
		if code == 0 {
			return nil, fmt.Errorf("%w: zero inside frame", ErrFraming)
		}
		i++
		end := i + code - 1
		if end > len(src) {
			return nil, fmt.Errorf("%w: truncated block", ErrFraming)
		}
		for _, b := range src[i:end] {
			if b == 0 {
				return nil, fmt.Errorf("%w: zero inside frame", ErrFraming)
			}
		}
		out = append(out, src[i:end]...)
		i = end
		if code < 0xFF && i < len(src) {
			out = append(out, 0)
		}
	}
	return out, nil
}
