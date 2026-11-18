// Package linktest connects a link.Session to a sim.Board in one process
// with an in-memory pipe, and can lose, corrupt or repeat frames on the way
// to the board. Only test files import it.
package linktest

import (
	"errors"
	"net"
	"os"
	"sync"
	"time"

	"github.com/ptudor/accessibility-bridge/host/internal/serial"
)

// Port adapts one end of a net.Pipe to link.Port. Writes may be shaped by
// Shape, which receives the frame bytes and returns what actually goes on
// the wire (nil to drop).
type Port struct {
	conn net.Conn
	mu   sync.Mutex
	// Shape rewrites or drops outgoing bytes; nil passes them through.
	Shape func(frame []byte) [][]byte
}

// Pair returns the host's port and the board's stream.
func Pair() (*Port, net.Conn) {
	a, b := net.Pipe()
	return &Port{conn: a}, b
}

func (p *Port) ReadTimeout(b []byte, d time.Duration) (int, error) {
	if err := p.conn.SetReadDeadline(time.Now().Add(d)); err != nil {
		return 0, err
	}
	n, err := p.conn.Read(b)
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return 0, serial.ErrTimeout
	}
	return n, err
}

func (p *Port) Write(b []byte) (int, error) {
	p.mu.Lock()
	shape := p.Shape
	p.mu.Unlock()
	out := [][]byte{b}
	if shape != nil {
		out = shape(b)
	}
	if err := p.conn.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return 0, err
	}
	for _, chunk := range out {
		if _, err := p.conn.Write(chunk); err != nil {
			return 0, err
		}
	}
	return len(b), nil
}

func (p *Port) Close() error { return p.conn.Close() }

// SetShape installs a shaping function from any goroutine.
func (p *Port) SetShape(f func([]byte) [][]byte) {
	p.mu.Lock()
	p.Shape = f
	p.mu.Unlock()
}

// DropFirst drops the first n writes, then passes everything.
func DropFirst(n int) func([]byte) [][]byte {
	var mu sync.Mutex
	return func(b []byte) [][]byte {
		mu.Lock()
		defer mu.Unlock()
		if n > 0 {
			n--
			return nil
		}
		return [][]byte{b}
	}
}

// CorruptFirst flips a payload bit in the first n writes.
func CorruptFirst(n int) func([]byte) [][]byte {
	var mu sync.Mutex
	return func(b []byte) [][]byte {
		mu.Lock()
		defer mu.Unlock()
		if n > 0 && len(b) > 3 {
			n--
			c := append([]byte(nil), b...)
			c[2] ^= 0x40
			return [][]byte{c}
		}
		return [][]byte{b}
	}
}

// Duplicate sends every write twice.
func Duplicate() func([]byte) [][]byte {
	return func(b []byte) [][]byte { return [][]byte{b, b} }
}
