package proto

import (
	"bytes"
	"testing"

	"github.com/ptudor/accessibility-bridge/host/internal/frame"
)

func TestBodyLengthsMatchConstructors(t *testing.T) {
	wait, _ := Wait(10000)
	for _, m := range []Message{
		Hello(), Status(), Identity(), KeyDown(4), KeyUp(4), KeyReport([8]byte{}),
		Mouse(1, -127, 127, 0), Pointer(0, 32767, 0, -1), Consumer(0xE9, true),
		ReleaseAll(), wait, Sync(), Heartbeat(), USBDetach(), USBAttach(), Wakeup(), Bootloader(),
	} {
		want, ok := BodyLength[m.Type]
		if !ok || len(m.Body) != want {
			t.Errorf("%s body is %d bytes, table says %d (%v)", TypeName(m.Type), len(m.Body), want, ok)
		}
	}
	if _, err := Wait(0); err == nil {
		t.Error("accepted a zero wait")
	}
	if _, err := Wait(MaxWaitMS + 1); err == nil {
		t.Error("accepted a wait over the limit")
	}
}

func TestLayouts(t *testing.T) {
	p := Pointer(ButtonLeft|ButtonMiddle, 0x1234, 0x7FFF, -3)
	if !bytes.Equal(p.Body, []byte{0x05, 0x34, 0x12, 0xFF, 0x7F, 0xFD}) {
		t.Errorf("pointer body = % x", p.Body)
	}
	c := Consumer(0x00E9, true)
	if !bytes.Equal(c.Body, []byte{0xE9, 0x00, 0x01}) {
		t.Errorf("consumer body = % x", c.Body)
	}
	if ClampI8(300) != 127 || ClampI8(-300) != -127 || ClampI8(-128) != -127 || ClampI8(5) != 5 {
		t.Error("ClampI8 is wrong")
	}
	s := StatusInfo{USBState: USBConfigured, Flags: FlagHeld | FlagWakeupEnabled, LEDs: LEDCapsLock, Queue: 3,
		UptimeSeconds: 0x01020304, Watchdog: 7, RxErrors: 0x0102, Dropped: 1}
	back, err := ParseStatus(s.Encode())
	if err != nil || back != s {
		t.Fatalf("status round trip = %+v, %v", back, err)
	}
	if _, err = ParseStatus(make([]byte, 13)); err == nil {
		t.Error("accepted a short status")
	}
	h, err := ParseHello([]byte{1, 0, 1, 16})
	if err != nil || h != (HelloInfo{1, 0, 1, 16}) {
		t.Fatalf("hello = %+v, %v", h, err)
	}
	if _, err = ParseHello([]byte{2, 0, 1, 16}); err == nil {
		t.Error("accepted a foreign protocol version")
	}
	if _, err = ParseIdentity([]byte("accessibility-bridge R1 fw 0.1 sn HIDBRIDGE0001")); err != nil {
		t.Error(err)
	}
	if _, err = ParseIdentity([]byte{0x7F}); err == nil {
		t.Error("accepted a non-printable identity")
	}
}

func TestEvents(t *testing.T) {
	events := []Event{
		{Counter: 1, Type: TypeEventUSB, USBState: USBSuspended, Cause: CauseSuspend},
		{Counter: 2, Type: TypeEventProtocol, Interface: InterfaceMouse, Protocol: 1},
		{Counter: 3, Type: TypeEventLEDs, LEDs: LEDNumLock | LEDScrollLock},
		{Counter: 4, Type: TypeEventWakeup, Enabled: true},
		{Counter: 5, Type: TypeEventDone, DoneSeq: 200},
		{Counter: 6, Type: TypeEventReleased, Reason: ReleasedWatchdog},
		{Counter: 7, Type: TypeEventHeld, Held: true},
	}
	for _, e := range events {
		back, err := ParseEvent(e.Encode())
		if err != nil || back != e {
			t.Errorf("event %s round trip = %+v, %v", TypeName(e.Type), back, err)
		}
		if back.String() == "" {
			t.Errorf("event %s has no rendering", TypeName(e.Type))
		}
	}
	if _, err := ParseEvent(frame.Frame{Type: 0x9F}); err == nil {
		t.Error("accepted an unknown event")
	}
	if _, err := ParseEvent(frame.Frame{Type: TypeEventLEDs, Body: []byte{1, 2}}); err == nil {
		t.Error("accepted a wrong-length event")
	}
	if got := LEDNames(LEDNumLock | LEDCapsLock); got != "num caps" {
		t.Errorf("LEDNames = %q", got)
	}
	if got := (&Error{Type: TypeKeyDown, Seq: 9, Code: NAKState}).Error(); got != "board refused KEY_DOWN (seq 9): STATE" {
		t.Errorf("Error = %q", got)
	}
}
