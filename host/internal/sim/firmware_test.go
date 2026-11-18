package sim

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ptudor/accessibility-bridge/host/internal/frame"
	"github.com/ptudor/accessibility-bridge/host/internal/proto"
)

type firmwareWriter struct{ lines *[]string }

func (w firmwareWriter) Write(p []byte) (int, error) {
	*w.lines = append(*w.lines, fmt.Sprintf("W %x", p))
	return len(p), nil
}

// This invokes the same C protocol/executor sources linked into the RP2040 image.
// Go remains an independent oracle for bytes, refusals, state and HID reports.
func TestFirmwareMatchesSimulator(t *testing.T) {
	path := os.Getenv("HIDBRIDGE_FIRMWARE_TEST")
	if path == "" {
		t.Skip("run make test in ../firmware to build the native C executable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	identityBytes, err := exec.CommandContext(ctx, path, "--identity").Output()
	if err != nil {
		t.Fatal(err)
	}
	identity := strings.TrimSpace(string(identityBytes))
	if _, err := proto.ParseIdentity([]byte(identity)); err != nil {
		t.Fatalf("firmware identity: %v", err)
	}
	fields := strings.Fields(identity)
	if len(fields) < 6 || fields[2] != "fw" {
		t.Fatalf("firmware identity has no version: %q", identity)
	}
	var firmwareMajor, firmwareMinor uint8
	if _, err := fmt.Sscanf(fields[3], "%d.%d", &firmwareMajor, &firmwareMinor); err != nil {
		t.Fatalf("firmware identity version: %v", err)
	}
	cmd := exec.CommandContext(ctx, path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		if err := cmd.Wait(); err != nil {
			t.Errorf("firmware process: %v", err)
		}
	})
	scanner := bufio.NewScanner(stdout)
	var expected []string
	b := New(Options{
		USBState: proto.USBConfigured, ManualResume: true,
		FirmwareMajor: firmwareMajor, FirmwareMinor: firmwareMinor,
		Identity: identity,
		Reports: func(r Report) {
			var kind int
			var data []byte
			switch r.Interface {
			case "keyboard":
				kind, data = 0, r.Keyboard[:]
			case "mouse":
				kind, data = 1, r.Mouse[:]
			case "pointer":
				kind, data = 2, []byte{1, r.Buttons, 0, 0, 0, 0, byte(r.Wheel)}
				binary.LittleEndian.PutUint16(data[2:], r.X)
				binary.LittleEndian.PutUint16(data[4:], r.Y)
			case "consumer":
				kind, data = 3, []byte{2, byte(r.Consumer), byte(r.Consumer >> 8)}
			default:
				t.Fatalf("unknown report: %+v", r)
			}
			expected = append(expected, fmt.Sprintf("R %d %x", kind, data))
		},
	})
	// HID reset defaults to report protocol; the old simulator defaults to boot.
	b.kbdBoot, b.mouseBoot = false, false
	b.w = firmwareWriter{&expected}
	step := func(line string, reference func()) {
		t.Helper()
		expected = nil
		b.start = time.Now() // Tests compare uptime=0; C uses an explicit clock.
		reference()
		if _, err := fmt.Fprintln(stdin, line); err != nil {
			t.Fatal(err)
		}
		var actual []string
		for scanner.Scan() {
			if scanner.Text() == "OK" {
				if !reflect.DeepEqual(actual, expected) {
					t.Fatalf("%s\nC:  %v\nGo: %v", line, actual, expected)
				}
				return
			}
			actual = append(actual, scanner.Text())
		}
		t.Fatalf("firmware stopped at %s: %v", line, scanner.Err())
	}
	var decoder frame.Decoder
	wire := func(data []byte) {
		t.Helper()
		step(fmt.Sprintf("wire %x", data), func() { decoder.Push(data, b.handle) })
	}
	send := func(seq, kind uint8, body ...byte) {
		t.Helper()
		data, err := frame.Encode(frame.Frame{Seq: seq, Type: kind, Body: body})
		if err != nil {
			t.Fatal(err)
		}
		wire(data)
	}
	tick := func() {
		t.Helper()
		step("tick 0", func() {
			if len(b.queue) == 0 {
				return
			}
			q := b.queue[0]
			b.queue = b.queue[1:]
			b.execute(context.Background(), q, b.queueGen)
		})
	}
	send(0, proto.TypeStatus)
	send(250, proto.TypeHello, 1)
	// Sequence wrap, each retained refusal, and a retransmitted successful input.
	send(251, 0x77)
	send(251, proto.TypeKeyDown, 4)
	send(252, proto.TypeMouse)
	send(252, proto.TypeHeartbeat)
	send(253, proto.TypeWait, 0, 0)
	send(253, proto.TypeHeartbeat)
	send(254, proto.TypeKeyDown, 4)
	tick()
	send(254, proto.TypeKeyDown, 4)
	send(255, proto.TypeKeyUp, 4)
	tick()
	send(0, proto.TypeIdentity)
	// Full queue refusals do not consume a sequence, then retry after one slot drains.
	for i := 0; i < 17; i++ {
		send(b.expected, proto.TypeSync)
	}
	tick()
	send(b.expected, proto.TypeSync)
	for len(b.queue) > 0 {
		tick()
	}
	// Known, independently encoded malformed streams followed by a valid frame.
	wire([]byte{0, 5, 1, 0})
	wire([]byte{5, 1, 2, 3, 4, 0})
	oversized := make([]byte, 100)
	for i := 0; i < 99; i++ {
		oversized[i] = 1
	}
	wire(oversized)
	send(b.expected, proto.TypeStatus)
	// Pseudorandom mixes exercise held reports, rollover, duplicates and stale frames.
	rng := rand.New(rand.NewSource(0x32a4))
	for i := 0; i < 1800; i++ {
		seq := b.expected
		if i%11 == 0 {
			seq -= uint8(rng.Intn(10) + 1)
		}
		switch rng.Intn(12) {
		case 0:
			send(seq, proto.TypeKeyDown, byte(4+rng.Intn(32)))
		case 1:
			send(seq, proto.TypeKeyUp, byte(4+rng.Intn(32)))
		case 2:
			send(seq, proto.TypeKeyDown, byte(0xe0+rng.Intn(8)))
		case 3:
			send(seq, proto.TypeKeyReport, byte(rng.Intn(256)), 0xff, 4, 5, 6, 7, 8, 9)
		case 4:
			send(seq, proto.TypeMouse, byte(rng.Intn(256)), 128, 127, byte(rng.Intn(256)))
		case 5:
			send(seq, proto.TypePointer, 7, byte(rng.Intn(256)), 127, 1, 0, 255)
		case 6:
			send(seq, proto.TypeConsumer, 0xe9, 0, byte(rng.Intn(2)))
		case 7:
			send(seq, proto.TypeReleaseAll)
		case 8:
			send(seq, proto.TypeSync)
		case 9:
			send(seq, proto.TypeStatus)
		case 10:
			send(seq, proto.TypeHeartbeat)
		case 11:
			send(seq, proto.TypeMouse, 1) // Retained LENGTH refusal.
		}
		if i%3 != 0 {
			tick()
		}
	}
	step("leds 5", func() { b.SetLEDs(5) })
	step("protocol 0 0", func() { b.SetProtocol(0, 0) })
	send(b.expected, proto.TypeStatus)
	step("usb 3 3", func() { b.SetUSB(proto.USBSuspended, proto.CauseSuspend) })
	send(b.expected, proto.TypeKeyDown, 4)
	step("wakeup 1", func() { b.SetWakeupEnabled(true) })
	send(b.expected, proto.TypeWakeup)
	wakeSeq := b.expected - 1
	step("usb 2 4", func() { b.SetUSB(proto.USBConfigured, proto.CauseResume) })
	send(wakeSeq, proto.TypeWakeup) // Original issued=1, even after resume.
	send(b.expected, proto.TypeReleaseAll)
	send(b.expected, proto.TypeStatus)
}
