package session

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTranscript(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 10, 1, 15, 4, 5, 0, time.Local)
	l, err := Open(dir, "login test", at)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "2026", "10", "01", "150405.000000000-login-test.jsonl")
	if l.Path() != want {
		t.Fatalf("path = %q, want %q", l.Path(), want)
	}
	if err = l.Record("command", map[string]any{"line": 3, "text": "key enter"}); err != nil {
		t.Fatal(err)
	}
	if err = l.Record("event", map[string]any{"event": "leds caps"}); err != nil {
		t.Fatal(err)
	}
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = Open(dir, "login test", at); err == nil {
		t.Fatal("overwrote an existing transcript")
	}
	f, err := os.Open(want)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var kinds []string
	for s := bufio.NewScanner(f); s.Scan(); {
		var entry map[string]any
		if err := json.Unmarshal(s.Bytes(), &entry); err != nil {
			t.Fatal(err)
		}
		if _, ok := entry["t"].(string); !ok {
			t.Fatalf("entry without time: %v", entry)
		}
		kinds = append(kinds, entry["kind"].(string))
	}
	if len(kinds) != 2 || kinds[0] != "command" || kinds[1] != "event" {
		t.Fatalf("kinds = %v", kinds)
	}
	var nilLog *Log
	if nilLog.Record("x", nil) != nil || nilLog.Close() != nil || nilLog.Path() != "" {
		t.Fatal("nil log is not silent")
	}
}

func TestTranscriptRetainsWriteFailure(t *testing.T) {
	l, err := Open(t.TempDir(), "failure", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_ = l.file.Close()
	if err = l.Record("event", nil); err == nil {
		t.Fatal("closed file write succeeded")
	}
	if l.Err() == nil || l.Close() == nil {
		t.Fatal("write failure was lost")
	}
}
