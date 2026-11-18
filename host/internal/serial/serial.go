// Package serial opens the HID bridge's CONTROL port: 1 000 000 baud, 8N1,
// raw, with hardware output flow control on CTS# so a halted target side
// stops the FTDI chip instead of losing bytes. Platform files set the
// termios; this file holds what every platform shares.
package serial

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Baud is the link rate fixed by docs/PROTOCOL.md.
const Baud = 1000000

// DefaultWriteTimeout bounds one Write. A board that is not running
// deasserts CTS#, so a write that cannot drain is the usual symptom.
const DefaultWriteTimeout = 2 * time.Second

var (
	// ErrTimeout is a read or write that did not complete in time.
	ErrTimeout = errors.New("serial: timed out")
	// ErrNoModemStatus means the device cannot report CTS (a pty, for
	// example); the link then does not gate retransmissions on it.
	ErrNoModemStatus = errors.New("serial: modem status unavailable")
	// ErrUnsupportedPlatform is returned where no termios backend exists.
	ErrUnsupportedPlatform = errors.New("serial: unsupported platform")
	// ErrBusy is a port another process holds.
	ErrBusy = errors.New("serial: port is busy")
)

// Find resolves a device path or glob pattern to exactly one existing
// device. A glob must select exactly one serial interface.
func Find(pattern string) (string, error) {
	if pattern == "" {
		return "", errors.New("no serial port given: pass -port or set [port] path")
	}
	if !strings.ContainsAny(pattern, "*?[") {
		if _, err := os.Stat(pattern); err != nil {
			return "", fmt.Errorf("serial port %s: %w", pattern, err)
		}
		return pattern, nil
	}
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return "", fmt.Errorf("serial port pattern %q: %w", pattern, err)
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no serial port matches %s (is the CONTROL cable plugged in?)", pattern)
	case 1:
		return matches[0], nil
	}
	return "", fmt.Errorf("%d serial ports match %s: %s; pass -port", len(matches), pattern, strings.Join(matches, ", "))
}
