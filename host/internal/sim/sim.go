// Package sim is the board side of docs/PROTOCOL.md in Go: the executable
// reading of the contract that the host tool is tested against, and the
// reference for the firmware. It also plays the target just enough to be
// useful: Caps, Num and Scroll Lock toggle the keyboard LEDs, USB attach
// enumerates, and a suspended target wakes on remote wakeup.
package sim

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/ptudor/accessibility-bridge/host/internal/frame"
	"github.com/ptudor/accessibility-bridge/host/internal/keymap"
	"github.com/ptudor/accessibility-bridge/host/internal/proto"
)

// Timing fixed by the protocol.
const (
	Watchdog    = 500 * time.Millisecond
	WakeupGrace = 2 * time.Second
	enumerate   = 100 * time.Millisecond
)

// ErrBootloader is returned by Run after a BOOTLOADER command: the board
// has left the protocol.
var ErrBootloader = errors.New("board jumped to its bootloader")

// Report is one HID report the simulated target received.
type Report struct {
	Interface string `json:"interface"`
	// Keyboard: modifiers, reserved, six keys (HID 1.11 Appendix B.1).
	Keyboard [8]byte `json:"keyboard,omitempty"`
	// Mouse: buttons, dx, dy, wheel.
	Mouse [4]byte `json:"mouse,omitempty"`
	// Pointer: absolute.
	Buttons uint8  `json:"buttons,omitempty"`
	X       uint16 `json:"x,omitempty"`
	Y       uint16 `json:"y,omitempty"`
	Wheel   int8   `json:"wheel,omitempty"`
	// Consumer usage, 0 for release.
	Consumer uint16 `json:"consumer,omitempty"`
}

// String renders a report the way hidbridge-sim prints it.
func (r Report) String() string {
	switch r.Interface {
	case "keyboard":
		keys := ""
		for _, k := range r.Keyboard[2:] {
			if k != 0 {
				keys += " " + keymap.Name(k)
			}
		}
		if keys == "" {
			keys = " -"
		}
		return fmt.Sprintf("keyboard mods=%08b keys=%s", r.Keyboard[0], keys[1:])
	case "mouse":
		return fmt.Sprintf("mouse buttons=%03b dx=%d dy=%d wheel=%d", r.Mouse[0], int8(r.Mouse[1]), int8(r.Mouse[2]), int8(r.Mouse[3]))
	case "pointer":
		return fmt.Sprintf("pointer buttons=%03b x=%d y=%d wheel=%d", r.Buttons, r.X, r.Y, r.Wheel)
	case "consumer":
		if r.Consumer == 0 {
			return "consumer release"
		}
		return "consumer " + keymap.ConsumerName(r.Consumer)
	}
	return r.Interface
}

// Options configure a simulated board.
type Options struct {
	USBState      uint8 // initial state; zero value USBDetached, so set it
	WakeupEnabled bool
	// ManualResume leaves the target suspended after remote wakeup until
	// SetUSB resumes it, allowing wakeup timeouts to be rehearsed.
	ManualResume  bool
	Identity      string
	FirmwareMajor uint8
	FirmwareMinor uint8
	// Reports receives every report the target would see. Calls are
	// serialized; the callback must return promptly and must not mutate Board.
	Reports func(Report)
	Logger  *slog.Logger
}

type queued struct {
	seq uint8
	msg proto.Message
}

// Board is one simulated board. Run drives it over a byte stream.
type Board struct {
	opts Options
	log  *slog.Logger
	// opMu orders state changes, reports and events as one board operation,
	// including while mu is released for callbacks and wire writes.
	opMu sync.Mutex

	mu             sync.Mutex
	session        bool
	expected       uint8
	queue          []queued
	queueGen       int // bumped whenever the queue is cleared, to cut a WAIT short
	held           held
	usb            uint8
	wakeupEnabled  bool
	leds           uint8
	kbdBoot        bool
	mouseBoot      bool
	eventCounter   uint8
	lastFrame      time.Time
	start          time.Time
	watchdogs      uint16
	rxErrors       uint16
	dropped        uint16
	bootloader     bool
	activeWait     bool
	resumeDeadline time.Time
	replies        map[uint8]frame.Frame

	wmu      sync.Mutex
	w        io.Writer
	wakeup   chan struct{}
	cancel   context.CancelFunc
	writeErr error
}

type held struct {
	modifiers      uint8
	keys           [6]uint8
	mouseButtons   uint8
	pointerButtons uint8
	consumer       uint16
}

func (h held) any() bool {
	if h.modifiers != 0 || h.mouseButtons != 0 || h.pointerButtons != 0 || h.consumer != 0 {
		return true
	}
	for _, k := range h.keys {
		if k != 0 {
			return true
		}
	}
	return false
}

// New makes a board that has just reset.
func New(opts Options) *Board {
	if opts.Identity == "" {
		opts.Identity = fmt.Sprintf("accessibility-bridge R1 fw %d.%d sn SIMULATED", opts.FirmwareMajor, opts.FirmwareMinor)
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Board{
		opts:          opts,
		log:           opts.Logger,
		usb:           opts.USBState,
		wakeupEnabled: opts.WakeupEnabled,
		kbdBoot:       true,
		mouseBoot:     true,
		start:         time.Now(),
		wakeup:        make(chan struct{}, 1),
		replies:       make(map[uint8]frame.Frame),
	}
}

// Snapshot is the board's state for tests and the simulator's display.
type Snapshot struct {
	Session   bool
	Expected  uint8
	Queue     int
	Held      bool
	Modifiers uint8
	Keys      [6]uint8
	USB       uint8
	LEDs      uint8
	Watchdogs uint16
	RxErrors  uint16
	Dropped   uint16
}

// Snapshot reads the state.
func (b *Board) Snapshot() Snapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	return Snapshot{
		Session: b.session, Expected: b.expected, Queue: len(b.queue), Held: b.held.any(),
		Modifiers: b.held.modifiers, Keys: b.held.keys, USB: b.usb, LEDs: b.leds,
		Watchdogs: b.watchdogs, RxErrors: b.rxErrors, Dropped: b.dropped,
	}
}

// Run serves the protocol on rw until ctx ends, the stream fails, or a
// BOOTLOADER command arrives.
func (b *Board) Run(ctx context.Context, rw io.ReadWriter) error {
	if _, err := proto.ParseIdentity([]byte(b.opts.Identity)); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	b.wmu.Lock()
	b.w, b.cancel = rw, cancel
	b.wmu.Unlock()
	stopIO := func() bool { return true }
	if closer, ok := rw.(io.Closer); ok {
		stopIO = context.AfterFunc(ctx, func() { _ = closer.Close() })
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); b.executor(ctx) }()
	go func() { defer wg.Done(); b.watchdog(ctx) }()
	defer wg.Wait()
	defer cancel()
	defer func() {
		// A normal return leaves transport ownership with the caller, which
		// may need to drain the final BOOTLOADER ACK before closing a PTY.
		stopIO()
		cancel()
		b.opMu.Lock()
		defer b.opMu.Unlock()
		b.wmu.Lock()
		b.w = nil
		b.wmu.Unlock()
		b.mu.Lock()
		defer b.mu.Unlock()
		b.clearQueueLocked()
		b.releaseLocked(proto.ReleasedCommand)
	}()
	buf := make([]byte, 512)
	var dec frame.Decoder
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		n, err := rw.Read(buf)
		if n > 0 {
			dec.Push(buf[:n], b.handle)
			b.mu.Lock()
			boot := b.bootloader
			b.mu.Unlock()
			if boot {
				return ErrBootloader
			}
		}
		if err != nil {
			b.wmu.Lock()
			writeErr := b.writeErr
			b.wmu.Unlock()
			if writeErr != nil {
				return writeErr
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
	}
}

func (b *Board) write(f frame.Frame) {
	wire, err := frame.Encode(f)
	if err != nil {
		panic("sim: " + err.Error())
	}
	b.wmu.Lock()
	defer b.wmu.Unlock()
	if b.w != nil {
		n, err := b.w.Write(wire)
		if err == nil && n != len(wire) {
			err = io.ErrShortWrite
		}
		if err != nil {
			b.writeErr = err
			if b.cancel != nil {
				b.cancel()
			}
		}
	}
}

func (b *Board) ack(seq uint8, body []byte) {
	f := frame.Frame{Seq: seq, Type: proto.TypeACK, Body: body}
	b.remember(f)
	b.write(f)
}

// remember retains the outcome of consumed commands, including refusals.
func (b *Board) remember(f frame.Frame) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for seq := range b.replies {
		if uint8(b.expected-seq) > proto.Window {
			delete(b.replies, seq)
		}
	}
	b.replies[f.Seq] = f
}
func (b *Board) nak(seq, code uint8) {
	b.mu.Lock()
	expected := b.expected
	switch code {
	case proto.NAKFraming, proto.NAKCRC, proto.NAKSequence, proto.NAKType, proto.NAKLength:
		b.rxErrors++
	}
	b.mu.Unlock()
	f := frame.Frame{Seq: seq, Type: proto.TypeNAK, Body: []byte{code, expected}}
	if code == proto.NAKType || code == proto.NAKLength || code == proto.NAKState {
		b.remember(f)
	}
	b.write(f)
}

// emitLocked sends an event; b.mu must be held.
func (b *Board) emitLocked(e proto.Event) {
	e.Counter = b.eventCounter
	b.eventCounter++
	f := e.Encode()
	b.mu.Unlock()
	b.write(f)
	b.mu.Lock()
}

func (b *Board) handle(f frame.Frame, err error) {
	b.opMu.Lock()
	defer b.opMu.Unlock()
	if b.bootloader {
		return
	}
	if err != nil {
		code := proto.NAKFraming
		if errors.Is(err, frame.ErrCRC) {
			code = proto.NAKCRC
		}
		b.nak(0, code)
		return
	}
	want, ok := proto.BodyLength[f.Type]
	b.mu.Lock()
	if f.Type == proto.TypeHello && len(f.Body) == want {
		b.clearQueueLocked()
		b.releaseLocked(proto.ReleasedCommand)
		clear(b.replies)
		b.session = true
		b.expected = f.Seq + 1
		b.lastFrame = time.Now()
		b.mu.Unlock()
		b.ack(f.Seq, []byte{proto.Version, b.opts.FirmwareMajor, b.opts.FirmwareMinor, proto.QueueCapacity})
		return
	}
	if !b.session {
		b.mu.Unlock()
		b.nak(f.Seq, proto.NAKNoSession)
		return
	}
	switch {
	case f.Seq == b.expected:
		// Accepted below; TYPE, LENGTH and STATE refusals consume the
		// sequence number, QUEUE_FULL does not (PROTOCOL.md, sequence
		// numbers).
	case uint8(b.expected-f.Seq) <= proto.Window:
		// Repeat the original outcome without executing the command again.
		b.lastFrame = time.Now()
		reply, known := b.replies[f.Seq]
		b.mu.Unlock()
		if !known {
			b.nak(f.Seq, proto.NAKSequence)
			return
		}
		b.write(reply)
		return
	default:
		b.mu.Unlock()
		b.nak(f.Seq, proto.NAKSequence)
		return
	}
	b.lastFrame = time.Now()
	if !ok {
		b.expected++
		b.mu.Unlock()
		b.nak(f.Seq, proto.NAKType)
		return
	}
	if len(f.Body) != want {
		b.expected++
		b.mu.Unlock()
		b.nak(f.Seq, proto.NAKLength)
		return
	}
	if f.Type == proto.TypeWait && (uint16(f.Body[0])|uint16(f.Body[1])<<8 == 0 || uint16(f.Body[0])|uint16(f.Body[1])<<8 > proto.MaxWaitMS) {
		b.expected++
		b.mu.Unlock()
		b.nak(f.Seq, proto.NAKState)
		return
	}
	if proto.Queued(f.Type) {
		if proto.Input(f.Type) && !(b.usb == proto.USBConfigured || (b.usb == proto.USBSuspended && b.wakeupEnabled)) {
			b.expected++
			b.mu.Unlock()
			b.nak(f.Seq, proto.NAKState)
			return
		}
		if len(b.queue) >= proto.QueueCapacity {
			b.mu.Unlock()
			b.nak(f.Seq, proto.NAKQueueFull)
			return
		}
		b.expected++
		b.queue = append(b.queue, queued{f.Seq, proto.Message{Type: f.Type, Body: f.Body}})
		b.mu.Unlock()
		select {
		case b.wakeup <- struct{}{}:
		default:
		}
		b.ack(f.Seq, nil)
		return
	}
	b.expected++
	var body []byte
	switch f.Type {
	case proto.TypeStatus:
		b.mu.Unlock()
		body = b.status()
		b.mu.Lock()
	case proto.TypeIdentity:
		body = []byte(b.opts.Identity)
	case proto.TypeReleaseAll:
		b.clearQueueLocked()
		b.releaseLocked(proto.ReleasedCommand)
	case proto.TypeHeartbeat:
	case proto.TypeUSBDetach:
		b.clearQueueLocked()
		b.releaseLocked(proto.ReleasedDetach)
		b.setUSBLocked(proto.USBDetached, proto.CauseDetach)
	case proto.TypeUSBAttach:
		if b.usb == proto.USBDetached {
			b.setUSBLocked(proto.USBAttached, proto.CauseAttach)
			go b.enumerate()
		}
	case proto.TypeWakeup:
		body = []byte{0}
		if b.usb == proto.USBSuspended && b.wakeupEnabled {
			if !b.opts.ManualResume {
				b.setUSBLocked(proto.USBConfigured, proto.CauseRemoteWakeup)
			}
			body[0] = 1
		}
	case proto.TypeBootloader:
		b.clearQueueLocked()
		b.releaseLocked(proto.ReleasedDetach)
		b.setUSBLocked(proto.USBDetached, proto.CauseDetach)
		b.bootloader = true
	}
	b.mu.Unlock()
	b.ack(f.Seq, body)
}

func (b *Board) status() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	var flags uint8
	if b.wakeupEnabled {
		flags |= proto.FlagWakeupEnabled
	}
	if b.held.any() {
		flags |= proto.FlagHeld
	}
	if b.kbdBoot {
		flags |= proto.FlagKeyboardBoot
	}
	if b.mouseBoot {
		flags |= proto.FlagMouseBoot
	}
	return proto.StatusInfo{
		USBState: b.usb, Flags: flags, LEDs: b.leds, Queue: uint8(len(b.queue)),
		UptimeSeconds: uint32(time.Since(b.start) / time.Second),
		Watchdog:      b.watchdogs, RxErrors: b.rxErrors, Dropped: b.dropped,
	}.Encode()
}

func (b *Board) clearQueueLocked() {
	b.queue = nil
	b.queueGen++
	b.activeWait = false
	b.resumeDeadline = time.Time{}
}

// releaseLocked sends a zero report on every interface that holds
// something, then EVENT_RELEASED and EVENT_HELD. b.mu must be held.
func (b *Board) releaseLocked(reason uint8) {
	h := b.held
	b.held = held{}
	b.mu.Unlock()
	if h.modifiers != 0 || h.keys != [6]uint8{} {
		b.report(Report{Interface: "keyboard"})
	}
	if h.mouseButtons != 0 {
		b.report(Report{Interface: "mouse"})
	}
	if h.pointerButtons != 0 {
		b.report(Report{Interface: "pointer"})
	}
	if h.consumer != 0 {
		b.report(Report{Interface: "consumer"})
	}
	b.mu.Lock()
	b.emitLocked(proto.Event{Type: proto.TypeEventReleased, Reason: reason})
	if h.any() {
		b.emitLocked(proto.Event{Type: proto.TypeEventHeld, Held: false})
	}
}

func (b *Board) report(r Report) {
	if b.opts.Reports != nil {
		b.opts.Reports(r)
	}
}

func (b *Board) setUSBLocked(state, cause uint8) {
	b.usb = state
	if state != proto.USBSuspended {
		b.resumeDeadline = time.Time{}
	}
	b.emitLocked(proto.Event{Type: proto.TypeEventUSB, USBState: state, Cause: cause})
}

func (b *Board) enumerate() {
	time.Sleep(enumerate)
	b.opMu.Lock()
	defer b.opMu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.usb == proto.USBAttached {
		b.setUSBLocked(proto.USBConfigured, proto.CauseConfigured)
	}
}

// SetUSB plays the target: a bus reset, suspend, resume or unplug. Leaving
// the configured state releases everything first, as the firmware must.
func (b *Board) SetUSB(state, cause uint8) {
	b.opMu.Lock()
	defer b.opMu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	if state != proto.USBConfigured {
		reason := proto.ReleasedBusReset
		switch cause {
		case proto.CauseSuspend:
			reason = proto.ReleasedSuspend
		case proto.CausePower, proto.CauseDetach:
			reason = proto.ReleasedDetach
		}
		b.clearQueueLocked()
		b.releaseLocked(reason)
	}
	b.setUSBLocked(state, cause)
}

// SetLEDs plays the target writing the keyboard output report.
func (b *Board) SetLEDs(leds uint8) {
	b.opMu.Lock()
	defer b.opMu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	b.leds = leds
	b.emitLocked(proto.Event{Type: proto.TypeEventLEDs, LEDs: leds})
}

// SetWakeupEnabled plays the target setting the remote-wakeup feature.
func (b *Board) SetWakeupEnabled(enabled bool) {
	b.opMu.Lock()
	defer b.opMu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	b.wakeupEnabled = enabled
	b.emitLocked(proto.Event{Type: proto.TypeEventWakeup, Enabled: enabled})
}

// SetProtocol plays the target choosing boot or report protocol.
func (b *Board) SetProtocol(iface, protocol uint8) {
	b.opMu.Lock()
	defer b.opMu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	if iface == proto.InterfaceKeyboard {
		b.kbdBoot = protocol == 0
	} else {
		b.mouseBoot = protocol == 0
	}
	b.emitLocked(proto.Event{Type: proto.TypeEventProtocol, Interface: iface, Protocol: protocol})
}

func (b *Board) watchdog(ctx context.Context) {
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		b.opMu.Lock()
		b.mu.Lock()
		if (b.held.any() || len(b.queue) > 0 || b.activeWait) && time.Since(b.lastFrame) > Watchdog {
			b.watchdogs++
			b.clearQueueLocked()
			b.releaseLocked(proto.ReleasedWatchdog)
			b.log.Warn("watchdog released everything")
		}
		b.mu.Unlock()
		b.opMu.Unlock()
	}
}

func (b *Board) executor(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		b.opMu.Lock()
		b.mu.Lock()
		if len(b.queue) == 0 {
			b.mu.Unlock()
			b.opMu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-b.wakeup:
			}
			continue
		}
		item := b.queue[0]
		gen := b.queueGen
		if proto.Input(item.msg.Type) && b.usb == proto.USBSuspended {
			if !b.wakeupEnabled {
				// The target disabled wakeup after the command was queued.
				b.clearQueueLocked()
				b.releaseLocked(proto.ReleasedSuspend)
				b.mu.Unlock()
				b.opMu.Unlock()
				continue
			}
			if b.opts.ManualResume {
				if b.resumeDeadline.IsZero() {
					b.resumeDeadline = time.Now().Add(WakeupGrace)
				}
				if time.Now().After(b.resumeDeadline) {
					b.clearQueueLocked()
					b.releaseLocked(proto.ReleasedSuspend)
				}
				b.mu.Unlock()
				b.opMu.Unlock()
				select {
				case <-ctx.Done():
					return
				case <-time.After(10 * time.Millisecond):
				}
				continue
			}
			b.setUSBLocked(proto.USBConfigured, proto.CauseRemoteWakeup)
		}
		b.queue = b.queue[1:]
		b.activeWait = item.msg.Type == proto.TypeWait
		b.mu.Unlock()
		b.opMu.Unlock()
		b.execute(ctx, item, gen)
		b.mu.Lock()
		b.activeWait = false
		b.mu.Unlock()
	}
}

func (b *Board) execute(ctx context.Context, item queued, gen int) {
	m := item.msg
	switch m.Type {
	case proto.TypeWait:
		ms := time.Duration(uint16(m.Body[0])|uint16(m.Body[1])<<8) * time.Millisecond
		deadline := time.NewTimer(ms)
		defer deadline.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-deadline.C:
				return
			case <-time.After(10 * time.Millisecond):
				b.mu.Lock()
				cut := b.queueGen != gen
				b.mu.Unlock()
				if cut {
					return
				}
			}
		}
	}
	b.opMu.Lock()
	defer b.opMu.Unlock()
	b.mu.Lock()
	if b.queueGen != gen || ctx.Err() != nil {
		b.mu.Unlock()
		return
	}
	if m.Type == proto.TypeSync {
		b.emitLocked(proto.Event{Type: proto.TypeEventDone, DoneSeq: item.seq})
		b.mu.Unlock()
		return
	}
	wasHeld := b.held.any()
	oldKeys := b.held.keys
	var r Report
	switch m.Type {
	case proto.TypeKeyDown, proto.TypeKeyUp:
		u := m.Body[0]
		down := m.Type == proto.TypeKeyDown
		switch {
		case keymap.IsModifier(u) && down:
			b.held.modifiers |= keymap.ModifierBit(u)
		case keymap.IsModifier(u):
			b.held.modifiers &^= keymap.ModifierBit(u)
		case down:
			placed := false
			for _, k := range b.held.keys {
				if k == u {
					placed = true
				}
			}
			for i := range b.held.keys {
				if !placed && b.held.keys[i] == 0 {
					b.held.keys[i] = u
					placed = true
				}
			}
			if !placed {
				b.dropped++
				b.mu.Unlock()
				return
			}
		default:
			for i := range b.held.keys {
				if b.held.keys[i] == u {
					b.held.keys[i] = 0
				}
			}
		}
		r = b.keyboardReportLocked()
	case proto.TypeKeyReport:
		b.held.modifiers = m.Body[0]
		copy(b.held.keys[:], m.Body[2:8])
		r = b.keyboardReportLocked()
	case proto.TypeMouse:
		b.held.mouseButtons = m.Body[0] & 0x07
		r = Report{Interface: "mouse", Mouse: [4]byte{b.held.mouseButtons, byte(proto.ClampI8(int(int8(m.Body[1])))), byte(proto.ClampI8(int(int8(m.Body[2])))), byte(proto.ClampI8(int(int8(m.Body[3]))))}}
	case proto.TypePointer:
		b.held.pointerButtons = m.Body[0] & 0x07
		r = Report{Interface: "pointer", Buttons: b.held.pointerButtons,
			X: uint16(m.Body[1]) | uint16(m.Body[2])<<8, Y: uint16(m.Body[3]) | uint16(m.Body[4])<<8, Wheel: int8(m.Body[5])}
	case proto.TypeConsumer:
		if m.Body[2] != 0 {
			b.held.consumer = uint16(m.Body[0]) | uint16(m.Body[1])<<8
		} else {
			b.held.consumer = 0
		}
		r = Report{Interface: "consumer", Consumer: b.held.consumer}
	}
	if r.Interface == "keyboard" {
		for usage, bit := range map[uint8]uint8{keymap.UsageCapsLock: proto.LEDCapsLock, keymap.UsageNumLock: proto.LEDNumLock, keymap.UsageScrollLock: proto.LEDScrollLock} {
			contains := func(keys [6]uint8) bool {
				for _, k := range keys {
					if k == usage {
						return true
					}
				}
				return false
			}
			if contains(b.held.keys) && !contains(oldKeys) {
				b.leds ^= bit
				b.emitLocked(proto.Event{Type: proto.TypeEventLEDs, LEDs: b.leds})
			}
		}
	}
	nowHeld := b.held.any()
	b.mu.Unlock()
	b.report(r)
	if nowHeld != wasHeld {
		b.mu.Lock()
		b.emitLocked(proto.Event{Type: proto.TypeEventHeld, Held: nowHeld})
		b.mu.Unlock()
	}
}

func (b *Board) keyboardReportLocked() Report {
	r := Report{Interface: "keyboard"}
	r.Keyboard[0] = b.held.modifiers
	copy(r.Keyboard[2:], b.held.keys[:])
	return r
}
