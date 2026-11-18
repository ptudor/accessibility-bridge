package capture

import (
	"context"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestArgs(t *testing.T) {
	g := Grabber{FFmpeg: "ffmpeg", Format: "avfoundation", Device: "1", Size: "1920x1080", Framerate: 30}
	got := strings.Join(g.Args("/tmp/x.png"), " ")
	want := "-hide_banner -loglevel error -nostdin -y -f avfoundation -framerate 30 -video_size 1920x1080 -i 1:none -map 0:v:0 -frames:v 1 -an -sn -dn /tmp/x.png"
	if got != want {
		t.Fatalf("args = %q\nwant   %q", got, want)
	}
	g = Grabber{Format: "v4l2", Device: "/dev/video0", PixelFormat: "yuyv422"}
	got = strings.Join(g.Args("o.png"), " ")
	if !strings.Contains(got, "-pixel_format yuyv422 -i /dev/video0 ") || strings.Contains(got, "-framerate") {
		t.Fatalf("args = %q", got)
	}
}

func TestStreamInputIgnoresDeviceSettings(t *testing.T) {
	for _, scheme := range []string{"rtsp", "rtsps", "rtmp", "rtmps", "RTSP"} {
		t.Run(scheme, func(t *testing.T) {
			// These device defaults must not be sent to a network demuxer.
			g := Grabber{FFmpeg: "ffmpeg", URL: scheme + "://user:password@encoder.example/live/view?token=key",
				Format: "avfoundation", Device: "0", Size: "1920x1080", Framerate: 30,
				PixelFormat: "uyvy422", FormatCode: "Hi50", Timeout: 3 * time.Second}
			if err := g.ValidateInput(); err != nil {
				t.Fatal(err)
			}
			args := g.Args("out.png")
			for _, arg := range args {
				if arg == "-f" || arg == "-framerate" || arg == "-video_size" || arg == "-pixel_format" || arg == "-format_code" {
					t.Fatalf("device option %s in network capture: %v", arg, args)
				}
			}
			got := strings.Join(args, " ")
			if !strings.Contains(got, "-i "+strings.ToLower(scheme)+"://user:password@encoder.example/live/view?token=key -map 0:v:0") {
				t.Fatalf("input URL or video selection changed: %s", got)
			}
			if strings.HasPrefix(strings.ToLower(scheme), "rtsp") {
				if !strings.Contains(got, "-rtsp_transport tcp -allowed_media_types video -timeout 3000000") {
					t.Fatalf("missing RTSP transport/timeout: %s", got)
				}
			} else if !strings.Contains(got, "-rw_timeout 3000000 -rtmp_live live -rtmp_buffer 0") || strings.Contains(got, "-timeout ") {
				t.Fatalf("RTMP must read live with a bounded wait, without enabling listen mode: %s", got)
			}
			secure := scheme == "rtsps" || scheme == "rtmps"
			if strings.Contains(got, "-tls_verify 1") != secure {
				t.Fatalf("TLS verification: %s", got)
			}
		})
	}
	g := Grabber{URL: "rtsp://encoder.example/live", RTSPTransport: "udp"}
	if got := strings.Join(g.Args("out.png"), " "); !strings.Contains(got, "-rtsp_transport udp") {
		t.Fatalf("UDP override: %s", got)
	}
}

func TestDeckLinkInputOptions(t *testing.T) {
	g := Grabber{FFmpeg: "ffmpeg", Format: "decklink", Device: "DeckLink input 1",
		FormatCode: "Hi50", PixelFormat: "uyvy422", Size: "1920x1080", Framerate: 30}
	if err := g.ValidateInput(); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(g.Args("out.png"), " ")
	if !strings.Contains(got, "-f decklink -format_code Hi50 -raw_format uyvy422 -i DeckLink input 1") ||
		strings.Contains(got, "-video_size") || strings.Contains(got, "-framerate") || strings.Contains(got, "-pixel_format") {
		t.Fatalf("DeckLink options: %s", got)
	}
	g.FormatCode, g.PixelFormat = "", ""
	if got := strings.Join(g.Args("out.png"), " "); strings.Contains(got, "-format_code") || strings.Contains(got, "-raw_format") {
		t.Fatalf("autodetect should omit explicit modes: %s", got)
	}
}

func TestInvalidStreamURLDoesNotLeakOrCreateOutput(t *testing.T) {
	for _, input := range []string{
		"rstp://encoder.example/live", "https://encoder.example/live", "file:///etc/passwd",
		"rtsp:///live", "rtsp://encoder.example:65536/live", "rtsp://encoder.example:0/live",
		"rtsp://user:secret@encoder.example:bad/live", "rtsp://user:%zz@encoder.example/live",
		"rtsp://encoder.example/live#secret",
		"rtsp://encoder.example/live?token=secret with spaces",
	} {
		g := Grabber{FFmpeg: "false", URL: input}
		out := filepath.Join(t.TempDir(), "out.png")
		err := g.Grab(context.Background(), out)
		if err == nil || !strings.Contains(err.Error(), "capture.url") || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), input) {
			t.Fatalf("invalid URL error: %v", err)
		}
		if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("invalid input created an output: %v", err)
		}
	}
}

func TestStreamErrorsRedactCredentialsAndRemovePartialCapture(t *testing.T) {
	dir := t.TempDir()
	ffmpeg := filepath.Join(dir, "fake-ffmpeg")
	// Simulate FFmpeg echoing its input URL and leaving a partial output.
	if err := os.WriteFile(ffmpeg, []byte("#!/bin/sh\nfor arg; do printf '%s\\n' \"$arg\" >&2; out=$arg; done\nprintf partial >\"$out\"\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	g := Grabber{FFmpeg: ffmpeg, URL: "rtsp://user:secret'phrase@encoder.example/live?token=private-key"}
	out := filepath.Join(dir, "out.png")
	err := g.Grab(context.Background(), out)
	if err == nil || !strings.Contains(err.Error(), "[stream URL]") || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "phrase") || strings.Contains(err.Error(), "private-key") {
		t.Fatalf("unredacted stream failure: %v", err)
	}
	if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial capture remains: %v", err)
	}
}

func TestPathAndLabel(t *testing.T) {
	at := time.Date(2026, 10, 1, 15, 4, 5, 0, time.UTC)
	got := Path("captures", "login test", "after login!", 7, at)
	want := filepath.Join("captures", "2026", "10", "01", "150405.000000000-login-test-007-after-login.png")
	if got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
	if Label("") != "frame" || Label("///") != "frame" || len(Label(strings.Repeat("a", 100))) != 48 {
		t.Error("Label is wrong")
	}
}

func TestGrabRequiresConfiguration(t *testing.T) {
	err := Grabber{}.Grab(context.Background(), filepath.Join(t.TempDir(), "x.png"))
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("err = %v", err)
	}
	// A command that exits non-zero leaves no file behind.
	out := filepath.Join(t.TempDir(), "x.png")
	err = Grabber{FFmpeg: "false", Format: "v4l2", Device: "/dev/video0", Timeout: time.Second}.Grab(context.Background(), out)
	if err == nil {
		t.Fatal("false succeeded")
	}
	err = Grabber{FFmpeg: "true", Format: "v4l2", Device: "/dev/video0", Timeout: time.Second}.Grab(context.Background(), out)
	if err == nil || !strings.Contains(err.Error(), "empty file") {
		t.Fatalf("err = %v, want empty file", err)
	}
}

func TestCaptureNeverRemovesExistingFile(t *testing.T) {
	out := filepath.Join(t.TempDir(), "existing.png")
	if err := os.WriteFile(out, []byte("previous capture"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := (Grabber{FFmpeg: "false", Format: "v4l2", Device: "0"}).Grab(context.Background(), out)
	if err == nil {
		t.Fatal("existing capture accepted")
	}
	got, err := os.ReadFile(out)
	if err != nil || string(got) != "previous capture" {
		t.Fatalf("existing capture changed: %q, %v", got, err)
	}
}

func TestCaptureProducesPNGAndPropagatesCancellation(t *testing.T) {
	dir := t.TempDir()
	ffmpeg := filepath.Join(dir, "fake-ffmpeg")
	if err := os.WriteFile(ffmpeg, []byte("#!/bin/sh\nfor out; do :; done\ncp \"${0}.png\" \"$out\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(ffmpeg + ".png")
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(file, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	g := Grabber{FFmpeg: ffmpeg, Format: "v4l2", Device: "0"}
	if err = g.Grab(context.Background(), filepath.Join(dir, "capture.png")); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(ffmpeg, []byte("#!/bin/sh\nexec sleep 30\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	out := filepath.Join(dir, "cancelled.png")
	if err = g.Grab(ctx, out); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancel: %v", err)
	}
	if _, err = os.Stat(out); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled capture left a file")
	}
}
