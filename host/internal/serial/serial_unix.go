//go:build darwin || linux || freebsd

package serial

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

// Port is an open, exclusively held serial device.
type Port struct {
	fd        int
	path      string
	closed    atomic.Bool
	mu        sync.RWMutex // protects descriptor lifetime against close/reuse
	exclusive bool

	// WriteTimeout bounds each Write; DefaultWriteTimeout when zero.
	WriteTimeout time.Duration
}

// Open opens path at Baud, raw 8N1 with CTS output flow control, and takes
// an exclusive lock so two sessions cannot interleave frames.
func Open(path string) (*Port, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.EBUSY) {
			return nil, fmt.Errorf("%w: %s", ErrBusy, path)
		}
		return nil, fmt.Errorf("serial: open %s: %w", path, err)
	}
	p := &Port{fd: fd, path: path}
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		p.Close()
		return nil, fmt.Errorf("%w: %s: %v", ErrBusy, path, err)
	}
	if err = unix.IoctlSetInt(fd, unix.TIOCEXCL, 0); err != nil && !errors.Is(err, unix.ENOTTY) {
		p.Close()
		return nil, fmt.Errorf("serial: exclusive mode on %s: %w", path, err)
	}
	p.exclusive = err == nil
	if err = configure(fd); err != nil {
		p.Close()
		return nil, fmt.Errorf("serial: configure %s at %d baud: %w", path, Baud, err)
	}
	if err = flush(fd); err != nil && !errors.Is(err, unix.ENOTTY) {
		p.Close()
		return nil, fmt.Errorf("serial: flush %s: %w", path, err)
	}
	return p, nil
}

// Path returns the device path.
func (p *Port) Path() string { return p.path }

// ReadTimeout reads what is available within d, or ErrTimeout. A closed
// port reads io.EOF.
func (p *Port) ReadTimeout(b []byte, d time.Duration) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	deadline := time.Now().Add(d)
	for {
		if p.closed.Load() {
			return 0, io.EOF
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return 0, ErrTimeout
		}
		wait := remaining
		if wait > 100*time.Millisecond {
			wait = 100 * time.Millisecond
		}
		n, err := p.readReady(b, wait)
		if errors.Is(err, ErrTimeout) {
			continue
		}
		return n, err
	}
}

func (p *Port) readReady(b []byte, wait time.Duration) (int, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.closed.Load() {
		return 0, io.EOF
	}
	fds := []unix.PollFd{{Fd: int32(p.fd), Events: unix.POLLIN}}
	n, err := unix.Poll(fds, int(wait/time.Millisecond)+1)
	if err != nil && !errors.Is(err, unix.EINTR) {
		return 0, fmt.Errorf("serial: poll %s: %w", p.path, err)
	}
	if n <= 0 {
		return 0, ErrTimeout
	}
	if fds[0].Revents&(unix.POLLNVAL) != 0 {
		return 0, io.EOF
	}
	n, err = unix.Read(p.fd, b)
	switch {
	case err == nil && n > 0:
		return n, nil
	case err == nil:
		// Zero bytes with no error: the other end hung up.
		if fds[0].Revents&(unix.POLLHUP|unix.POLLERR) != 0 {
			return 0, io.EOF
		}
	case errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR):
	default:
		return 0, fmt.Errorf("serial: read %s: %w", p.path, err)
	}
	return 0, ErrTimeout
}

// Write writes all of b or fails. It blocks while CTS# is deasserted, up to
// WriteTimeout, which is how a halted target side shows up.
func (p *Port) Write(b []byte) (int, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	timeout := p.WriteTimeout
	if timeout <= 0 {
		timeout = DefaultWriteTimeout
	}
	deadline := time.Now().Add(timeout)
	written := 0
	for written < len(b) {
		if p.closed.Load() {
			return written, io.ErrClosedPipe
		}
		n, err := unix.Write(p.fd, b[written:])
		if n > 0 {
			written += n
			continue
		}
		if err != nil && !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EINTR) {
			return written, fmt.Errorf("serial: write %s: %w", p.path, err)
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return written, fmt.Errorf("%w: write to %s (CTS# deasserted: target side not running?)", ErrTimeout, p.path)
		}
		if remaining > 100*time.Millisecond {
			remaining = 100 * time.Millisecond
		}
		fds := []unix.PollFd{{Fd: int32(p.fd), Events: unix.POLLOUT}}
		if _, err = unix.Poll(fds, int(remaining/time.Millisecond)+1); err != nil && !errors.Is(err, unix.EINTR) {
			return written, fmt.Errorf("serial: poll %s: %w", p.path, err)
		}
	}
	return written, nil
}

// CTS reports whether the board asserts CTS#, meaning its firmware runs and
// has room. ErrNoModemStatus means the device cannot say.
func (p *Port) CTS() (bool, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.closed.Load() {
		return false, io.ErrClosedPipe
	}
	bits, err := unix.IoctlGetInt(p.fd, unix.TIOCMGET)
	if err != nil {
		if errors.Is(err, unix.ENOTTY) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENXIO) {
			return false, ErrNoModemStatus
		}
		return false, fmt.Errorf("serial: modem status of %s: %w", p.path, err)
	}
	return bits&unix.TIOCM_CTS != 0, nil
}

// Close releases the device. Pending reads return io.EOF.
func (p *Port) Close() error {
	if p.closed.Swap(true) {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	// TIOCEXCL lasts until the last descriptor closes. The simulator keeps
	// its slave open between sessions, so explicitly relinquish our claim.
	if p.exclusive {
		_ = unix.IoctlSetInt(p.fd, unix.TIOCNXCL, 0)
	}
	return unix.Close(p.fd)
}
