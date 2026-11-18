package sim

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/ptudor/accessibility-bridge/host/internal/frame"
	"github.com/ptudor/accessibility-bridge/host/internal/keymap"
	"github.com/ptudor/accessibility-bridge/host/internal/proto"
)

func TestHelloDiscardsPreviousQueue(t *testing.T) {
	b := New(Options{USBState: proto.USBConfigured})
	b.handle(frame.Frame{Type: proto.TypeHello, Body: []byte{proto.Version}}, nil)
	b.handle(frame.Frame{Seq: 1, Type: proto.TypeKeyDown, Body: []byte{keymap.UsageA}}, nil)
	gen := b.queueGen
	b.handle(frame.Frame{Seq: 42, Type: proto.TypeHello, Body: []byte{proto.Version}}, nil)
	if got := b.Snapshot(); got.Queue != 0 || got.Expected != 43 || gen == b.queueGen {
		t.Fatalf("new session inherited work: %+v", got)
	}
}

func TestDuplicateRetainsRefusalAndWakeupResult(t *testing.T) {
	for _, tc := range []struct {
		state    uint8
		message  proto.Message
		wantType uint8
		wantBody []byte
	}{
		{proto.USBAttached, proto.KeyDown(keymap.UsageA), proto.TypeNAK, []byte{proto.NAKState, 2}},
		{proto.USBSuspended, proto.Wakeup(), proto.TypeACK, []byte{1}},
	} {
		b := New(Options{USBState: tc.state, WakeupEnabled: true})
		var out bytes.Buffer
		b.w = &out
		b.handle(frame.Frame{Type: proto.TypeHello, Body: []byte{proto.Version}}, nil)
		f := frame.Frame{Seq: 1, Type: tc.message.Type, Body: tc.message.Body}
		b.handle(f, nil)
		out.Reset()
		b.handle(f, nil)
		var dec frame.Decoder
		dec.Push(out.Bytes(), func(got frame.Frame, err error) {
			if err != nil || got.Type != tc.wantType || !bytes.Equal(got.Body, tc.wantBody) {
				t.Fatalf("duplicate response: %+v, %v", got, err)
			}
		})
	}
}

func TestRunCancellationInterruptsRead(t *testing.T) {
	host, board := net.Pipe()
	defer host.Close()
	defer board.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- New(Options{}).Run(ctx, board) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not interrupt its blocked read")
	}
}

func TestInvalidIdentityReturnsError(t *testing.T) {
	b := New(Options{Identity: strings.Repeat("x", 65)})
	if err := b.Run(context.Background(), &bytes.Buffer{}); err == nil {
		t.Fatal("invalid identity accepted")
	}
}

func TestWatchdogCancelsActiveWaitWithoutHeldKeys(t *testing.T) {
	b := New(Options{})
	b.activeWait = true
	b.lastFrame = time.Now().Add(-time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); b.watchdog(ctx) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if b.Snapshot().Watchdogs == 1 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("active WAIT survived watchdog")
}

func TestClearedInputCannotExecute(t *testing.T) {
	for _, m := range []proto.Message{proto.KeyDown(keymap.UsageA), proto.Sync()} {
		b := New(Options{USBState: proto.USBConfigured})
		gen := b.queueGen
		b.clearQueueLocked()
		b.execute(context.Background(), queued{1, m}, gen)
		if b.Snapshot().Held || b.eventCounter != 0 {
			t.Fatalf("cleared %s executed", proto.TypeName(m.Type))
		}
	}
}

func TestLockLEDsFollowReportEdges(t *testing.T) {
	b := New(Options{USBState: proto.USBConfigured})
	for _, m := range []proto.Message{proto.KeyDown(keymap.UsageCapsLock), proto.KeyDown(keymap.UsageCapsLock)} {
		b.execute(context.Background(), queued{1, m}, b.queueGen)
	}
	if b.Snapshot().LEDs != proto.LEDCapsLock {
		t.Fatal("repeated down toggled the LED twice")
	}
	b.execute(context.Background(), queued{2, proto.KeyReport([8]byte{})}, b.queueGen)
	b.execute(context.Background(), queued{3, proto.KeyReport([8]byte{0, 0, keymap.UsageCapsLock})}, b.queueGen)
	if b.Snapshot().LEDs != 0 {
		t.Fatal("raw report did not toggle the LED")
	}
}
