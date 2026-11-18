// Package session writes the transcript of one run as JSON lines: every
// command, board event, capture and note with its time, so a usability test
// can be read back, compared with the captured frames, and repeated. Files
// land under YYYY/MM/DD.
package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Log is one transcript. A nil *Log discards everything, so callers need
// not check whether a transcript was requested.
type Log struct {
	mu     sync.Mutex
	file   *os.File
	enc    *json.Encoder
	path   string
	err    error
	closed bool
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// Open creates dir/YYYY/MM/DD/HHMMSS.nanoseconds-<name>.jsonl, refusing to overwrite.
func Open(dir, name string, at time.Time) (*Log, error) {
	name = strings.Trim(unsafeName.ReplaceAllString(strings.TrimSpace(name), "-"), "-.")
	if name == "" {
		name = "session"
	}
	if len(name) > 48 {
		name = name[:48]
	}
	path := filepath.Join(dir, at.Format("2006/01/02"), at.Format("150405.000000000")+"-"+name+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("transcript: %w", err)
	}
	return &Log{file: file, enc: json.NewEncoder(file), path: path}, nil
}

// Path is the transcript file; empty for a nil Log.
func (l *Log) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// Record appends one entry with the current time and kind.
func (l *Log) Record(kind string, fields map[string]any) error {
	if l == nil {
		return nil
	}
	entry := make(map[string]any, len(fields)+2)
	for k, v := range fields {
		entry[k] = v
	}
	entry["t"] = time.Now().UTC().Format(time.RFC3339Nano)
	entry["kind"] = kind
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		return l.err
	}
	if l.closed {
		return os.ErrClosed
	}
	l.err = l.enc.Encode(entry)
	return l.err
}

// Err returns the first write failure, including failures from event logging.
func (l *Log) Err() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
}

// Close flushes and closes the transcript.
func (l *Log) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return l.err
	}
	l.closed = true
	l.err = errors.Join(l.err, l.file.Sync(), l.file.Close())
	return l.err
}
