package main

import (
	"errors"
	"os"
	"sync"
	"time"
)

// pty owns both ends: keeping the slave open makes repeated host sessions
// possible, and closing the pair releases the device completely.
type pty struct {
	*os.File
	slave *os.File
	once  sync.Once
	err   error
}

func (p *pty) Close() error {
	p.once.Do(func() {
		slaveErr := p.slave.Close()
		if errors.Is(slaveErr, os.ErrClosed) {
			slaveErr = nil
		}
		p.err = errors.Join(p.File.Close(), slaveErr)
	})
	return p.err
}

// drain lets the host receive the final acknowledgement and close its
// slave. Closing the master immediately can discard unread slave bytes.
func (p *pty) drain() {
	_ = p.slave.Close()
	if err := p.File.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		return
	}
	var buf [256]byte
	for {
		if _, err := p.File.Read(buf[:]); err != nil {
			return
		}
	}
}
