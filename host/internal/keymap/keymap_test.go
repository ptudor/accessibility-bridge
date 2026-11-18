package keymap

import (
	"testing"

	"github.com/ptudor/accessibility-bridge/host/internal/proto"
)

func TestLookup(t *testing.T) {
	tests := []struct {
		name string
		want uint8
	}{
		{"a", 0x04}, {"Z", 0x1D}, {"1", 0x1E}, {"0", 0x27}, {"enter", 0x28}, {"Return", 0x28},
		{"esc", 0x29}, {"f1", 0x3A}, {"f12", 0x45}, {"f13", 0x68}, {"f24", 0x73},
		{"ctrl", 0xE0}, {"shift", 0xE1}, {"alt", 0xE2}, {"cmd", 0xE3}, {"win", 0xE3}, {"altgr", 0xE6}, {"rgui", 0xE7},
		{"kp_enter", 0x58}, {"numpad5", 0x5D}, {"kp-dot", 0x63}, {"delete", 0x4C}, {"capslock", 0x39},
		{"application", 0x65}, {"nonus_backslash", 0x64},
	}
	for _, tc := range tests {
		got, ok := Lookup(tc.name)
		if !ok || got != tc.want {
			t.Errorf("Lookup(%q) = %#02x, %v; want %#02x", tc.name, got, ok, tc.want)
		}
	}
	if _, ok := Lookup("hyperspace"); ok {
		t.Error("Lookup accepted an unknown key")
	}
	if Name(0x28) != "enter" || Name(0xE0) != "lctrl" || Name(0x01) != "0x01" {
		t.Error("Name is wrong")
	}
	if n := len(Names()); n != 26+10+24+60 {
		t.Errorf("Names() has %d entries", n)
	}
}

func TestChord(t *testing.T) {
	got, err := ParseChord("ctrl+alt+delete")
	if err != nil || len(got) != 3 || got[0] != 0xE0 || got[1] != 0xE2 || got[2] != 0x4C {
		t.Fatalf("ParseChord = % x, %v", got, err)
	}
	for _, bad := range []string{"", "ctrl+", "+a", "ctrl+control", "ctrl+plus+a"} {
		if _, err := ParseChord(bad); err == nil {
			t.Errorf("ParseChord(%q) accepted", bad)
		}
	}
	if !IsModifier(0xE7) || IsModifier(0x04) || ModifierBit(0xE1) != 0x02 || ModifierBit(0xE7) != 0x80 {
		t.Error("modifier helpers are wrong")
	}
}

func TestChar(t *testing.T) {
	tests := []struct {
		r     rune
		usage uint8
		shift bool
	}{
		{'a', 0x04, false}, {'A', 0x04, true}, {'z', 0x1D, false}, {'1', 0x1E, false}, {'!', 0x1E, true},
		{'0', 0x27, false}, {')', 0x27, true}, {' ', 0x2C, false}, {'-', 0x2D, false}, {'_', 0x2D, true},
		{'=', 0x2E, false}, {'+', 0x2E, true}, {'[', 0x2F, false}, {'{', 0x2F, true}, {'\\', 0x31, false},
		{'|', 0x31, true}, {';', 0x33, false}, {':', 0x33, true}, {'\'', 0x34, false}, {'"', 0x34, true},
		{'`', 0x35, false}, {'~', 0x35, true}, {',', 0x36, false}, {'<', 0x36, true}, {'.', 0x37, false},
		{'>', 0x37, true}, {'/', 0x38, false}, {'?', 0x38, true}, {'\n', 0x28, false}, {'\t', 0x2B, false},
	}
	for _, tc := range tests {
		got, ok := Char(tc.r)
		if !ok || got.Usage != tc.usage || got.Shift != tc.shift {
			t.Errorf("Char(%q) = %+v, %v; want %#02x shift=%v", tc.r, got, ok, tc.usage, tc.shift)
		}
	}
	for _, r := range []rune{'é', '€', 0, 0x7F} {
		if _, ok := Char(r); ok {
			t.Errorf("Char(%q) claims to be typeable", r)
		}
	}
}

func TestConsumer(t *testing.T) {
	tests := []struct {
		name string
		want uint16
	}{
		{"volume_up", 0xE9}, {"Volume-Down", 0xEA}, {"mute", 0xE2}, {"play_pause", 0xCD}, {"next", 0xB5},
		{"prev", 0xB6}, {"brightness_up", 0x6F}, {"sleep", 0x32}, {"power", 0x30}, {"browser_home", 0x223},
		{"back", 0x224}, {"calc", 0x192}, {"email", 0x18A}, {"search", 0x221}, {"eject", 0xB8},
	}
	for _, tc := range tests {
		got, ok := Consumer(tc.name)
		if !ok || got != tc.want {
			t.Errorf("Consumer(%q) = %#03x, %v; want %#03x", tc.name, got, ok, tc.want)
		}
	}
	if _, ok := Consumer("teleport"); ok {
		t.Error("Consumer accepted an unknown control")
	}
	if ConsumerName(0xE9) != "volume_up" || ConsumerName(0x001) != "0x001" {
		t.Error("ConsumerName is wrong")
	}
	// Every named control lies inside the range the board's report
	// descriptor declares; the target ignores a usage outside it.
	for name, usage := range consumer {
		if usage == 0 || usage > proto.ConsumerMax {
			t.Errorf("%s is usage %#x, outside 1..%#x", name, usage, proto.ConsumerMax)
		}
	}
	if len(ConsumerNames()) != 25 {
		t.Errorf("ConsumerNames() has %d entries", len(ConsumerNames()))
	}
}
