package frame

import (
	"bytes"
	"errors"
	"testing"
)

func TestCRC16CheckValue(t *testing.T) {
	if got := CRC16([]byte("123456789")); got != 0x29B1 {
		t.Fatalf("CRC16 check value = %#04x, want 0x29b1", got)
	}
	if got := CRC16(nil); got != 0xFFFF {
		t.Fatalf("CRC16 of nothing = %#04x, want the initial value", got)
	}
}

func TestCOBS(t *testing.T) {
	tests := []struct{ raw, encoded []byte }{
		{[]byte{0x00}, []byte{0x01, 0x01}},
		{[]byte{0x00, 0x00}, []byte{0x01, 0x01, 0x01}},
		{[]byte{0x11, 0x22, 0x00, 0x33}, []byte{0x03, 0x11, 0x22, 0x02, 0x33}},
		{[]byte{0x11, 0x22, 0x33, 0x44}, []byte{0x05, 0x11, 0x22, 0x33, 0x44}},
		{[]byte{0x11, 0x00, 0x00, 0x00}, []byte{0x02, 0x11, 0x01, 0x01, 0x01}},
	}
	for _, tc := range tests {
		if got := cobsEncode(tc.raw); !bytes.Equal(got, tc.encoded) {
			t.Errorf("encode(% x) = % x, want % x", tc.raw, got, tc.encoded)
		}
		got, err := cobsDecode(tc.encoded)
		if err != nil || !bytes.Equal(got, tc.raw) {
			t.Errorf("decode(% x) = % x, %v; want % x", tc.encoded, got, err, tc.raw)
		}
	}
	// A 254-byte run of non-zero data needs the long-block code and a
	// trailing code byte.
	long := bytes.Repeat([]byte{0x42}, 254)
	enc := cobsEncode(long)
	if enc[0] != 0xFF || len(enc) != 256 || enc[255] != 0x01 {
		t.Fatalf("254-byte block encoded as %d bytes starting %#02x ending %#02x", len(enc), enc[0], enc[len(enc)-1])
	}
	if dec, err := cobsDecode(enc); err != nil || !bytes.Equal(dec, long) {
		t.Fatalf("long block round trip failed: %v", err)
	}
	for _, bad := range [][]byte{nil, {0x00}, {0x05, 0x11}, {0x02, 0x00}} {
		if _, err := cobsDecode(bad); !errors.Is(err, ErrFraming) {
			t.Errorf("decode(% x) err = %v, want framing", bad, err)
		}
	}
}

func TestEncodeDecode(t *testing.T) {
	f := Frame{Seq: 0x2A, Type: 0x21, Body: []byte{0x01, 0x00, 0x80, 0xFF, 0x7F, 0x00}}
	wire, err := Encode(f)
	if err != nil {
		t.Fatal(err)
	}
	if wire[len(wire)-1] != Delimiter || bytes.Count(wire, []byte{0}) != 1 {
		t.Fatalf("wire % x must end with the only delimiter", wire)
	}
	got, err := DecodeWire(wire[:len(wire)-1])
	if err != nil || got.Seq != f.Seq || got.Type != f.Type || !bytes.Equal(got.Body, f.Body) {
		t.Fatalf("round trip = %+v, %v", got, err)
	}
	if _, err = Encode(Frame{Body: make([]byte, MaxBody+1)}); !errors.Is(err, ErrBodyTooLong) {
		t.Fatalf("oversize body err = %v", err)
	}
	if wire, err = Encode(Frame{Body: make([]byte, MaxBody)}); err != nil || len(wire) != MaxWire {
		t.Fatalf("largest frame is %d bytes, %v; want %d", len(wire), err, MaxWire)
	}
	// Flip one payload bit: the CRC must catch it.
	payload := []byte{0x01, 0x02, 0x03}
	crc := CRC16(payload)
	payload = append(payload, byte(crc>>8), byte(crc))
	if _, err = Decode(payload); err != nil {
		t.Fatal(err)
	}
	payload[2] ^= 0x10
	if _, err = Decode(payload); !errors.Is(err, ErrCRC) {
		t.Fatalf("corrupted payload err = %v, want crc", err)
	}
	if _, err = Decode([]byte{1, 2, 3}); !errors.Is(err, ErrFraming) {
		t.Fatalf("short payload err = %v, want framing", err)
	}
}

func TestDecoderStream(t *testing.T) {
	a, _ := Encode(Frame{Seq: 1, Type: 0x33})
	b, _ := Encode(Frame{Seq: 2, Type: 0x10, Body: []byte{0x04}})
	stream := append([]byte{0x00, 0x00}, a...)
	stream = append(stream, 0x00)
	stream = append(stream, b...)
	stream = append(stream, bytes.Repeat([]byte{0x55}, MaxWire+5)...)
	stream = append(stream, 0x00)
	c, _ := Encode(Frame{Seq: 3, Type: 0x02})
	stream = append(stream, c...)
	var frames []Frame
	var errs []error
	var d Decoder
	// Deliver one byte at a time: the decoder keeps state between pushes.
	for i := range stream {
		d.Push(stream[i:i+1], func(f Frame, err error) {
			if err != nil {
				errs = append(errs, err)
				return
			}
			frames = append(frames, f)
		})
	}
	if len(frames) != 3 || frames[0].Seq != 1 || frames[1].Seq != 2 || frames[2].Seq != 3 || !bytes.Equal(frames[1].Body, []byte{0x04}) {
		t.Fatalf("frames = %+v", frames)
	}
	if len(errs) != 1 || !errors.Is(errs[0], ErrFraming) {
		t.Fatalf("errors = %v, want one over-long frame", errs)
	}
}

func FuzzDecodeWire(f *testing.F) {
	wire, _ := Encode(Frame{Seq: 7, Type: 0x21, Body: []byte{1, 2, 3, 4, 5, 6}})
	f.Add(wire[:len(wire)-1])
	f.Add([]byte{0x01})
	f.Fuzz(func(t *testing.T, data []byte) {
		fr, err := DecodeWire(data)
		if err != nil {
			return
		}
		again, err := Encode(fr)
		if err != nil {
			t.Fatalf("decoded frame %+v does not re-encode: %v", fr, err)
		}
		if !bytes.Equal(again[:len(again)-1], data) {
			t.Fatalf("re-encoding differs: % x vs % x", again, data)
		}
	})
}
