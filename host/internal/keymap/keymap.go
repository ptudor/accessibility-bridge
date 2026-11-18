// Package keymap names HID usages for the tool and its scripts: keyboard
// page 0x07 keys and modifiers, consumer page 0x0C controls, and the US
// layout that turns text into key presses.
//
// Values are from USB-IF HID Usage Tables 1.6, §10 (keyboard/keypad, p. 89)
// and §15 (consumer, p. 125), archived as
// docs/datasheets/USB-IF_HID_Usage_Tables_1.6.pdf.
package keymap

import (
	"fmt"
	"sort"
	"strings"
)

// Keyboard page usages the tool refers to by name in code.
const (
	UsageA          uint8 = 0x04
	Usage1          uint8 = 0x1E
	Usage0          uint8 = 0x27
	UsageEnter      uint8 = 0x28
	UsageEscape     uint8 = 0x29
	UsageBackspace  uint8 = 0x2A
	UsageTab        uint8 = 0x2B
	UsageSpace      uint8 = 0x2C
	UsageCapsLock   uint8 = 0x39
	UsageScrollLock uint8 = 0x47
	UsageNumLock    uint8 = 0x53
	UsageLeftCtrl   uint8 = 0xE0
	UsageLeftShift  uint8 = 0xE1
	UsageLeftAlt    uint8 = 0xE2
	UsageLeftGUI    uint8 = 0xE3
	UsageRightCtrl  uint8 = 0xE4
	UsageRightShift uint8 = 0xE5
	UsageRightAlt   uint8 = 0xE6
	UsageRightGUI   uint8 = 0xE7
)

// Key is one key press with or without Shift, as needed to type a rune.
type Key struct {
	Usage uint8
	Shift bool
}

// IsModifier reports whether a usage is one of the eight modifier keys.
func IsModifier(usage uint8) bool { return usage >= UsageLeftCtrl && usage <= UsageRightGUI }

// ModifierBit is the bit a modifier usage occupies in the boot report's
// first byte (HID 1.11 Appendix B.1: Left Control is bit 0).
func ModifierBit(usage uint8) uint8 { return 1 << (usage - UsageLeftCtrl) }

// keys are the canonical names. HUT 1.6 §10.
var keys = map[string]uint8{
	"enter": 0x28, "escape": 0x29, "backspace": 0x2A, "tab": 0x2B, "space": 0x2C,
	"minus": 0x2D, "equal": 0x2E, "leftbracket": 0x2F, "rightbracket": 0x30, "backslash": 0x31,
	"nonus_hash": 0x32, "semicolon": 0x33, "quote": 0x34, "grave": 0x35, "comma": 0x36,
	"period": 0x37, "slash": 0x38, "capslock": 0x39,
	"printscreen": 0x46, "scrolllock": 0x47, "pause": 0x48, "insert": 0x49, "home": 0x4A,
	"pageup": 0x4B, "delete": 0x4C, "end": 0x4D, "pagedown": 0x4E,
	"right": 0x4F, "left": 0x50, "down": 0x51, "up": 0x52,
	"numlock": 0x53, "kp_slash": 0x54, "kp_asterisk": 0x55, "kp_minus": 0x56, "kp_plus": 0x57,
	"kp_enter": 0x58, "kp_1": 0x59, "kp_2": 0x5A, "kp_3": 0x5B, "kp_4": 0x5C, "kp_5": 0x5D,
	"kp_6": 0x5E, "kp_7": 0x5F, "kp_8": 0x60, "kp_9": 0x61, "kp_0": 0x62, "kp_period": 0x63,
	"nonus_backslash": 0x64, "application": 0x65, "power": 0x66, "kp_equal": 0x67,
	"lctrl": 0xE0, "lshift": 0xE1, "lalt": 0xE2, "lgui": 0xE3,
	"rctrl": 0xE4, "rshift": 0xE5, "ralt": 0xE6, "rgui": 0xE7,
}

// aliases map the names people type to canonical names.
var aliases = map[string]string{
	"return": "enter", "esc": "escape", "bs": "backspace", "del": "delete", "ins": "insert",
	"pgup": "pageup", "pgdn": "pagedown", "pgdown": "pagedown", "spacebar": "space",
	"ctrl": "lctrl", "control": "lctrl", "shift": "lshift", "alt": "lalt", "option": "lalt",
	"gui": "lgui", "win": "lgui", "windows": "lgui", "cmd": "lgui", "command": "lgui", "super": "lgui", "meta": "lgui",
	"altgr": "ralt", "roption": "ralt", "rcmd": "rgui", "rwin": "rgui", "rcommand": "rgui",
	"menu": "application", "app": "application", "prtsc": "printscreen", "sysrq": "printscreen",
	"caps": "capslock", "num": "numlock", "scroll": "scrolllock", "break": "pause",
	"dot": "period", "apostrophe": "quote", "backtick": "grave", "tilde": "grave",
	"lbracket": "leftbracket", "rbracket": "rightbracket", "equals": "equal", "dash": "minus", "hyphen": "minus",
	"leftarrow": "left", "rightarrow": "right", "uparrow": "up", "downarrow": "down",
	"arrow_left": "left", "arrow_right": "right", "arrow_up": "up", "arrow_down": "down",
	"kp_dot": "kp_period", "kp_decimal": "kp_period", "kp_star": "kp_asterisk", "kp_multiply": "kp_asterisk",
	"kp_divide": "kp_slash", "kp_subtract": "kp_minus", "kp_add": "kp_plus",
}

func init() {
	for i := 0; i < 26; i++ {
		keys[string(rune('a'+i))] = UsageA + uint8(i)
	}
	for i := 1; i <= 9; i++ {
		keys[fmt.Sprint(i)] = Usage1 + uint8(i-1)
	}
	keys["0"] = Usage0
	for i := 1; i <= 12; i++ {
		keys[fmt.Sprintf("f%d", i)] = 0x3A + uint8(i-1)
	}
	for i := 13; i <= 24; i++ {
		keys[fmt.Sprintf("f%d", i)] = 0x68 + uint8(i-13)
	}
	for i := 0; i <= 9; i++ {
		aliases[fmt.Sprintf("numpad%d", i)] = fmt.Sprintf("kp_%d", i)
		aliases[fmt.Sprintf("numpad_%d", i)] = fmt.Sprintf("kp_%d", i)
	}
}

func normalise(name string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(name)), "-", "_")
}

// Lookup resolves a key name or alias to its keyboard page usage.
func Lookup(name string) (uint8, bool) {
	n := normalise(name)
	if canonical, ok := aliases[n]; ok {
		n = canonical
	}
	u, ok := keys[n]
	return u, ok
}

// Name returns the canonical name of a keyboard usage, or its hex value.
func Name(usage uint8) string {
	for name, u := range keys {
		if u == usage {
			return name
		}
	}
	return fmt.Sprintf("0x%02x", usage)
}

// Names lists every canonical key name, sorted.
func Names() []string {
	out := make([]string, 0, len(keys))
	for name := range keys {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// ParseChord splits "ctrl+alt+delete" into usages in the order written.
// Each name must be distinct; the caller presses them in order and releases
// them in reverse.
func ParseChord(chord string) ([]uint8, error) {
	parts := strings.Split(chord, "+")
	var usages []uint8
	seen := map[uint8]bool{}
	for _, part := range parts {
		if part == "" {
			return nil, fmt.Errorf("chord %q has an empty key; name '+' as kp_plus or type it", chord)
		}
		u, ok := Lookup(part)
		if !ok {
			return nil, fmt.Errorf("unknown key %q in %q", part, chord)
		}
		if seen[u] {
			return nil, fmt.Errorf("key %q repeats in %q", part, chord)
		}
		seen[u] = true
		usages = append(usages, u)
	}
	return usages, nil
}

// shifted are the US-layout characters that need Shift, keyed by the
// unshifted key's usage.
var shifted = map[rune]uint8{
	'!': 0x1E, '@': 0x1F, '#': 0x20, '$': 0x21, '%': 0x22, '^': 0x23, '&': 0x24, '*': 0x25, '(': 0x26, ')': 0x27,
	'_': 0x2D, '+': 0x2E, '{': 0x2F, '}': 0x30, '|': 0x31, ':': 0x33, '"': 0x34, '~': 0x35, '<': 0x36, '>': 0x37, '?': 0x38,
}

var plain = map[rune]uint8{
	' ': 0x2C, '-': 0x2D, '=': 0x2E, '[': 0x2F, ']': 0x30, '\\': 0x31, ';': 0x33, '\'': 0x34, '`': 0x35,
	',': 0x36, '.': 0x37, '/': 0x38, '\n': 0x28, '\t': 0x2B, '\r': 0x28, '\b': 0x2A, 0x1B: 0x29,
}

// Char returns the key that types r on a US layout.
func Char(r rune) (Key, bool) {
	switch {
	case r >= 'a' && r <= 'z':
		return Key{UsageA + uint8(r-'a'), false}, true
	case r >= 'A' && r <= 'Z':
		return Key{UsageA + uint8(r-'A'), true}, true
	case r >= '1' && r <= '9':
		return Key{Usage1 + uint8(r-'1'), false}, true
	case r == '0':
		return Key{Usage0, false}, true
	}
	if u, ok := shifted[r]; ok {
		return Key{u, true}, true
	}
	if u, ok := plain[r]; ok {
		return Key{u, false}, true
	}
	return Key{}, false
}

// consumer are the consumer page 0x0C controls the tool offers. HUT 1.6 §15.
var consumer = map[string]uint16{
	"power": 0x30, "sleep": 0x32, "menu": 0x40,
	"brightness_up": 0x6F, "brightness_down": 0x70,
	"play": 0xB0, "pause": 0xB1, "record": 0xB2, "fast_forward": 0xB3, "rewind": 0xB4,
	"next": 0xB5, "previous": 0xB6, "stop": 0xB7, "eject": 0xB8, "play_pause": 0xCD,
	"mute": 0xE2, "volume_up": 0xE9, "volume_down": 0xEA,
	"email": 0x18A, "calculator": 0x192,
	"search": 0x221, "browser_home": 0x223, "back": 0x224, "forward": 0x225, "refresh": 0x227,
}

var consumerAliases = map[string]string{
	"prev": "previous", "ffwd": "fast_forward", "playpause": "play_pause",
	"vol_up": "volume_up", "vol_down": "volume_down", "volup": "volume_up", "voldown": "volume_down",
	"ac_home": "browser_home", "www_home": "browser_home", "ac_back": "back", "ac_forward": "forward",
	"ac_search": "search", "ac_refresh": "refresh", "calc": "calculator",
}

// Consumer resolves a consumer control name to its usage.
func Consumer(name string) (uint16, bool) {
	n := normalise(name)
	if canonical, ok := consumerAliases[n]; ok {
		n = canonical
	}
	u, ok := consumer[n]
	return u, ok
}

// ConsumerName returns the canonical name of a consumer usage, or its hex.
func ConsumerName(usage uint16) string {
	for name, u := range consumer {
		if u == usage {
			return name
		}
	}
	return fmt.Sprintf("0x%03x", usage)
}

// ConsumerNames lists every consumer control name, sorted.
func ConsumerNames() []string {
	out := make([]string, 0, len(consumer))
	for name := range consumer {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
