package script

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ptudor/accessibility-bridge/host/internal/capture"
	"github.com/ptudor/accessibility-bridge/host/internal/keymap"
	"github.com/ptudor/accessibility-bridge/host/internal/link"
	"github.com/ptudor/accessibility-bridge/host/internal/proto"
	"github.com/ptudor/accessibility-bridge/host/internal/session"
)

// Target is what the runner needs to know about the computer under test.
type Target struct {
	Screen      [2]int
	KeyInterval time.Duration
	ClickHold   time.Duration
}

// Runner executes commands on a session.
type Runner struct {
	Session *link.Session
	Target  Target
	// Grabber and CaptureDir serve the capture command; a zero Grabber
	// makes capture an error.
	Grabber    capture.Grabber
	CaptureDir string
	// Name labels captures and the transcript.
	Name string
	// Log receives the transcript; nil discards it.
	Log *session.Log
	// Out receives the human-readable trace; nil discards it.
	Out io.Writer
	// ShowEvents prints board events to Out as they arrive.
	ShowEvents bool

	start      time.Time
	captures   int
	pointerX   uint16
	pointerY   uint16
	stopEvents func()
	eventsDone chan struct{}
	printMu    sync.Mutex
}

// Start subscribes to board events for the transcript and trace.
func (r *Runner) Start() {
	r.start = time.Now()
	if r.Out == nil {
		r.Out = io.Discard
	}
	events, unsubscribe := r.Session.Subscribe(256)
	r.stopEvents = unsubscribe
	r.eventsDone = make(chan struct{})
	go func() {
		defer close(r.eventsDone)
		for e := range events {
			_ = r.Log.Record("event", map[string]any{"event": e.String(), "detail": e})
			// HELD mirrors the LED and DONE answers sync: both are in the
			// transcript, but neither tells the operator anything new.
			if r.ShowEvents && e.Type != proto.TypeEventHeld && e.Type != proto.TypeEventDone {
				r.printf("event: %s", e)
			}
		}
	}()
}

// Stop ends the event trace. The session is the caller's to close.
func (r *Runner) Stop() {
	if r.stopEvents != nil {
		r.stopEvents()
		r.stopEvents = nil
		<-r.eventsDone
	}
}

func (r *Runner) printf(format string, args ...any) {
	r.printMu.Lock()
	defer r.printMu.Unlock()
	fmt.Fprintf(r.Out, "[%8.3fs] %s\n", time.Since(r.start).Seconds(), fmt.Sprintf(format, args...))
}

// RunAll runs commands in order and stops at the first failure.
func (r *Runner) RunAll(ctx context.Context, cmds []*Command) error {
	for _, c := range cmds {
		if err := r.Run(ctx, c); err != nil {
			return err
		}
	}
	return nil
}

// Stream reads lines from in and runs each as it arrives. A line that does
// not parse, or a command the board refuses, is reported and the stream
// continues; a failed link or an ended context stops it. At EOF it waits
// for queued input and returns the first line error, if any.
func (r *Runner) Stream(ctx context.Context, in io.Reader) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	lines := make(chan string)
	readErr := make(chan error, 1)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(in)
		scanner.Buffer(make([]byte, 4096), 1024*1024)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
		readErr <- scanner.Err()
	}()
	lineNo := 0
	var firstErr error
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-r.Session.Done():
			return r.Session.Err()
		case line, ok := <-lines:
			if !ok {
				if err := <-readErr; err != nil {
					return err
				}
				if err := r.Session.Sync(ctx); err != nil {
					return err
				}
				return firstErr
			}
			lineNo++
			c, err := ParseLine(line, lineNo)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				r.printf("error: %v", err)
				_ = r.Log.Record("error", map[string]any{"line": lineNo, "text": line, "error": err.Error()})
				continue
			}
			if c == nil {
				continue
			}
			if err = r.Run(ctx, c); err != nil {
				if r.Session.Err() != nil || ctx.Err() != nil || r.Log.Err() != nil {
					return err
				}
				r.printf("error: %v", err)
				if firstErr == nil {
					firstErr = err
				}
			}
		}
	}
}

// Run executes one command: every frame it needs is submitted and
// acknowledged before Run returns, but queued input executes later, in
// order, with the board's timing. sync, capture and expect wait for it.
func (r *Runner) Run(ctx context.Context, c *Command) error {
	if c == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.Log.Err(); err != nil {
		return fmt.Errorf("transcript: %w", err)
	}
	if r.Out == nil {
		r.Out = io.Discard
	}
	if r.start.IsZero() {
		r.start = time.Now()
	}
	r.printf("#%d %s", c.Line, c.Text)
	err := r.run(ctx, c)
	fields := map[string]any{"line": c.Line, "text": c.Text}
	if err != nil {
		fields["error"] = err.Error()
	}
	err = errors.Join(err, r.Log.Record("command", fields))
	if err != nil {
		return fmt.Errorf("line %d: %s: %w", c.Line, c.Text, err)
	}
	return nil
}

func (r *Runner) run(ctx context.Context, c *Command) error {
	mouseButtons := r.Session.Held().MouseButtons
	switch c.Kind {
	case KindType:
		return r.typeText(ctx, c.Str)
	case KindKey:
		hold := c.Dur
		if hold == 0 {
			hold = r.Target.KeyInterval
		}
		var msgs []proto.Message
		for _, u := range c.Keys {
			msgs = append(msgs, proto.KeyDown(u))
		}
		msgs = append(msgs, r.wait(hold)...)
		for i := len(c.Keys) - 1; i >= 0; i-- {
			msgs = append(msgs, proto.KeyUp(c.Keys[i]))
		}
		msgs = append(msgs, r.wait(r.Target.KeyInterval)...)
		return r.send(ctx, msgs)
	case KindDown, KindUp:
		var msgs []proto.Message
		for _, u := range c.Keys {
			if c.Kind == KindDown {
				msgs = append(msgs, proto.KeyDown(u))
			} else {
				msgs = append(msgs, proto.KeyUp(u))
			}
			msgs = append(msgs, r.wait(r.Target.KeyInterval)...)
		}
		return r.send(ctx, msgs)
	case KindReport:
		return r.send(ctx, []proto.Message{proto.KeyReport(c.Report)})
	case KindMove:
		var msgs []proto.Message
		dx, dy := c.X, c.Y
		for dx != 0 || dy != 0 {
			sx, sy := proto.ClampI8(dx), proto.ClampI8(dy)
			dx -= int(sx)
			dy -= int(sy)
			msgs = append(msgs, proto.Mouse(mouseButtons, sx, sy, 0))
		}
		return r.send(ctx, msgs)
	case KindPointer:
		r.pointerX, r.pointerY = uint16(c.X), uint16(c.Y)
		return r.send(ctx, []proto.Message{proto.Pointer(r.Session.Held().PointerButtons, r.pointerX, r.pointerY, 0)})
	case KindClick:
		x, y, err := r.pixelsToUnits(c.X, c.Y)
		if err != nil {
			return err
		}
		r.pointerX, r.pointerY = x, y
		msgs := []proto.Message{proto.Pointer(0, x, y, 0)}
		msgs = append(msgs, r.wait(r.Target.ClickHold)...)
		clicks := 1
		if c.Double {
			clicks = 2
		}
		for i := 0; i < clicks; i++ {
			msgs = append(msgs, proto.Pointer(c.Button, x, y, 0))
			msgs = append(msgs, r.wait(r.Target.ClickHold)...)
			msgs = append(msgs, proto.Pointer(0, x, y, 0))
			msgs = append(msgs, r.wait(r.Target.ClickHold)...)
		}
		return r.send(ctx, msgs)
	case KindHover:
		x, y, err := r.pixelsToUnits(c.X, c.Y)
		if err != nil {
			return err
		}
		r.pointerX, r.pointerY = x, y
		return r.send(ctx, []proto.Message{proto.Pointer(r.Session.Held().PointerButtons, x, y, 0)})
	case KindDrag:
		x1, y1, err := r.pixelsToUnits(c.X, c.Y)
		if err != nil {
			return err
		}
		x2, y2, err := r.pixelsToUnits(c.X2, c.Y2)
		if err != nil {
			return err
		}
		// Arrive unpressed, press, then move in steps so the target sees motion while the button is
		// held (an operating system's drag threshold wants more than one jump), release at the end.
		steps := dragSteps(c.X, c.Y, c.X2, c.Y2)
		msgs := []proto.Message{proto.Pointer(0, x1, y1, 0)}
		msgs = append(msgs, r.wait(r.Target.ClickHold)...)
		msgs = append(msgs, proto.Pointer(c.Button, x1, y1, 0))
		msgs = append(msgs, r.wait(r.Target.ClickHold)...)
		for i := 1; i <= steps; i++ {
			x := uint16(int(x1) + (int(x2)-int(x1))*i/steps)
			y := uint16(int(y1) + (int(y2)-int(y1))*i/steps)
			msgs = append(msgs, proto.Pointer(c.Button, x, y, 0))
			msgs = append(msgs, r.wait(dragStepPause)...)
		}
		msgs = append(msgs, r.wait(r.Target.ClickHold)...)
		msgs = append(msgs, proto.Pointer(0, x2, y2, 0))
		msgs = append(msgs, r.wait(r.Target.ClickHold)...)
		r.pointerX, r.pointerY = x2, y2
		return r.send(ctx, msgs)
	case KindButton:
		var msgs []proto.Message
		switch c.Action {
		case "down":
			mouseButtons |= c.Button
			msgs = append(msgs, proto.Mouse(mouseButtons, 0, 0, 0))
		case "up":
			mouseButtons &^= c.Button
			msgs = append(msgs, proto.Mouse(mouseButtons, 0, 0, 0))
		case "click":
			msgs = append(msgs, proto.Mouse(mouseButtons|c.Button, 0, 0, 0))
			msgs = append(msgs, r.wait(r.Target.ClickHold)...)
			msgs = append(msgs, proto.Mouse(mouseButtons, 0, 0, 0))
		}
		return r.send(ctx, msgs)
	case KindScroll:
		var msgs []proto.Message
		for n := c.N; n != 0; {
			step := proto.ClampI8(n)
			n -= int(step)
			msgs = append(msgs, proto.Mouse(mouseButtons, 0, 0, step))
		}
		return r.send(ctx, msgs)
	case KindConsumer:
		hold := c.Dur
		if hold == 0 {
			hold = r.Target.KeyInterval
		}
		msgs := []proto.Message{proto.Consumer(c.Usage, true)}
		msgs = append(msgs, r.wait(hold)...)
		msgs = append(msgs, proto.Consumer(c.Usage, false))
		return r.send(ctx, msgs)
	case KindWait:
		return r.send(ctx, r.wait(c.Dur))
	case KindSleep:
		select {
		case <-time.After(c.Dur):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	case KindSync:
		return r.Session.Sync(ctx)
	case KindRelease:
		_, err := r.Session.SendContext(ctx, proto.ReleaseAll())
		return err
	case KindCapture:
		return r.capture(ctx, c.Str)
	case KindExpect:
		return r.expect(ctx, c)
	case KindEcho:
		return r.Log.Record("note", map[string]any{"text": c.Str})
	case KindDetach:
		_, err := r.Session.SendContext(ctx, proto.USBDetach())
		return err
	case KindAttach:
		_, err := r.Session.SendContext(ctx, proto.USBAttach())
		return err
	case KindWake:
		ack, err := r.Session.SendContext(ctx, proto.Wakeup())
		if err != nil {
			return err
		}
		if len(ack) != 1 || ack[0] == 0 {
			r.printf("remote wakeup not issued: the target is not suspended or did not enable it")
		} else {
			r.printf("remote wakeup issued")
		}
		return nil
	}
	return fmt.Errorf("unhandled command kind %d", c.Kind)
}

// dragStepPixels is the longest jump between two pointer reports of a drag, and dragStepPause the
// board-side pause after each; together they keep a drag under 8 ms per 40 pixels.
const (
	dragStepPixels = 40
	dragStepPause  = 8 * time.Millisecond
	dragStepsMax   = 64
)

// dragSteps is how many reports carry the pointer from one pixel position to another.
func dragSteps(x1, y1, x2, y2 int) int {
	dx, dy := x2-x1, y2-y1
	if dx < 0 {
		dx = -dx
	}
	if dy < 0 {
		dy = -dy
	}
	longest := dx
	if dy > longest {
		longest = dy
	}
	steps := (longest + dragStepPixels - 1) / dragStepPixels
	if steps < 2 {
		steps = 2
	}
	if steps > dragStepsMax {
		steps = dragStepsMax
	}
	return steps
}

// wait splits a duration into WAIT frames the board accepts.
func (r *Runner) wait(d time.Duration) []proto.Message {
	var msgs []proto.Message
	ms := int((d + time.Millisecond - 1) / time.Millisecond)
	for ms > 0 {
		step := ms
		if step > proto.MaxWaitMS {
			step = proto.MaxWaitMS
		}
		m, err := proto.Wait(step)
		if err != nil {
			panic(err) // step is within 1..MaxWaitMS by construction
		}
		msgs = append(msgs, m)
		ms -= step
	}
	return msgs
}

// send submits every frame, then waits for every acknowledgement, so a
// refusal is reported against the command that caused it.
func (r *Runner) send(ctx context.Context, msgs []proto.Message) error {
	if err := r.Session.Validate(msgs); err != nil {
		return err
	}
	pendings := make([]*link.Pending, 0, len(msgs))
	var first error
	for _, m := range msgs {
		p, err := r.Session.Submit(ctx, m)
		if err != nil {
			first = err
			break
		}
		pendings = append(pendings, p)
	}
	for _, p := range pendings {
		if _, err := p.Wait(ctx); err != nil && first == nil {
			first = err
		}
	}
	if first != nil && r.Session.Err() == nil {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, err := r.Session.SendContext(cleanup, proto.ReleaseAll())
		first = errors.Join(first, err)
	}
	return first
}

// typeText presses each character's key, holding Shift across a run of
// characters that need it rather than tapping it for each one.
func (r *Runner) typeText(ctx context.Context, text string) error {
	var msgs []proto.Message
	shift := false
	for _, ch := range text {
		k, ok := keymap.Char(ch)
		if !ok {
			return fmt.Errorf("%q cannot be typed on a US layout", ch)
		}
		if k.Shift != shift {
			if k.Shift {
				msgs = append(msgs, proto.KeyDown(keymap.UsageLeftShift))
			} else {
				msgs = append(msgs, proto.KeyUp(keymap.UsageLeftShift))
			}
			msgs = append(msgs, r.wait(r.Target.KeyInterval)...)
			shift = k.Shift
		}
		msgs = append(msgs, proto.KeyDown(k.Usage))
		msgs = append(msgs, r.wait(r.Target.KeyInterval)...)
		msgs = append(msgs, proto.KeyUp(k.Usage))
		msgs = append(msgs, r.wait(r.Target.KeyInterval)...)
	}
	if shift {
		msgs = append(msgs, proto.KeyUp(keymap.UsageLeftShift))
		msgs = append(msgs, r.wait(r.Target.KeyInterval)...)
	}
	return r.send(ctx, msgs)
}

// pixelsToUnits maps a pixel position on the configured screen onto the
// absolute pointer's 0..32767 range.
func (r *Runner) pixelsToUnits(x, y int) (uint16, uint16, error) {
	w, h := r.Target.Screen[0], r.Target.Screen[1]
	if w < 1 || h < 1 {
		return 0, 0, errors.New("target screen size is not configured")
	}
	if x >= w || y >= h {
		return 0, 0, fmt.Errorf("(%d,%d) is outside the %dx%d screen", x, y, w, h)
	}
	scale := func(v, dim int) uint16 {
		if dim == 1 {
			return 0
		}
		return uint16((v*proto.PointerMax + (dim-1)/2) / (dim - 1))
	}
	return scale(x, w), scale(y, h), nil
}

func (r *Runner) capture(ctx context.Context, label string) error {
	if err := r.Session.Sync(ctx); err != nil {
		return err
	}
	r.captures++
	if label == "" {
		label = fmt.Sprintf("frame-%d", r.captures)
	}
	path := capture.Path(r.CaptureDir, r.Name, label, r.captures, time.Now())
	if err := r.Grabber.Grab(ctx, path); err != nil {
		return err
	}
	abs := path
	if a, err := absPath(path); err == nil {
		abs = a
	}
	r.printf("captured %s", abs)
	return r.Log.Record("capture", map[string]any{"label": label, "path": abs, "n": r.captures})
}

func absPath(p string) (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(p, "/") {
		return p, nil
	}
	return wd + "/" + p, nil
}

func (r *Runner) expect(ctx context.Context, c *Command) error {
	if err := r.Session.Sync(ctx); err != nil {
		return err
	}
	st, err := r.Session.StatusContext(ctx)
	if err != nil {
		return err
	}
	var problems []string
	switch c.ExpectOf {
	case "leds":
		for name, want := range c.Expect {
			on := st.LEDs&ledBit(name) != 0
			if on != (want == "on") {
				problems = append(problems, fmt.Sprintf("%s LED is %s, expected %s", name, onOff(on), want))
			}
		}
	case "usb":
		if int(st.USBState) != usbState(c.Expect["state"]) {
			problems = append(problems, fmt.Sprintf("usb is %s, expected %s", proto.USBStateName(st.USBState), c.Expect["state"]))
		}
	case "held":
		held := st.Flags&proto.FlagHeld != 0
		if held != (c.Expect["held"] == "yes") {
			problems = append(problems, fmt.Sprintf("held is %v, expected %s", held, c.Expect["held"]))
		}
	case "protocol":
		for iface, want := range c.Expect {
			bit := proto.FlagKeyboardBoot
			if iface == "mouse" {
				bit = proto.FlagMouseBoot
			}
			got := "report"
			if st.Flags&bit != 0 {
				got = "boot"
			}
			if got != want {
				problems = append(problems, fmt.Sprintf("%s interface is in %s protocol, expected %s", iface, got, want))
			}
		}
	}
	if len(problems) > 0 {
		return &ExpectationError{Problems: problems}
	}
	r.printf("ok")
	return nil
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
