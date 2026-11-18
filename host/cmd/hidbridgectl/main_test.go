package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestOfflineCommands(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"keys"}, strings.NewReader(""), &out, &errOut); code != 0 || !strings.Contains(out.String(), "capslock") || !strings.Contains(out.String(), "volume_up") {
		t.Fatalf("keys: code %d, out %q, err %q", code, out.String(), errOut.String())
	}
	out.Reset()
	if code := run([]string{"version"}, strings.NewReader(""), &out, &errOut); code != 0 || !strings.HasPrefix(out.String(), "hidbridgectl ") {
		t.Fatalf("version: code %d, out %q", code, out.String())
	}
	if code := run(nil, strings.NewReader(""), &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "usage:") {
		t.Fatalf("no command: code %d, err %q", code, errOut.String())
	}
	errOut.Reset()
	if code := run([]string{"-port", "/nonexistent/device", "fly"}, strings.NewReader(""), &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "unknown command") {
		t.Fatalf("unknown command: code %d, err %q", code, errOut.String())
	}
	errOut.Reset()
	if code := run([]string{"-port", "/nonexistent/device", "status"}, strings.NewReader(""), &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "/nonexistent/device") {
		t.Fatalf("missing port: code %d, err %q", code, errOut.String())
	}
	errOut.Reset()
	if code := run([]string{"-screen", "wide", "status"}, strings.NewReader(""), &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "WIDTHxHEIGHT") {
		t.Fatalf("bad screen: code %d, err %q", code, errOut.String())
	}
}

func TestInvalidInputIsRejectedBeforeOpeningPort(t *testing.T) {
	for _, args := range [][]string{{"key", "not-a-key"}, {"run"}, {"status", "surprise"}, {"run", "/nonexistent/script.hid"}, {"version", "extra"}} {
		var out, errOut bytes.Buffer
		code := run(append([]string{"-port", "/nonexistent/device"}, args...), strings.NewReader(""), &out, &errOut)
		if code != 2 || strings.Contains(errOut.String(), "serial port") {
			t.Errorf("%v: %d %s", args, code, errOut.String())
		}
	}
}

func TestBuildLine(t *testing.T) {
	tests := []struct {
		cmd  string
		rest []string
		want string
	}{
		{"type", []string{"Hello,", "world!"}, `type "Hello, world!"`},
		{"key", []string{"ctrl+alt+delete", "100ms"}, `key ctrl+alt+delete 100ms`},
		{"capture", []string{"after login"}, `capture "after login"`},
		{"echo", []string{"a", "note"}, `echo "a note"`},
		{"sync", nil, `sync`},
	}
	for _, tc := range tests {
		if got := buildLine(tc.cmd, tc.rest); got != tc.want {
			t.Errorf("buildLine(%s, %v) = %q, want %q", tc.cmd, tc.rest, got, tc.want)
		}
	}
}
