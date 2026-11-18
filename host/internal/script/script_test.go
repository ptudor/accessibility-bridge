package script_test

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ptudor/accessibility-bridge/host/internal/link"
	"github.com/ptudor/accessibility-bridge/host/internal/linktest"
	"github.com/ptudor/accessibility-bridge/host/internal/proto"
	"github.com/ptudor/accessibility-bridge/host/internal/script"
	"github.com/ptudor/accessibility-bridge/host/internal/sim"
)

func TestParseLine(t *testing.T) {
	good := []string{
		`type "Hello, world!\n"`, `key ctrl+alt+delete`, `key enter 100ms`, `down shift a`, `up a shift`,
		`report 0200040000000000`, `move 10 -5`, `move 10,-5`, `pointer 16383 16383`, `click 100 200`,
		`click 100,200 right double`, `hover 10 20`, `hover 10,20`, `drag 10 20 300 400`, `drag 10,20 300,400 right`,
		`button left down`, `button m click`, `scroll -3`, `consumer volume_up`,
		`consumer mute 250ms`, `wait 500ms`, `wait 500`, `sleep 1.5s`, `sync`, `release`, `capture after-login`,
		`capture`, `expect leds caps=on num=off`, `expect usb configured`, `expect held no`,
		`expect protocol keyboard boot`, `echo "a note" here`, `detach`, `attach`, `wake`, `   # comment only`, ``,
	}
	for _, line := range good {
		if _, err := script.ParseLine(line, 1); err != nil {
			t.Errorf("%q: %v", line, err)
		}
	}
	bad := []string{
		`type "é"`, `type`, `type a b`, `key`, `key ctrl+`, `key enter soon`, `down`, `report 0102`, `move 10`,
		`move x y`, `pointer 40000 0`, `click -1 5`, `click 1 2 sideways`, `hover -1 5`, `hover 1 2 3`,
		`drag 1 2`, `drag 1 2 1 2`, `drag 1 2 3 4 sideways`, `drag -1 2 3 4`, `button left sideways`, `scroll 0`,
		`consumer teleport`, `wait 0`, `wait -5`, `wait 2h`, `sync now`, `expect`, `expect leds caps=maybe`,
		`expect usb orbiting`, `expect held perhaps`, `expect protocol keyboard fast`, `expect mood good`,
		`fly away`, `type "unterminated`,
	}
	for _, line := range bad {
		if _, err := script.ParseLine(line, 1); err == nil {
			t.Errorf("%q was accepted", line)
		}
	}
	c, err := script.ParseLine(`click 100,200 right double # comment`, 7)
	if err != nil || c.Kind != script.KindClick || c.X != 100 || c.Y != 200 || c.Button != proto.ButtonRight || !c.Double || c.Line != 7 {
		t.Fatalf("click = %+v, %v", c, err)
	}
	if c, err = script.ParseLine(`wait 250`, 1); err != nil || c.Dur != 250*time.Millisecond {
		t.Fatalf("wait = %+v, %v", c, err)
	}
	if _, err = script.ParseAll(strings.NewReader("key a\nfly\ntype \"x\"\nmove 1\n")); err == nil || !strings.Contains(err.Error(), "line 2") || !strings.Contains(err.Error(), "line 4") {
		t.Fatalf("ParseAll err = %v", err)
	}
}

// syncBuffer is a bytes.Buffer the event goroutine and the test may share.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type rig struct {
	mu      sync.Mutex
	reports []sim.Report
	board   *sim.Board
	session *link.Session
	runner  *script.Runner
	out     syncBuffer
}

func newRig(t *testing.T, opts sim.Options) *rig {
	t.Helper()
	r := &rig{}
	opts.Reports = func(rep sim.Report) {
		r.mu.Lock()
		r.reports = append(r.reports, rep)
		r.mu.Unlock()
	}
	if opts.USBState == 0 {
		opts.USBState = proto.USBConfigured
	}
	r.board = sim.New(opts)
	port, stream := linktest.Pair()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.board.Run(ctx, stream) }()
	s, err := link.Open(port, link.Options{AckTimeout: 150 * time.Millisecond})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	r.session = s
	r.runner = &script.Runner{Session: s, Target: script.Target{Screen: [2]int{1920, 1080}, KeyInterval: 2 * time.Millisecond, ClickHold: 2 * time.Millisecond}, Out: &r.out, ShowEvents: true, Name: "test"}
	r.runner.Start()
	t.Cleanup(func() {
		r.runner.Stop()
		_ = s.Close()
		cancel()
		_ = stream.Close()
		<-done
	})
	return r
}

func (r *rig) run(t *testing.T, text string) error {
	t.Helper()
	cmds, err := script.ParseAll(strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	return r.runner.RunAll(context.Background(), cmds)
}

func (r *rig) keyboard() [][8]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out [][8]byte
	for _, rep := range r.reports {
		if rep.Interface == "keyboard" {
			out = append(out, rep.Keyboard)
		}
	}
	return out
}

func TestTypingHoldsShiftAcrossARun(t *testing.T) {
	r := newRig(t, sim.Options{})
	if err := r.run(t, "type \"Hi!\"\nsync\n"); err != nil {
		t.Fatal(err)
	}
	want := [][8]byte{
		{0x02}, {0x02, 0, 0x0B}, {0x02}, // shift down, H down, H up
		{}, {0, 0, 0x0C}, {}, // shift up, i down, i up
		{0x02}, {0x02, 0, 0x1E}, {0x02}, {}, // shift down, 1 down (= !), 1 up, shift up
	}
	got := r.keyboard()
	if len(got) != len(want) {
		t.Fatalf("reports = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("report %d = %v, want %v (all: %v)", i, got[i], want[i], got)
		}
	}
	if err := r.run(t, "expect held no\n"); err != nil {
		t.Fatal(err)
	}
}

func TestChordAndExpectLEDs(t *testing.T) {
	r := newRig(t, sim.Options{})
	err := r.run(t, "key ctrl+alt+delete\nkey capslock\nexpect leds caps=on num=off\nkey capslock\nexpect leds caps=off\n")
	if err != nil {
		t.Fatal(err)
	}
	got := r.keyboard()
	if len(got) < 6 || got[0] != [8]byte{0x01} || got[1] != [8]byte{0x05} || got[2] != [8]byte{0x05, 0, 0x4C} ||
		got[3] != [8]byte{0x05} || got[4] != [8]byte{0x01} || got[5] != [8]byte{} {
		t.Fatalf("chord reports = %v", got[:6])
	}
	if err = r.run(t, "expect leds caps=on\n"); err == nil || !strings.Contains(err.Error(), "caps LED is off") {
		t.Fatalf("expect should fail: %v", err)
	}
	if !strings.Contains(r.out.String(), "event: leds caps") {
		t.Fatalf("trace lacks the LED event:\n%s", r.out.String())
	}
}

func TestHoverAndDrag(t *testing.T) {
	r := newRig(t, sim.Options{})
	if err := r.run(t, "hover 1919 0\ndrag 100 100 300 500 right\nsync\nexpect held no\n"); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var pointer []sim.Report
	for _, rep := range r.reports {
		if rep.Interface == "pointer" {
			pointer = append(pointer, rep)
		}
	}
	// hover: one unpressed report at the top-right corner
	if len(pointer) < 5 || pointer[0].X != 32767 || pointer[0].Y != 0 || pointer[0].Buttons != 0 {
		t.Fatalf("hover report = %v", pointer[:1])
	}
	// drag: arrive unpressed, press at the start, move pressed, release at the end
	arrive, press, last := pointer[1], pointer[2], pointer[len(pointer)-1]
	x1, y1 := uint16((100*32767+959)/1919), uint16((100*32767+539)/1079)
	x2, y2 := uint16((300*32767+959)/1919), uint16((500*32767+539)/1079)
	if arrive.Buttons != 0 || arrive.X != x1 || arrive.Y != y1 || press.Buttons != proto.ButtonRight || press.X != x1 {
		t.Fatalf("drag start = %v %v", arrive, press)
	}
	if last.Buttons != 0 || last.X != x2 || last.Y != y2 {
		t.Fatalf("drag end = %v", last)
	}
	moves := 0
	for _, rep := range pointer[3 : len(pointer)-1] {
		if rep.Buttons != proto.ButtonRight {
			t.Fatalf("button released mid-drag: %v", rep)
		}
		moves++
	}
	if moves < 10 {
		t.Fatalf("a 400 pixel drag moved in %d steps", moves)
	}
}

func TestPointerAndMouse(t *testing.T) {
	r := newRig(t, sim.Options{})
	if err := r.run(t, "click 1919 1079\nclick 0 0 right\nmove 300 -5\nbutton left click\nscroll -130\nsync\n"); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var pointer, mouse []sim.Report
	for _, rep := range r.reports {
		switch rep.Interface {
		case "pointer":
			pointer = append(pointer, rep)
		case "mouse":
			mouse = append(mouse, rep)
		}
	}
	if len(pointer) != 6 || pointer[0].X != 32767 || pointer[0].Y != 32767 || pointer[1].Buttons != proto.ButtonLeft ||
		pointer[2].Buttons != 0 || pointer[3].X != 0 || pointer[4].Buttons != proto.ButtonRight {
		t.Fatalf("pointer reports = %v", pointer)
	}
	// move 300,-5 is three frames (127, 127, 46), then click down/up, then
	// scroll -127 and -3.
	if len(mouse) != 7 || mouse[0].Mouse != [4]byte{0, 127, 0xFB, 0} || mouse[2].Mouse != [4]byte{0, 46, 0, 0} ||
		mouse[3].Mouse != [4]byte{1, 0, 0, 0} || mouse[4].Mouse != [4]byte{0, 0, 0, 0} ||
		mouse[5].Mouse != [4]byte{0, 0, 0, 0x81} || mouse[6].Mouse != [4]byte{0, 0, 0, 0xFD} {
		t.Fatalf("mouse reports = %v", mouse)
	}
	if err := r.run(t, "click 1920 10\n"); err == nil {
		t.Fatal("click outside the screen accepted")
	}
}

func TestRefusalNamesTheLine(t *testing.T) {
	r := newRig(t, sim.Options{USBState: proto.USBAttached})
	err := r.run(t, "sync\nkey a\n")
	if err == nil || !strings.Contains(err.Error(), "line 2") || !strings.Contains(err.Error(), "STATE") {
		t.Fatalf("err = %v", err)
	}
	if err = r.run(t, "expect usb attached\nexpect usb configured\n"); err == nil || !strings.Contains(err.Error(), "usb is attached") {
		t.Fatalf("err = %v", err)
	}
}

func TestStreamContinuesPastBadLines(t *testing.T) {
	r := newRig(t, sim.Options{})
	in := strings.NewReader("key a\nnonsense here\ntype \"b\"\nexpect usb suspended\nsync\n")
	if err := r.runner.Stream(context.Background(), in); err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("stream did not retain its error: %v", err)
	}
	got := r.keyboard()
	if len(got) != 4 {
		t.Fatalf("reports = %v", got)
	}
	out := r.out.String()
	if !strings.Contains(out, "error: line 2") || !strings.Contains(out, "usb is configured, expected suspended") {
		t.Fatalf("trace:\n%s", out)
	}
}

func TestStreamEOFWaitsForQueuedTyping(t *testing.T) {
	r := newRig(t, sim.Options{})
	if err := r.runner.Stream(context.Background(), strings.NewReader("wait 50ms\ntype \"abc\"")); err != nil {
		t.Fatal(err)
	}
	if got := r.keyboard(); len(got) != 6 {
		t.Fatalf("EOF returned before typing finished: %v", got)
	}
}

func TestTooManyKeysDoesNotSendPartialCommand(t *testing.T) {
	r := newRig(t, sim.Options{})
	if err := r.run(t, "down a b c d e f g"); err == nil {
		t.Fatal("accepted seven keys")
	}
	if err := r.session.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.keyboard()) != 0 {
		t.Fatal("sent a partial chord")
	}
}

func TestDurationsAreBoundedForEveryVerb(t *testing.T) {
	for _, line := range []string{"key a 2h", "consumer mute 2h", "wait 0.000000001"} {
		if _, err := script.ParseLine(line, 1); err == nil {
			t.Errorf("accepted %s", line)
		}
	}
}

func TestWaitSplitsAndCaptureNeedsConfiguration(t *testing.T) {
	r := newRig(t, sim.Options{})
	if err := r.run(t, "wait 25s\nrelease\n"); err != nil {
		t.Fatal(err)
	}
	if st := r.session.Stats(); st.Sent < 5 {
		t.Fatalf("25 s should take three WAIT frames: %+v", st)
	}
	if err := r.run(t, "capture x\n"); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("err = %v", err)
	}
}
