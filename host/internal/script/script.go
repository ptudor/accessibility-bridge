// Package script is the small language hidbridgectl runs, one command per
// line, from a file, from stdin, or built from the command line. It turns
// each command into protocol frames and runs them through a link.Session,
// recording what happened in the session transcript.
//
//	# a comment
//	type "Hello, world!\n"      US-layout text; Go string escapes
//	key ctrl+alt+delete [HOLD]  press a chord, hold, release in reverse
//	down shift a                hold keys down
//	up a shift                  release keys
//	report 0200040000000000     raw boot keyboard report, 8 bytes hex
//	move DX DY                  relative mouse motion (pixels, any size)
//	pointer X Y                 absolute pointer, 0..32767 units
//	click X Y [BUTTON] [double] click at a pixel position
//	hover X Y                   move the absolute pointer to a pixel position, nothing pressed
//	drag X1 Y1 X2 Y2 [BUTTON]   press at one pixel position, move in steps, release at another
//	button left down|up|click   boot mouse buttons: left right middle
//	scroll N                    wheel notches, negative is down
//	consumer volume_up [HOLD]   consumer control press and release
//	wait 500ms                  board-side pause, keeps order with input
//	sleep 2s                    host-side pause
//	sync                        wait until the board has executed everything
//	release                     release everything now
//	capture LABEL               sync, then grab a frame of the display
//	expect leds caps=on num=off expect usb configured | expect held no
//	echo "note"                 a note in the transcript
//	detach | attach | wake      USB detach, attach, remote wakeup
//
// Durations take Go syntax (500ms, 1.5s); a bare number is milliseconds.
package script

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/ptudor/accessibility-bridge/host/internal/keymap"
	"github.com/ptudor/accessibility-bridge/host/internal/proto"
)

// Kind is a command verb.
type Kind int

const (
	KindType Kind = iota + 1
	KindKey
	KindDown
	KindUp
	KindReport
	KindMove
	KindPointer
	KindClick
	KindHover
	KindDrag
	KindButton
	KindScroll
	KindConsumer
	KindWait
	KindSleep
	KindSync
	KindRelease
	KindCapture
	KindExpect
	KindEcho
	KindDetach
	KindAttach
	KindWake
)

// Command is one parsed line.
type Command struct {
	Line int
	Text string
	Kind Kind

	Str      string            // type text, capture label, echo note
	Keys     []uint8           // key, down, up
	Report   [8]byte           // report
	X, Y     int               // move, pointer, click, hover, drag (the start)
	X2, Y2   int               // drag (the end)
	Button   uint8             // click, drag, button
	Action   string            // button: down, up, click
	Double   bool              // click
	N        int               // scroll
	Usage    uint16            // consumer
	Dur      time.Duration     // key hold, consumer hold, wait, sleep
	Expect   map[string]string // expect
	ExpectOf string            // expect: leds, usb, held, protocol
}

// ParseLine parses one line; a blank or comment line gives nil.
func ParseLine(line string, lineNo int) (*Command, error) {
	tokens, err := tokenize(line)
	if err != nil {
		return nil, fmt.Errorf("line %d: %w", lineNo, err)
	}
	if len(tokens) == 0 {
		return nil, nil
	}
	c := &Command{Line: lineNo, Text: strings.TrimSpace(line)}
	verb, args := strings.ToLower(tokens[0]), tokens[1:]
	if err = c.parse(verb, args); err != nil {
		return nil, fmt.Errorf("line %d: %s: %w", lineNo, verb, err)
	}
	return c, nil
}

// ParseAll parses a whole script, reporting every bad line.
func ParseAll(r io.Reader) ([]*Command, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	var cmds []*Command
	var problems []string
	for i, line := range strings.Split(string(data), "\n") {
		c, err := ParseLine(line, i+1)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		if c != nil {
			cmds = append(cmds, c)
		}
	}
	if len(problems) > 0 {
		return nil, errors.New(strings.Join(problems, "\n"))
	}
	return cmds, nil
}

func (c *Command) parse(verb string, args []string) error {
	need := func(min, max int) error {
		if len(args) < min || len(args) > max {
			if min == max {
				return fmt.Errorf("takes %d argument(s), got %d", min, len(args))
			}
			return fmt.Errorf("takes %d to %d arguments, got %d", min, max, len(args))
		}
		return nil
	}
	switch verb {
	case "type":
		c.Kind = KindType
		if err := need(1, 1); err != nil {
			return err
		}
		c.Str = args[0]
		for _, r := range c.Str {
			if _, ok := keymap.Char(r); !ok {
				return fmt.Errorf("%q cannot be typed on a US layout", r)
			}
		}
		if c.Str == "" {
			return errors.New("nothing to type")
		}
	case "key":
		c.Kind = KindKey
		if err := need(1, 2); err != nil {
			return err
		}
		keys, err := keymap.ParseChord(args[0])
		if err != nil {
			return err
		}
		c.Keys = keys
		if len(args) == 2 {
			if c.Dur, err = parseDuration(args[1]); err != nil {
				return err
			}
		}
	case "down", "up":
		c.Kind = KindDown
		if verb == "up" {
			c.Kind = KindUp
		}
		if len(args) == 0 {
			return errors.New("needs at least one key")
		}
		for _, a := range args {
			keys, err := keymap.ParseChord(a)
			if err != nil {
				return err
			}
			c.Keys = append(c.Keys, keys...)
		}
	case "report":
		c.Kind = KindReport
		if err := need(1, 1); err != nil {
			return err
		}
		b, err := hex.DecodeString(args[0])
		if err != nil || len(b) != 8 {
			return fmt.Errorf("%q is not 8 bytes of hex", args[0])
		}
		copy(c.Report[:], b)
	case "move", "pointer", "click":
		xy, rest, err := parseXY(args)
		if err != nil {
			return err
		}
		c.X, c.Y = xy[0], xy[1]
		switch verb {
		case "move":
			c.Kind = KindMove
			if len(rest) != 0 {
				return fmt.Errorf("unexpected %q", rest[0])
			}
		case "pointer":
			c.Kind = KindPointer
			if len(rest) != 0 {
				return fmt.Errorf("unexpected %q", rest[0])
			}
			if c.X < 0 || c.Y < 0 || c.X > proto.PointerMax || c.Y > proto.PointerMax {
				return fmt.Errorf("pointer units are 0..%d", proto.PointerMax)
			}
		case "click":
			c.Kind = KindClick
			c.Button = proto.ButtonLeft
			for _, a := range rest {
				switch strings.ToLower(a) {
				case "double":
					c.Double = true
				default:
					b, ok := parseButton(a)
					if !ok {
						return fmt.Errorf("unexpected %q", a)
					}
					c.Button = b
				}
			}
			if c.X < 0 || c.Y < 0 {
				return errors.New("click position is in pixels from the top left")
			}
		}
	case "hover":
		c.Kind = KindHover
		xy, rest, err := parseXY(args)
		if err != nil {
			return err
		}
		if len(rest) != 0 {
			return fmt.Errorf("unexpected %q", rest[0])
		}
		c.X, c.Y = xy[0], xy[1]
		if c.X < 0 || c.Y < 0 {
			return errors.New("hover position is in pixels from the top left")
		}
	case "drag":
		c.Kind = KindDrag
		from, rest, err := parseXY(args)
		if err != nil {
			return err
		}
		to, rest, err := parseXY(rest)
		if err != nil {
			return fmt.Errorf("needs X1 Y1 X2 Y2: %w", err)
		}
		c.X, c.Y, c.X2, c.Y2 = from[0], from[1], to[0], to[1]
		c.Button = proto.ButtonLeft
		for _, a := range rest {
			b, ok := parseButton(a)
			if !ok {
				return fmt.Errorf("unexpected %q", a)
			}
			c.Button = b
		}
		if c.X < 0 || c.Y < 0 || c.X2 < 0 || c.Y2 < 0 {
			return errors.New("drag positions are in pixels from the top left")
		}
		if c.X == c.X2 && c.Y == c.Y2 {
			return errors.New("drag start and end are the same point")
		}
	case "button":
		c.Kind = KindButton
		if err := need(2, 2); err != nil {
			return err
		}
		b, ok := parseButton(args[0])
		if !ok {
			return fmt.Errorf("unknown button %q", args[0])
		}
		c.Button = b
		c.Action = strings.ToLower(args[1])
		if c.Action != "down" && c.Action != "up" && c.Action != "click" {
			return fmt.Errorf("action is down, up or click, not %q", args[1])
		}
	case "scroll":
		c.Kind = KindScroll
		if err := need(1, 1); err != nil {
			return err
		}
		n, err := strconv.Atoi(args[0])
		if err != nil || n == 0 || n > 10000 || n < -10000 {
			return fmt.Errorf("%q is not a number of notches", args[0])
		}
		c.N = n
	case "consumer":
		c.Kind = KindConsumer
		if err := need(1, 2); err != nil {
			return err
		}
		u, ok := keymap.Consumer(args[0])
		if !ok {
			return fmt.Errorf("unknown consumer control %q", args[0])
		}
		c.Usage = u
		if len(args) == 2 {
			var err error
			if c.Dur, err = parseDuration(args[1]); err != nil {
				return err
			}
		}
	case "wait", "sleep":
		c.Kind = KindWait
		if verb == "sleep" {
			c.Kind = KindSleep
		}
		if err := need(1, 1); err != nil {
			return err
		}
		d, err := parseDuration(args[0])
		if err != nil {
			return err
		}
		if d > time.Hour {
			return errors.New("longer than an hour")
		}
		c.Dur = d
	case "sync", "release", "detach", "attach", "wake":
		c.Kind = map[string]Kind{"sync": KindSync, "release": KindRelease, "detach": KindDetach, "attach": KindAttach, "wake": KindWake}[verb]
		return need(0, 0)
	case "capture":
		c.Kind = KindCapture
		if err := need(0, 1); err != nil {
			return err
		}
		if len(args) == 1 {
			c.Str = args[0]
		}
	case "echo":
		c.Kind = KindEcho
		c.Str = strings.Join(args, " ")
	case "expect":
		c.Kind = KindExpect
		if len(args) < 2 {
			return errors.New("expect leds caps=on | usb STATE | held yes|no | protocol keyboard|mouse boot|report")
		}
		c.ExpectOf = strings.ToLower(args[0])
		c.Expect = map[string]string{}
		switch c.ExpectOf {
		case "leds":
			for _, a := range args[1:] {
				name, value, ok := strings.Cut(strings.ToLower(a), "=")
				if !ok || ledBit(name) == 0 || (value != "on" && value != "off") {
					return fmt.Errorf("%q is not LED=on|off with LED num, caps, scroll, compose or kana", a)
				}
				c.Expect[name] = value
			}
		case "usb":
			if len(args) != 2 || usbState(args[1]) < 0 {
				return errors.New("expect usb detached|attached|configured|suspended")
			}
			c.Expect["state"] = strings.ToLower(args[1])
		case "held":
			if len(args) != 2 || (args[1] != "yes" && args[1] != "no") {
				return errors.New("expect held yes|no")
			}
			c.Expect["held"] = args[1]
		case "protocol":
			if len(args) != 3 || (args[1] != "keyboard" && args[1] != "mouse") || (args[2] != "boot" && args[2] != "report") {
				return errors.New("expect protocol keyboard|mouse boot|report")
			}
			c.Expect[args[1]] = args[2]
		default:
			return fmt.Errorf("cannot expect %q", args[0])
		}
	default:
		return errors.New("unknown command")
	}
	return nil
}

func parseButton(s string) (uint8, bool) {
	switch strings.ToLower(s) {
	case "left", "l", "1":
		return proto.ButtonLeft, true
	case "right", "r", "2":
		return proto.ButtonRight, true
	case "middle", "m", "3":
		return proto.ButtonMiddle, true
	}
	return 0, false
}

func ledBit(name string) uint8 {
	switch name {
	case "num", "numlock":
		return proto.LEDNumLock
	case "caps", "capslock":
		return proto.LEDCapsLock
	case "scroll", "scrolllock":
		return proto.LEDScrollLock
	case "compose":
		return proto.LEDCompose
	case "kana":
		return proto.LEDKana
	}
	return 0
}

func usbState(name string) int {
	switch strings.ToLower(name) {
	case "detached":
		return int(proto.USBDetached)
	case "attached":
		return int(proto.USBAttached)
	case "configured":
		return int(proto.USBConfigured)
	case "suspended":
		return int(proto.USBSuspended)
	}
	return -1
}

// parseXY reads "X Y" or "X,Y" and returns the remaining arguments.
func parseXY(args []string) ([2]int, []string, error) {
	var xy [2]int
	if len(args) == 0 {
		return xy, nil, errors.New("needs X Y")
	}
	parts := args
	if strings.Contains(args[0], ",") {
		a, b, _ := strings.Cut(args[0], ",")
		parts = append([]string{a, b}, args[1:]...)
	}
	if len(parts) < 2 {
		return xy, nil, errors.New("needs X Y")
	}
	for i := 0; i < 2; i++ {
		v, err := strconv.Atoi(parts[i])
		if err != nil || v < -1000000 || v > 1000000 {
			return xy, nil, fmt.Errorf("%q is not a coordinate", parts[i])
		}
		xy[i] = v
	}
	return xy, parts[2:], nil
}

// parseDuration accepts Go syntax or a bare number of milliseconds.
func parseDuration(s string) (time.Duration, error) {
	if n, err := strconv.ParseFloat(s, 64); err == nil {
		if math.IsNaN(n) || math.IsInf(n, 0) || n <= 0 || n > 3.6e6 {
			return 0, fmt.Errorf("%q is not a duration in milliseconds", s)
		}
		d := time.Duration(n * float64(time.Millisecond))
		if d <= 0 {
			return 0, fmt.Errorf("%q is shorter than one nanosecond", s)
		}
		return d, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("%q is not a duration", s)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%q is not a positive duration", s)
	}
	if d > time.Hour {
		return 0, errors.New("longer than an hour")
	}
	return d, nil
}

// tokenize splits on whitespace, keeping double-quoted strings (with Go
// escapes) together. A # outside quotes starts a comment.
func tokenize(line string) ([]string, error) {
	var tokens []string
	i := 0
	for i < len(line) {
		switch ch := line[i]; {
		case ch == ' ' || ch == '\t' || ch == '\r':
			i++
		case ch == '#':
			return tokens, nil
		case ch == '"':
			j := i + 1
			for j < len(line) && line[j] != '"' {
				if line[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(line) {
				return nil, errors.New("unterminated string")
			}
			s, err := strconv.Unquote(line[i : j+1])
			if err != nil {
				return nil, fmt.Errorf("bad string %s: %w", line[i:j+1], err)
			}
			tokens = append(tokens, s)
			i = j + 1
		default:
			j := i
			for j < len(line) && line[j] != ' ' && line[j] != '\t' && line[j] != '\r' && line[j] != '#' {
				j++
			}
			tokens = append(tokens, line[i:j])
			i = j
		}
	}
	return tokens, nil
}

// ExpectationError is an expect command the board's state did not satisfy.
type ExpectationError struct {
	Problems []string
}

func (e *ExpectationError) Error() string { return strings.Join(e.Problems, "; ") }
