//go:build !darwin && !linux && !freebsd

package serial

import (
	"fmt"
	"time"
)

// Port is unavailable on this platform.
type Port struct {
	WriteTimeout time.Duration
}

func Open(path string) (*Port, error) {
	return nil, fmt.Errorf("serial: open %s: %w", path, ErrUnsupportedPlatform)
}
func (p *Port) Path() string                                   { return "" }
func (p *Port) ReadTimeout([]byte, time.Duration) (int, error) { return 0, ErrUnsupportedPlatform }
func (p *Port) Write([]byte) (int, error)                      { return 0, ErrUnsupportedPlatform }
func (p *Port) CTS() (bool, error)                             { return false, ErrUnsupportedPlatform }
func (p *Port) Close() error                                   { return nil }
