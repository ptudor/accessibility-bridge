package link_test

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/ptudor/accessibility-bridge/host/internal/frame"
	"github.com/ptudor/accessibility-bridge/host/internal/keymap"
	"github.com/ptudor/accessibility-bridge/host/internal/link"
	"github.com/ptudor/accessibility-bridge/host/internal/linktest"
	"github.com/ptudor/accessibility-bridge/host/internal/proto"
	"github.com/ptudor/accessibility-bridge/host/internal/sim"
)

type rig struct {
	t       *testing.T
	port    *linktest.Port
	board   *sim.Board
	session *link.Session
	mu      sync.Mutex
	reports []sim.Report
	cancel  context.CancelFunc
	done    chan error
}

func (r *rig) collected() []sim.Report {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]sim.Report(nil), r.reports...)
}

func newRig(t *testing.T, boardOpts sim.Options, linkOpts link.Options, shape func([]byte) [][]byte) *rig {
	return newRigReplies(t, boardOpts, linkOpts, shape, nil)
}

type replyConn struct {
	net.Conn
	shape func([]byte) [][]byte
}

func (c replyConn) Write(b []byte) (int, error) {
	if c.shape == nil {
		return c.Conn.Write(b)
	}
	for _, out := range c.shape(b) {
		if _, err := c.Conn.Write(out); err != nil {
			return 0, err
		}
	}
	return len(b), nil
}

func newRigReplies(t *testing.T, boardOpts sim.Options, linkOpts link.Options, shape, replies func([]byte) [][]byte) *rig {
	t.Helper()
	r := &rig{t: t, done: make(chan error, 1)}
	boardOpts.Reports = func(rep sim.Report) {
		r.mu.Lock()
		r.reports = append(r.reports, rep)
		r.mu.Unlock()
	}
	if boardOpts.USBState == 0 && !boardOpts.WakeupEnabled {
		boardOpts.USBState = proto.USBConfigured
	}
	r.board = sim.New(boardOpts)
	var stream interface{}
	r.port, stream = linktest.Pair()
	conn := stream.(interface {
		Read([]byte) (int, error)
		Write([]byte) (int, error)
		Close() error
	})
	r.port.Shape = shape
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	go func() { r.done <- r.board.Run(ctx, replyConn{stream.(net.Conn), replies}) }()
	if linkOpts.AckTimeout == 0 {
		linkOpts.AckTimeout = 150 * time.Millisecond
	}
	s, err := link.Open(r.port, linkOpts)
	if err != nil {
		cancel()
		_ = conn.Close()
		t.Fatalf("open: %v", err)
	}
	r.session = s
	t.Cleanup(func() {
		_ = s.Close()
		cancel()
		_ = conn.Close()
		<-r.done
	})
	return r
}

func TestLostACKDoesNotSlideBeyondDuplicateWindow(t *testing.T) {
	dropped := false
	r := newRigReplies(t, sim.Options{}, link.Options{AckTimeout: 50 * time.Millisecond, Heartbeat: -1}, nil, func(w []byte) [][]byte {
		f, err := frame.DecodeWire(w[:len(w)-1])
		if err == nil && f.Type == proto.TypeACK && f.Seq == 1 && !dropped {
			dropped = true
			return nil
		}
		return [][]byte{w}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	// Exercise both window advancement and sequence wrap after a missing ACK.
	for i := 0; i < 300; i++ {
		m := proto.KeyDown(keymap.UsageA)
		if i%2 == 1 {
			m = proto.KeyUp(keymap.UsageA)
		}
		if _, err := r.session.Submit(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.session.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if len(r.collected()) != 300 {
		t.Fatalf("got %d reports", len(r.collected()))
	}
}

func TestLostRefusalStaysRefusedAndDoesNotHoldKey(t *testing.T) {
	dropped := false
	r := newRigReplies(t, sim.Options{USBState: proto.USBAttached}, link.Options{Heartbeat: -1}, nil, func(w []byte) [][]byte {
		f, _ := frame.DecodeWire(w[:len(w)-1])
		if f.Type == proto.TypeNAK && !dropped {
			dropped = true
			return nil
		}
		return [][]byte{w}
	})
	_, err := r.session.Send(proto.KeyDown(keymap.UsageA))
	var perr *proto.Error
	if !errors.As(err, &perr) || perr.Code != proto.NAKState {
		t.Fatalf("lost refusal became %v", err)
	}
	if r.session.Held().Any() {
		t.Fatal("refused key entered held model")
	}
}

func TestMissingDoneCannotHangSyncForever(t *testing.T) {
	r := newRigReplies(t, sim.Options{}, link.Options{AckTimeout: 25 * time.Millisecond, Retries: 1}, nil, func(w []byte) [][]byte {
		f, _ := frame.DecodeWire(w[:len(w)-1])
		if f.Type == proto.TypeEventDone {
			return nil
		}
		return [][]byte{w}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := r.session.Sync(ctx); !errors.Is(err, link.ErrSyncLost) {
		t.Fatalf("Sync: %v", err)
	}
}

func TestCancellationDoesNotSendInput(t *testing.T) {
	r := newRig(t, sim.Options{}, link.Options{}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.session.Submit(ctx, proto.KeyDown(keymap.UsageA)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := r.session.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.collected()) != 0 {
		t.Fatal("cancelled input was sent")
	}
}

func TestFailureClosesEventSubscription(t *testing.T) {
	r := newRig(t, sim.Options{}, link.Options{AckTimeout: 25 * time.Millisecond, Retries: 1, Heartbeat: -1}, nil)
	events, unsubscribe := r.session.Subscribe(1)
	defer unsubscribe()
	r.port.SetShape(func([]byte) [][]byte { return nil })
	_, _ = r.session.Send(proto.Status())
	select {
	case _, ok := <-events:
		if ok {
			t.Fatal("subscription still open")
		}
	case <-time.After(time.Second):
		t.Fatal("subscription blocked after link loss")
	}
}

func TestSlowSyncSurvivesHeartbeatSequenceWrap(t *testing.T) {
	r := newRig(t, sim.Options{}, link.Options{Heartbeat: time.Millisecond}, nil)
	wait, _ := proto.Wait(600)
	if _, err := r.session.Send(wait); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := r.session.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if r.board.Snapshot().Watchdogs != 0 {
		t.Fatal("watchdog fired while waiting for sequence reuse")
	}
}

func TestClearedQueueFailsWaitingSync(t *testing.T) {
	r := newRig(t, sim.Options{}, link.Options{}, nil)
	wait, _ := proto.Wait(1000)
	if _, err := r.session.Send(wait); err != nil {
		t.Fatal(err)
	}
	// WAIT must have left the queue before Queue > 0 can identify SYNC.
	// Otherwise RELEASE_ALL can win the race before SYNC has been submitted.
	waitFor(t, "active WAIT", func() bool { return r.board.Snapshot().Queue == 0 })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.session.Sync(ctx) }()
	waitFor(t, "queued SYNC", func() bool { return r.board.Snapshot().Queue > 0 })
	if _, err := r.session.Send(proto.ReleaseAll()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, link.ErrSyncLost) {
		t.Fatalf("cleared SYNC: %v", err)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestHelloStatusIdentity(t *testing.T) {
	r := newRig(t, sim.Options{FirmwareMajor: 0, FirmwareMinor: 3}, link.Options{}, nil)
	if h := r.session.Hello(); h.Version != 1 || h.FirmwareMinor != 3 || h.Queue != proto.QueueCapacity {
		t.Fatalf("hello = %+v", h)
	}
	st, err := r.session.Status()
	if err != nil || st.USBState != proto.USBConfigured || st.Flags&proto.FlagHeld != 0 {
		t.Fatalf("status = %+v, %v", st, err)
	}
	id, err := r.session.Identity()
	if err != nil || id != "accessibility-bridge R1 fw 0.3 sn SIMULATED" {
		t.Fatalf("identity = %q, %v", id, err)
	}
}

func TestTypingReachesTarget(t *testing.T) {
	r := newRig(t, sim.Options{}, link.Options{}, nil)
	ctx := context.Background()
	for _, m := range []proto.Message{
		proto.KeyDown(keymap.UsageLeftShift), proto.KeyDown(keymap.UsageA),
		proto.KeyUp(keymap.UsageA), proto.KeyUp(keymap.UsageLeftShift),
	} {
		if _, err := r.session.Submit(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.session.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	got := r.collected()
	want := [][8]byte{{0x02}, {0x02, 0, 0x04}, {0x02}, {}}
	if len(got) != len(want) {
		t.Fatalf("reports = %v", got)
	}
	for i := range want {
		if got[i].Interface != "keyboard" || got[i].Keyboard != want[i] {
			t.Fatalf("report %d = %+v, want %v", i, got[i], want[i])
		}
	}
	if r.session.Held().Any() {
		t.Fatal("host model still holds something")
	}
	st, _ := r.session.Status()
	if st.Flags&proto.FlagHeld != 0 || st.RxErrors != 0 {
		t.Fatalf("status after typing = %+v", st)
	}
}

func TestLossIsRetransmitted(t *testing.T) {
	r := newRig(t, sim.Options{}, link.Options{Heartbeat: -1}, nil)
	r.port.SetShape(linktest.DropFirst(1))
	start := time.Now()
	if _, err := r.session.Send(proto.KeyDown(keymap.UsageA)); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took < 100*time.Millisecond {
		t.Fatalf("acknowledged in %v, the first copy should have been lost", took)
	}
	if st := r.session.Stats(); st.Retransmits != 1 {
		t.Fatalf("stats = %+v", st)
	}
	if _, err := r.session.Send(proto.KeyUp(keymap.UsageA)); err != nil {
		t.Fatal(err)
	}
	if err := r.session.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(r.collected()); n != 2 {
		t.Fatalf("%d reports, want the press and the release", n)
	}
}

func TestCorruptionIsNAKedAndResent(t *testing.T) {
	r := newRig(t, sim.Options{}, link.Options{Heartbeat: -1}, nil)
	r.port.SetShape(linktest.CorruptFirst(1))
	start := time.Now()
	if _, err := r.session.Send(proto.KeyDown(keymap.UsageA)); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 100*time.Millisecond {
		t.Fatalf("took %v: a CRC NAK should trigger an immediate resend", took)
	}
	st := r.session.Stats()
	if st.Naks != 1 || st.Retransmits != 1 {
		t.Fatalf("stats = %+v", st)
	}
	if snap := r.board.Snapshot(); snap.RxErrors != 1 {
		t.Fatalf("board counted %d rx errors", snap.RxErrors)
	}
	if _, err := r.session.Send(proto.KeyUp(keymap.UsageA)); err != nil {
		t.Fatal(err)
	}
	if err := r.session.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(r.collected()); n != 2 {
		t.Fatalf("%d reports, want 2", n)
	}
}

func TestDuplicatesAreNotReplayed(t *testing.T) {
	r := newRig(t, sim.Options{}, link.Options{Heartbeat: -1}, nil)
	r.port.SetShape(linktest.Duplicate())
	ctx := context.Background()
	for i := 0; i < 20; i++ {
		if _, err := r.session.Submit(ctx, proto.KeyDown(keymap.UsageA)); err != nil {
			t.Fatal(err)
		}
		if _, err := r.session.Submit(ctx, proto.KeyUp(keymap.UsageA)); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.session.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(r.collected()); n != 40 {
		t.Fatalf("%d reports from 40 commands sent twice each", n)
	}
	if st := r.session.Stats(); st.Naks != 0 || st.Retransmits != 0 {
		t.Fatalf("stats = %+v", st)
	}
	if id, err := r.session.Identity(); err != nil || id == "" {
		t.Fatal(err)
	}
}

func TestQueueFullIsRetried(t *testing.T) {
	r := newRig(t, sim.Options{}, link.Options{Heartbeat: -1}, nil)
	ctx := context.Background()
	wait, _ := proto.Wait(30)
	// 40 waits of 30 ms fill the 16-entry queue several times over.
	for i := 0; i < 40; i++ {
		if _, err := r.session.Submit(ctx, wait); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.session.Submit(ctx, proto.KeyDown(keymap.UsageA)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.session.Submit(ctx, proto.KeyUp(keymap.UsageA)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := r.session.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	st := r.session.Stats()
	if st.QueueFull == 0 {
		t.Fatalf("the board never reported a full queue: %+v", st)
	}
	if n := len(r.collected()); n != 2 {
		t.Fatalf("%d reports, want 2 after the waits", n)
	}
}

func TestWatchdogReleasesHeldKeys(t *testing.T) {
	r := newRig(t, sim.Options{}, link.Options{Heartbeat: -1}, nil)
	events, unsubscribe := r.session.Subscribe(16)
	defer unsubscribe()
	if _, err := r.session.Send(proto.KeyDown(keymap.UsageLeftShift)); err != nil {
		t.Fatal(err)
	}
	if err := r.session.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !r.session.Held().Any() {
		t.Fatal("host model does not know shift is held")
	}
	// No heartbeat and nothing sent: the board must let go within 500 ms.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case e := <-events:
			if e.Type == proto.TypeEventReleased && e.Reason == proto.ReleasedWatchdog {
				goto released
			}
		case <-deadline:
			t.Fatal("no watchdog release")
		}
	}
released:
	waitFor(t, "host model cleared", func() bool { return !r.session.Held().Any() })
	snap := r.board.Snapshot()
	if snap.Held || snap.Watchdogs != 1 {
		t.Fatalf("board = %+v", snap)
	}
	reports := r.collected()
	if len(reports) != 2 || reports[1].Keyboard != [8]byte{} {
		t.Fatalf("reports = %v, want a press then a release", reports)
	}
}

func TestHeartbeatKeepsKeysHeld(t *testing.T) {
	r := newRig(t, sim.Options{}, link.Options{}, nil)
	if _, err := r.session.Send(proto.KeyDown(keymap.UsageLeftCtrl)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(800 * time.Millisecond)
	if snap := r.board.Snapshot(); !snap.Held || snap.Watchdogs != 0 {
		t.Fatalf("board = %+v, the heartbeat should have kept control held", snap)
	}
}

func TestInputRefusedUntilConfigured(t *testing.T) {
	r := newRig(t, sim.Options{USBState: proto.USBAttached}, link.Options{}, nil)
	_, err := r.session.Send(proto.KeyDown(keymap.UsageA))
	var perr *proto.Error
	if !errors.As(err, &perr) || perr.Code != proto.NAKState {
		t.Fatalf("err = %v, want NAK STATE", err)
	}
	// STATUS, WAIT and SYNC are fine while unconfigured.
	if _, err = r.session.Status(); err != nil {
		t.Fatal(err)
	}
	if err = r.session.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	events, unsubscribe := r.session.Subscribe(16)
	defer unsubscribe()
	r.board.SetUSB(proto.USBConfigured, proto.CauseConfigured)
	select {
	case e := <-events:
		if e.Type != proto.TypeEventUSB || e.USBState != proto.USBConfigured {
			t.Fatalf("event = %v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("no USB event")
	}
	if _, err = r.session.Send(proto.KeyDown(keymap.UsageA)); err != nil {
		t.Fatal(err)
	}
}

func TestSuspendWakesOnInput(t *testing.T) {
	r := newRig(t, sim.Options{USBState: proto.USBConfigured, WakeupEnabled: true}, link.Options{}, nil)
	events, unsubscribe := r.session.Subscribe(16)
	defer unsubscribe()
	r.board.SetUSB(proto.USBSuspended, proto.CauseSuspend)
	if _, err := r.session.Send(proto.KeyDown(keymap.UsageA)); err != nil {
		t.Fatal(err)
	}
	if err := r.session.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	woke := false
	for len(events) > 0 {
		if e := <-events; e.Type == proto.TypeEventUSB && e.Cause == proto.CauseRemoteWakeup {
			woke = true
		}
	}
	if !woke {
		t.Fatal("no remote wakeup event")
	}
	if snap := r.board.Snapshot(); snap.USB != proto.USBConfigured || !snap.Held {
		t.Fatalf("board = %+v", snap)
	}
}

func TestRemoteWakeupTimeoutDiscardsInput(t *testing.T) {
	r := newRig(t, sim.Options{USBState: proto.USBSuspended, WakeupEnabled: true, ManualResume: true}, link.Options{}, nil)
	if _, err := r.session.Send(proto.KeyDown(keymap.UsageA)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if err := r.session.Sync(ctx); !errors.Is(err, link.ErrSyncLost) {
		t.Fatalf("timeout: %v", err)
	}
	if got := r.board.Snapshot(); got.Held || got.Queue != 0 || got.USB != proto.USBSuspended {
		t.Fatalf("state: %+v", got)
	}
	if len(r.collected()) != 0 {
		t.Fatal("input reached a sleeping target")
	}
}

func TestRemoteWakeupWaitsForTarget(t *testing.T) {
	r := newRig(t, sim.Options{USBState: proto.USBSuspended, WakeupEnabled: true, ManualResume: true}, link.Options{}, nil)
	if _, err := r.session.Send(proto.KeyDown(keymap.UsageA)); err != nil {
		t.Fatal(err)
	}
	if len(r.collected()) != 0 {
		t.Fatal("input reached a sleeping target")
	}
	r.board.SetUSB(proto.USBConfigured, proto.CauseResume)
	if err := r.session.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.collected()) != 1 {
		t.Fatal("resumed target did not receive input")
	}
}

func TestLinkLostAfterRetries(t *testing.T) {
	r := newRig(t, sim.Options{}, link.Options{AckTimeout: 50 * time.Millisecond, Retries: 2, Heartbeat: -1}, nil)
	r.port.SetShape(func([]byte) [][]byte { return nil })
	_, err := r.session.Send(proto.KeyDown(keymap.UsageA))
	if !errors.Is(err, link.ErrLinkLost) {
		t.Fatalf("err = %v, want link lost", err)
	}
	if st := r.session.Stats(); st.Retransmits != 2 {
		t.Fatalf("stats = %+v", st)
	}
	if _, err = r.session.Send(proto.Status()); !errors.Is(err, link.ErrLinkLost) {
		t.Fatalf("after loss err = %v", err)
	}
}

func TestSeventhKeyRefused(t *testing.T) {
	r := newRig(t, sim.Options{}, link.Options{}, nil)
	for u := uint8(keymap.UsageA); u < keymap.UsageA+6; u++ {
		if _, err := r.session.Send(proto.KeyDown(u)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.session.Send(proto.KeyDown(keymap.UsageA + 6)); !errors.Is(err, link.ErrTooManyKeys) {
		t.Fatalf("err = %v", err)
	}
	if _, err := r.session.Send(proto.KeyDown(keymap.UsageLeftShift)); err != nil {
		t.Fatalf("modifiers do not use key slots: %v", err)
	}
	if held := r.session.Held(); held.KeyCount() != 6 || held.Modifiers != 0x02 {
		t.Fatalf("held = %+v", held)
	}
}

func TestCloseReleasesEverything(t *testing.T) {
	r := newRig(t, sim.Options{}, link.Options{}, nil)
	if _, err := r.session.Send(proto.Pointer(proto.ButtonLeft, 100, 200, 0)); err != nil {
		t.Fatal(err)
	}
	if err := r.session.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if snap := r.board.Snapshot(); !snap.Held {
		t.Fatal("button not held")
	}
	if err := r.session.Close(); err != nil {
		t.Fatal(err)
	}
	if snap := r.board.Snapshot(); snap.Held || snap.Watchdogs != 0 {
		t.Fatalf("board after close = %+v", snap)
	}
	if _, err := r.session.Send(proto.Status()); !errors.Is(err, link.ErrClosed) {
		t.Fatalf("send after close err = %v", err)
	}
}

func TestBoardResetIsDetected(t *testing.T) {
	r := newRig(t, sim.Options{}, link.Options{Heartbeat: -1}, nil)
	if _, err := r.session.Send(proto.Status()); err != nil {
		t.Fatal(err)
	}
	// Another HELLO on the same stream, as if the board had reset and a
	// second host taken it, moves the board's expected sequence away from
	// ours. The next command must fail cleanly, not hang or look replayed.
	wire, err := frame.Encode(frame.Frame{Seq: 200, Type: proto.TypeHello, Body: []byte{proto.Version}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.port.Write(wire); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if _, err = r.session.Send(proto.Status()); !errors.Is(err, link.ErrDesync) {
		t.Fatalf("err = %v, want desync", err)
	}
}
