// Package proto holds the message types, bodies and codes of
// docs/PROTOCOL.md version 1. Everything here is data; the link package
// carries it.
package proto

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"github.com/ptudor/accessibility-bridge/host/internal/frame"
)

// Version is the protocol version carried by HELLO.
const Version = 1

// Window is the host's send window and the board's duplicate window.
const Window = 8

// QueueCapacity is the board's execution queue depth.
const QueueCapacity = 16

// Command types, host to board.
const (
	TypeHello      uint8 = 0x01
	TypeStatus     uint8 = 0x02
	TypeIdentity   uint8 = 0x03
	TypeKeyDown    uint8 = 0x10
	TypeKeyUp      uint8 = 0x11
	TypeKeyReport  uint8 = 0x12
	TypeMouse      uint8 = 0x20
	TypePointer    uint8 = 0x21
	TypeConsumer   uint8 = 0x22
	TypeReleaseAll uint8 = 0x30
	TypeWait       uint8 = 0x31
	TypeSync       uint8 = 0x32
	TypeHeartbeat  uint8 = 0x33
	TypeUSBDetach  uint8 = 0x40
	TypeUSBAttach  uint8 = 0x41
	TypeWakeup     uint8 = 0x42
	TypeBootloader uint8 = 0x43
)

// Response types, board to host.
const (
	TypeACK uint8 = 0x80
	TypeNAK uint8 = 0x81
)

// Event types, board to host.
const (
	TypeEventUSB      uint8 = 0x90
	TypeEventProtocol uint8 = 0x91
	TypeEventLEDs     uint8 = 0x92
	TypeEventWakeup   uint8 = 0x93
	TypeEventDone     uint8 = 0x94
	TypeEventReleased uint8 = 0x95
	TypeEventHeld     uint8 = 0x96
)

// NAK codes.
const (
	NAKFraming   uint8 = 1
	NAKCRC       uint8 = 2
	NAKSequence  uint8 = 3
	NAKType      uint8 = 4
	NAKLength    uint8 = 5
	NAKQueueFull uint8 = 6
	NAKState     uint8 = 7
	NAKNoSession uint8 = 8
)

// USB states.
const (
	USBDetached   uint8 = 0
	USBAttached   uint8 = 1
	USBConfigured uint8 = 2
	USBSuspended  uint8 = 3
)

// EVENT_USB causes.
const (
	CausePower        uint8 = 0
	CauseBusReset     uint8 = 1
	CauseConfigured   uint8 = 2
	CauseSuspend      uint8 = 3
	CauseResume       uint8 = 4
	CauseDetach       uint8 = 5
	CauseAttach       uint8 = 6
	CauseRemoteWakeup uint8 = 7
)

// EVENT_RELEASED reasons.
const (
	ReleasedWatchdog uint8 = 1
	ReleasedBusReset uint8 = 2
	ReleasedSuspend  uint8 = 3
	ReleasedDetach   uint8 = 4
	ReleasedCommand  uint8 = 5
)

// Status flag bits.
const (
	FlagWakeupEnabled uint8 = 1 << 0
	FlagHeld          uint8 = 1 << 1
	FlagKeyboardBoot  uint8 = 1 << 2
	FlagMouseBoot     uint8 = 1 << 3
)

// Keyboard LED bits, HID 1.11 Appendix B.1 output report.
const (
	LEDNumLock    uint8 = 1 << 0
	LEDCapsLock   uint8 = 1 << 1
	LEDScrollLock uint8 = 1 << 2
	LEDCompose    uint8 = 1 << 3
	LEDKana       uint8 = 1 << 4
)

// Mouse and pointer button bits.
const (
	ButtonLeft   uint8 = 1 << 0
	ButtonRight  uint8 = 1 << 1
	ButtonMiddle uint8 = 1 << 2
)

// Interfaces reported by EVENT_PROTOCOL.
const (
	InterfaceKeyboard uint8 = 0
	InterfaceMouse    uint8 = 1
)

// Limits.
const (
	MaxWaitMS  = 10000
	PointerMax = 32767
	// ConsumerMax is the largest consumer usage the board's report
	// descriptor declares (firmware descriptors.h); the target ignores a
	// larger one.
	ConsumerMax = 0x03FF
	MaxIdentity = 64
)

// Message is a command without its sequence number.
type Message struct {
	Type uint8
	Body []byte
}

// BodyLength is the fixed body length of each command type.
var BodyLength = map[uint8]int{
	TypeHello: 1, TypeStatus: 0, TypeIdentity: 0,
	TypeKeyDown: 1, TypeKeyUp: 1, TypeKeyReport: 8,
	TypeMouse: 4, TypePointer: 6, TypeConsumer: 3,
	TypeReleaseAll: 0, TypeWait: 2, TypeSync: 0, TypeHeartbeat: 0,
	TypeUSBDetach: 0, TypeUSBAttach: 0, TypeWakeup: 0, TypeBootloader: 0,
}

// Queued reports whether a command type executes from the board's queue.
func Queued(t uint8) bool {
	switch t {
	case TypeKeyDown, TypeKeyUp, TypeKeyReport, TypeMouse, TypePointer, TypeConsumer, TypeWait, TypeSync:
		return true
	}
	return false
}

// Input reports whether a command type produces a HID report, so the board
// refuses it while the target has not configured the device.
func Input(t uint8) bool {
	switch t {
	case TypeKeyDown, TypeKeyUp, TypeKeyReport, TypeMouse, TypePointer, TypeConsumer:
		return true
	}
	return false
}

// Constructors. Each returns a Message with its body laid out as the
// protocol tables specify.

func Hello() Message          { return Message{TypeHello, []byte{Version}} }
func Status() Message         { return Message{TypeStatus, nil} }
func Identity() Message       { return Message{TypeIdentity, nil} }
func KeyDown(u uint8) Message { return Message{TypeKeyDown, []byte{u}} }
func KeyUp(u uint8) Message   { return Message{TypeKeyUp, []byte{u}} }
func KeyReport(r [8]byte) Message {
	return Message{TypeKeyReport, append([]byte(nil), r[:]...)}
}
func Mouse(buttons uint8, dx, dy, wheel int8) Message {
	return Message{TypeMouse, []byte{buttons, byte(dx), byte(dy), byte(wheel)}}
}
func Pointer(buttons uint8, x, y uint16, wheel int8) Message {
	b := make([]byte, 6)
	b[0] = buttons
	binary.LittleEndian.PutUint16(b[1:], x)
	binary.LittleEndian.PutUint16(b[3:], y)
	b[5] = byte(wheel)
	return Message{TypePointer, b}
}
func Consumer(usage uint16, press bool) Message {
	b := make([]byte, 3)
	binary.LittleEndian.PutUint16(b, usage)
	if press {
		b[2] = 1
	}
	return Message{TypeConsumer, b}
}
func ReleaseAll() Message { return Message{TypeReleaseAll, nil} }
func Sync() Message       { return Message{TypeSync, nil} }
func Heartbeat() Message  { return Message{TypeHeartbeat, nil} }
func USBDetach() Message  { return Message{TypeUSBDetach, nil} }
func USBAttach() Message  { return Message{TypeUSBAttach, nil} }
func Wakeup() Message     { return Message{TypeWakeup, nil} }
func Bootloader() Message { return Message{TypeBootloader, nil} }

// Wait builds a WAIT of ms milliseconds, 1 to MaxWaitMS.
func Wait(ms int) (Message, error) {
	if ms < 1 || ms > MaxWaitMS {
		return Message{}, fmt.Errorf("wait of %d ms is outside 1..%d", ms, MaxWaitMS)
	}
	b := make([]byte, 2)
	binary.LittleEndian.PutUint16(b, uint16(ms))
	return Message{TypeWait, b}, nil
}

// ClampI8 fits a relative movement into the −127…127 the reports carry.
func ClampI8(v int) int8 {
	if v > 127 {
		return 127
	}
	if v < -127 {
		return -127
	}
	return int8(v)
}

// HelloInfo is the HELLO acknowledgement.
type HelloInfo struct {
	Version       uint8
	FirmwareMajor uint8
	FirmwareMinor uint8
	Queue         uint8
}

// ParseHello decodes a HELLO acknowledgement body.
func ParseHello(b []byte) (HelloInfo, error) {
	if len(b) != 4 {
		return HelloInfo{}, fmt.Errorf("hello acknowledgement of %d bytes, want 4", len(b))
	}
	h := HelloInfo{b[0], b[1], b[2], b[3]}
	if h.Version != Version {
		return h, fmt.Errorf("board speaks protocol version %d, this tool version %d", h.Version, Version)
	}
	if h.Queue == 0 {
		return h, errors.New("board reports a queue of zero entries")
	}
	return h, nil
}

// StatusInfo is the STATUS acknowledgement.
type StatusInfo struct {
	USBState      uint8  `json:"usb_state"`
	Flags         uint8  `json:"flags"`
	LEDs          uint8  `json:"leds"`
	Queue         uint8  `json:"queue"`
	UptimeSeconds uint32 `json:"uptime_seconds"`
	Watchdog      uint16 `json:"watchdog_releases"`
	RxErrors      uint16 `json:"rx_errors"`
	Dropped       uint16 `json:"dropped_keys"`
}

// StatusLength is the STATUS body length.
const StatusLength = 14

// ParseStatus decodes a STATUS acknowledgement body.
func ParseStatus(b []byte) (StatusInfo, error) {
	if len(b) != StatusLength {
		return StatusInfo{}, fmt.Errorf("status of %d bytes, want %d", len(b), StatusLength)
	}
	return StatusInfo{
		USBState:      b[0],
		Flags:         b[1],
		LEDs:          b[2],
		Queue:         b[3],
		UptimeSeconds: binary.LittleEndian.Uint32(b[4:]),
		Watchdog:      binary.LittleEndian.Uint16(b[8:]),
		RxErrors:      binary.LittleEndian.Uint16(b[10:]),
		Dropped:       binary.LittleEndian.Uint16(b[12:]),
	}, nil
}

// Encode lays a StatusInfo out as its body; the simulator uses it.
func (s StatusInfo) Encode() []byte {
	b := make([]byte, StatusLength)
	b[0], b[1], b[2], b[3] = s.USBState, s.Flags, s.LEDs, s.Queue
	binary.LittleEndian.PutUint32(b[4:], s.UptimeSeconds)
	binary.LittleEndian.PutUint16(b[8:], s.Watchdog)
	binary.LittleEndian.PutUint16(b[10:], s.RxErrors)
	binary.LittleEndian.PutUint16(b[12:], s.Dropped)
	return b
}

// ParseIdentity checks an IDENTITY acknowledgement: printable ASCII, 1 to
// MaxIdentity bytes.
func ParseIdentity(b []byte) (string, error) {
	if len(b) == 0 || len(b) > MaxIdentity {
		return "", fmt.Errorf("identity of %d bytes, want 1..%d", len(b), MaxIdentity)
	}
	for _, c := range b {
		if c < 0x20 || c > 0x7E {
			return "", fmt.Errorf("identity contains byte %#02x", c)
		}
	}
	return string(b), nil
}

// NAKInfo is a NAK body.
type NAKInfo struct {
	Code     uint8
	Expected uint8
}

// ParseNAK decodes a NAK body.
func ParseNAK(b []byte) (NAKInfo, error) {
	if len(b) != 2 {
		return NAKInfo{}, fmt.Errorf("nak of %d bytes, want 2", len(b))
	}
	return NAKInfo{b[0], b[1]}, nil
}

// Error is a command the board refused.
type Error struct {
	Type     uint8
	Seq      uint8
	Code     uint8
	Expected uint8
}

func (e *Error) Error() string {
	return fmt.Sprintf("board refused %s (seq %d): %s", TypeName(e.Type), e.Seq, NAKName(e.Code))
}

// Event is one board event, decoded. Only the fields for Type are set.
type Event struct {
	Counter   uint8 `json:"counter"`
	Type      uint8 `json:"type"`
	USBState  uint8 `json:"usb_state,omitempty"`
	Cause     uint8 `json:"cause,omitempty"`
	Interface uint8 `json:"interface,omitempty"`
	Protocol  uint8 `json:"protocol,omitempty"`
	LEDs      uint8 `json:"leds,omitempty"`
	Enabled   bool  `json:"enabled,omitempty"`
	DoneSeq   uint8 `json:"done_seq,omitempty"`
	Reason    uint8 `json:"reason,omitempty"`
	Held      bool  `json:"held,omitempty"`
}

// IsEvent reports whether a frame type is a board event.
func IsEvent(t uint8) bool { return t >= 0x90 && t <= 0x9F }

// ParseEvent decodes an event frame.
func ParseEvent(f frame.Frame) (Event, error) {
	e := Event{Counter: f.Seq, Type: f.Type}
	need := map[uint8]int{
		TypeEventUSB: 2, TypeEventProtocol: 2, TypeEventLEDs: 1, TypeEventWakeup: 1,
		TypeEventDone: 1, TypeEventReleased: 1, TypeEventHeld: 1,
	}
	n, ok := need[f.Type]
	if !ok {
		return e, fmt.Errorf("unknown event type %#02x", f.Type)
	}
	if len(f.Body) != n {
		return e, fmt.Errorf("%s of %d bytes, want %d", TypeName(f.Type), len(f.Body), n)
	}
	switch f.Type {
	case TypeEventUSB:
		e.USBState, e.Cause = f.Body[0], f.Body[1]
	case TypeEventProtocol:
		e.Interface, e.Protocol = f.Body[0], f.Body[1]
	case TypeEventLEDs:
		e.LEDs = f.Body[0]
	case TypeEventWakeup:
		e.Enabled = f.Body[0] != 0
	case TypeEventDone:
		e.DoneSeq = f.Body[0]
	case TypeEventReleased:
		e.Reason = f.Body[0]
	case TypeEventHeld:
		e.Held = f.Body[0] != 0
	}
	return e, nil
}

// Encode lays an event out as a frame; the simulator uses it.
func (e Event) Encode() frame.Frame {
	f := frame.Frame{Seq: e.Counter, Type: e.Type}
	switch e.Type {
	case TypeEventUSB:
		f.Body = []byte{e.USBState, e.Cause}
	case TypeEventProtocol:
		f.Body = []byte{e.Interface, e.Protocol}
	case TypeEventLEDs:
		f.Body = []byte{e.LEDs}
	case TypeEventWakeup:
		f.Body = []byte{boolByte(e.Enabled)}
	case TypeEventDone:
		f.Body = []byte{e.DoneSeq}
	case TypeEventReleased:
		f.Body = []byte{e.Reason}
	case TypeEventHeld:
		f.Body = []byte{boolByte(e.Held)}
	}
	return f
}

func boolByte(b bool) byte {
	if b {
		return 1
	}
	return 0
}

// String renders an event the way the tool prints it.
func (e Event) String() string {
	switch e.Type {
	case TypeEventUSB:
		return fmt.Sprintf("usb %s (%s)", USBStateName(e.USBState), CauseName(e.Cause))
	case TypeEventProtocol:
		iface := "keyboard"
		if e.Interface == InterfaceMouse {
			iface = "mouse"
		}
		proto := "boot"
		if e.Protocol != 0 {
			proto = "report"
		}
		return fmt.Sprintf("protocol %s %s", iface, proto)
	case TypeEventLEDs:
		return "leds " + LEDNames(e.LEDs)
	case TypeEventWakeup:
		return fmt.Sprintf("remote wakeup enabled=%v", e.Enabled)
	case TypeEventDone:
		return fmt.Sprintf("done seq %d", e.DoneSeq)
	case TypeEventReleased:
		return "released (" + ReleasedName(e.Reason) + ")"
	case TypeEventHeld:
		return fmt.Sprintf("held=%v", e.Held)
	}
	return fmt.Sprintf("event %#02x", e.Type)
}

// Names for output.

func TypeName(t uint8) string {
	names := map[uint8]string{
		TypeHello: "HELLO", TypeStatus: "STATUS", TypeIdentity: "IDENTITY",
		TypeKeyDown: "KEY_DOWN", TypeKeyUp: "KEY_UP", TypeKeyReport: "KEY_REPORT",
		TypeMouse: "MOUSE", TypePointer: "POINTER", TypeConsumer: "CONSUMER",
		TypeReleaseAll: "RELEASE_ALL", TypeWait: "WAIT", TypeSync: "SYNC", TypeHeartbeat: "HEARTBEAT",
		TypeUSBDetach: "USB_DETACH", TypeUSBAttach: "USB_ATTACH", TypeWakeup: "WAKEUP", TypeBootloader: "BOOTLOADER",
		TypeACK: "ACK", TypeNAK: "NAK",
		TypeEventUSB: "EVENT_USB", TypeEventProtocol: "EVENT_PROTOCOL", TypeEventLEDs: "EVENT_LEDS",
		TypeEventWakeup: "EVENT_WAKEUP", TypeEventDone: "EVENT_DONE", TypeEventReleased: "EVENT_RELEASED", TypeEventHeld: "EVENT_HELD",
	}
	if n, ok := names[t]; ok {
		return n
	}
	return fmt.Sprintf("type %#02x", t)
}

func NAKName(c uint8) string {
	names := [...]string{"", "FRAMING", "CRC", "SEQUENCE", "TYPE", "LENGTH", "QUEUE_FULL", "STATE", "NO_SESSION"}
	if int(c) < len(names) && names[c] != "" {
		return names[c]
	}
	return fmt.Sprintf("code %d", c)
}

func USBStateName(s uint8) string {
	names := [...]string{"detached", "attached", "configured", "suspended"}
	if int(s) < len(names) {
		return names[s]
	}
	return fmt.Sprintf("state %d", s)
}

func CauseName(c uint8) string {
	names := [...]string{"power", "bus reset", "configured", "suspend", "resume", "detach command", "attach command", "remote wakeup"}
	if int(c) < len(names) {
		return names[c]
	}
	return fmt.Sprintf("cause %d", c)
}

func ReleasedName(r uint8) string {
	names := [...]string{"", "watchdog", "bus reset", "suspend", "detach", "command"}
	if int(r) < len(names) && names[r] != "" {
		return names[r]
	}
	return fmt.Sprintf("reason %d", r)
}

// LEDNames renders a LED byte as "num caps scroll compose kana" or "none".
func LEDNames(leds uint8) string {
	var on []string
	for _, l := range []struct {
		bit  uint8
		name string
	}{{LEDNumLock, "num"}, {LEDCapsLock, "caps"}, {LEDScrollLock, "scroll"}, {LEDCompose, "compose"}, {LEDKana, "kana"}} {
		if leds&l.bit != 0 {
			on = append(on, l.name)
		}
	}
	if len(on) == 0 {
		return "none"
	}
	return strings.Join(on, " ")
}
