//go:build darwin || linux

package integration

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type output struct {
	sync.Mutex
	text string
}

func (o *output) Write(p []byte) (int, error) {
	o.Lock()
	defer o.Unlock()
	o.text += string(p)
	return len(p), nil
}

func (o *output) String() string {
	o.Lock()
	defer o.Unlock()
	return o.text
}

// Exercise the actual binaries, termios and PTY across consecutive sessions.
func TestCLI(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dir := t.TempDir()
	build := exec.CommandContext(ctx, "go", "build", "-o", dir+string(os.PathSeparator), "./cmd/...")
	build.Dir = ".."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	simulator := exec.CommandContext(ctx, filepath.Join(dir, "hidbridge-sim"))
	stderr, err := simulator.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = simulator.Start(); err != nil {
		t.Fatal(err)
	}
	var logs output
	path := make(chan string, 1)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for s := bufio.NewScanner(stderr); s.Scan(); {
			line := s.Text()
			_, _ = logs.Write([]byte(line + "\n"))
			if _, device, ok := strings.Cut(line, "CONTROL port is "); ok {
				path <- device
			}
		}
	}()
	simDone := make(chan error, 1)
	go func() { simDone <- simulator.Wait() }()
	t.Cleanup(func() { _ = simulator.Process.Signal(syscall.SIGTERM); <-readDone })
	var device string
	select {
	case device = <-path:
	case err := <-simDone:
		t.Fatalf("simulator: %v\n%s", err, logs.String())
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	ctl := filepath.Join(dir, "hidbridgectl")
	run := func(input string, want int, args ...string) []byte {
		t.Helper()
		command := exec.CommandContext(ctx, ctl, append([]string{"-port", device}, args...)...)
		command.Dir = dir
		command.Stdin = strings.NewReader(input)
		var stderr bytes.Buffer
		command.Stderr = &stderr
		out, err := command.Output()
		code := 0
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		if code != want {
			t.Fatalf("%v: exit %d, want %d\n%s\n%s", args, code, want, out, stderr.String())
		}
		return out
	}
	var status map[string]any
	if err := json.Unmarshal(run("", 0, "-json", "-session", "status", "status"), &status); err != nil {
		t.Fatal(err)
	}
	if status["usb"] != "configured" {
		t.Fatalf("status: %v", status)
	}
	// The network URL must reach FFmpeg through TOML, the CLI and the script
	// runner without inherited USB-device input options.
	fakeFFmpeg := filepath.Join(dir, "fake-ffmpeg")
	if err := os.WriteFile(fakeFFmpeg, []byte(`#!/bin/sh
found=0
for arg; do
    case "$arg" in
        rtsp://encoder.example/live/mirror) found=1 ;;
        -framerate|-video_size|-pixel_format) exit 61 ;;
    esac
    out=$arg
done
test "$found" = 1 || exit 62
cp "${0}.png" "$out"
`), 0o700); err != nil {
		t.Fatal(err)
	}
	var sourcePNG bytes.Buffer
	if err := png.Encode(&sourcePNG, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fakeFFmpeg+".png", sourcePNG.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	captureConfig := filepath.Join(dir, "streamer.toml")
	if err := os.WriteFile(captureConfig, []byte(fmt.Sprintf("[capture]\nffmpeg = %q\nurl = %q\n", fakeFFmpeg, "rtsp://encoder.example/live/mirror")), 0o600); err != nil {
		t.Fatal(err)
	}
	run("", 0, "-config", captureConfig, "-session", "capture", "capture", "mirror")
	captures, err := filepath.Glob(filepath.Join(dir, "captures", "*", "*", "*", "*.png"))
	if err != nil || len(captures) != 1 {
		t.Fatalf("captures: %v %v", captures, err)
	}
	if data, err := os.ReadFile(captures[0]); err != nil || !bytes.Equal(data, sourcePNG.Bytes()) {
		t.Fatalf("capture did not preserve the source PNG: %v", err)
	}
	run("wait 50ms\ntype \"abc\"", 0, "-quiet", "run", "-")
	if !strings.Contains(logs.String(), "keys=c") {
		t.Fatal("stdin EOF lost the final character")
	}
	run("expect usb suspended\n", 1, "-quiet", "run", "-")
	run("invalid command\nkey enter\n", 2, "-quiet", "run", "-")
	run("", 2, "down", "a", "b", "c", "d", "e", "f", "g")
	run("", 0, "detach")
	run("", 2, "key", "a")
	run("", 0, "attach")
	deadline := time.Now().Add(2 * time.Second)
	for {
		if err := json.Unmarshal(run("", 0, "-json", "status"), &status); err != nil {
			t.Fatal(err)
		}
		if status["usb"] == "configured" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("attach never configured")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// SIGTERM must stop the live event stream and preserve exit status 130.
	events := exec.CommandContext(ctx, ctl, "-port", device, "events")
	events.Dir = dir
	var eventLogs output
	events.Stderr = &eventLogs
	if err := events.Start(); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for !strings.Contains(eventLogs.String(), "listening") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	_ = events.Process.Signal(syscall.SIGTERM)
	var exit *exec.ExitError
	if err := events.Wait(); !errors.As(err, &exit) || exit.ExitCode() != 130 {
		t.Fatalf("events interrupt: %v\n%s", err, eventLogs.String())
	}
	run("", 0, "bootloader")
	if err := <-simDone; err != nil {
		t.Fatal(err)
	}
	<-readDone
	files, err := filepath.Glob(filepath.Join(dir, "sessions", "*", "*", "*", "*.jsonl"))
	if err != nil || len(files) != 5 {
		t.Fatalf("transcripts: %v %v", files, err)
	}
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(data, []byte("released (command)")) {
			t.Fatalf("missing final release in %s: %s", path, data)
		}
	}
	stdio := exec.CommandContext(ctx, filepath.Join(dir, "hidbridge-sim"), "-stdio", "-quiet")
	if out, err := stdio.CombinedOutput(); err != nil {
		t.Fatalf("stdio EOF: %v %s", err, out)
	}
}
