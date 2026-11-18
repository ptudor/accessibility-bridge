package capture

import (
	"context"
	"errors"
	"fmt"
	"image/png"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Opt-in real FFmpeg coverage: two simultaneous publishers, each available
// through RTSP and RTMP. All listeners bind to loopback; no camera is needed.
func TestNetworkSnapshots(t *testing.T) {
	server := os.Getenv("HIDBRIDGE_TEST_MEDIAMTX")
	if server == "" {
		t.Skip("set HIDBRIDGE_TEST_MEDIAMTX to run real RTSP/RTMP capture tests")
	}
	ffmpeg := os.Getenv("HIDBRIDGE_TEST_FFMPEG")
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	dir := t.TempDir()
	var addresses []string
	var listeners []net.Listener
	for range 2 {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		listeners = append(listeners, l)
		t.Cleanup(func() { _ = l.Close() })
		addresses = append(addresses, l.Addr().String())
	}
	config := fmt.Sprintf(`logLevel: info
rtsp: true
rtspAddress: %s
rtspTransports: [tcp]
rtspEncryption: "no"
rtmp: true
rtmpAddress: %s
rtmpEncryption: "no"
hls: false
webrtc: false
srt: false
moq: false
api: false
metrics: false
pprof: false
playback: false
paths:
  all_others:
`, addresses[0], addresses[1])
	configPath := filepath.Join(dir, "mediamtx.yml")
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, l := range listeners {
		_ = l.Close()
	}
	media := startCaptureProcess(t, ctx, dir, server, configPath)
	awaitCaptureProcess(t, ctx, media, func() bool {
		for _, address := range addresses {
			c, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
			if err != nil {
				return false
			}
			_ = c.Close()
		}
		return true
	})
	for _, colour := range []string{"red", "blue"} {
		publisher := startCaptureProcess(t, ctx, dir, ffmpeg,
			"-hide_banner", "-loglevel", "error", "-nostdin", "-re", "-f", "lavfi", "-i", "color=c="+colour+":s=160x120:r=10",
			"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
			"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-g", "10", "-bf", "0", "-pix_fmt", "yuv420p",
			"-c:a", "aac", "-progress", "pipe:1", "-stats_period", "0.1", "-f", "rtsp", "-rtsp_transport", "tcp",
			"rtsp://"+addresses[0]+"/"+colour)
		awaitCaptureProcess(t, ctx, publisher, func() bool { return strings.Contains(publisher.logs.String(), "progress=continue") })
	}
	for i, scheme := range []string{"rtsp", "rtmp"} {
		t.Run(scheme, func(t *testing.T) {
			for n, colour := range []string{"red", "blue", "red"} {
				g := Grabber{FFmpeg: ffmpeg, URL: scheme + "://" + addresses[i] + "/" + colour,
					Format: "avfoundation", Device: "0", Size: "1920x1080", Framerate: 30, Timeout: 10 * time.Second}
				out := filepath.Join(dir, fmt.Sprintf("%s-%d.png", scheme, n))
				if err := g.Grab(ctx, out); err != nil {
					t.Fatalf("%s: %v\n%s", colour, err, media.logs.String())
				}
				f, err := os.Open(out)
				if err != nil {
					t.Fatal(err)
				}
				frame, err := png.Decode(f)
				_ = f.Close()
				if err != nil {
					t.Fatal(err)
				}
				if frame.Bounds().Dx() != 160 || frame.Bounds().Dy() != 120 {
					t.Fatalf("stream was resized by device defaults: %v", frame.Bounds())
				}
				r, green, b, _ := frame.At(80, 60).RGBA()
				if green > 5000 || (colour == "red" && (r < 50000 || b > 5000)) || (colour == "blue" && (b < 50000 || r > 5000)) {
					t.Fatalf("wrong stream: want %s, got RGB(%d,%d,%d)", colour, r, green, b)
				}
			}
		})
	}
	// A connected peer that sends no handshake must obey the whole-capture
	// deadline and leave no empty or partial snapshot behind.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	for _, scheme := range []string{"rtsp", "rtmp"} {
		out := filepath.Join(dir, "timeout-"+scheme+".png")
		g := Grabber{FFmpeg: ffmpeg, URL: scheme + "://" + listener.Addr().String() + "/silent", Timeout: 200 * time.Millisecond}
		started := time.Now()
		if err := g.Grab(ctx, out); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("%s stalled peer: %v", scheme, err)
		}
		if time.Since(started) > 3*time.Second {
			t.Fatalf("%s capture exceeded timeout allowance", scheme)
		}
		if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s timeout left output: %v", scheme, err)
		}
	}
}

type captureProcess struct {
	logs captureLogs
	done chan struct{}
	err  error
}

type captureLogs struct {
	sync.Mutex
	text strings.Builder
}

func (l *captureLogs) Write(p []byte) (int, error) {
	l.Lock()
	defer l.Unlock()
	return l.text.Write(p)
}

func (l *captureLogs) String() string {
	l.Lock()
	defer l.Unlock()
	return l.text.String()
}

func startCaptureProcess(t *testing.T, ctx context.Context, dir, executable string, args ...string) *captureProcess {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	p := &captureProcess{done: make(chan struct{})}
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = &p.logs, &p.logs
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	go func() { p.err = cmd.Wait(); close(p.done) }()
	t.Cleanup(func() { cancel(); <-p.done })
	return p
}

func awaitCaptureProcess(t *testing.T, ctx context.Context, p *captureProcess, ready func() bool) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for !ready() {
		select {
		case <-p.done:
			t.Fatalf("capture fixture exited: %v\n%s", p.err, p.logs.String())
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-deadline.C:
			t.Fatalf("capture fixture did not start:\n%s", p.logs.String())
		case <-tick.C:
		}
	}
}
