//go:build darwin || linux

package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ptudor/accessibility-bridge/host/internal/link"
	"github.com/ptudor/accessibility-bridge/host/internal/proto"
	"github.com/ptudor/accessibility-bridge/host/internal/serial"
	"github.com/ptudor/accessibility-bridge/host/internal/sim"
)

func TestPTYCanServeRepeatedExclusiveSessions(t *testing.T) {
	master, path, err := openPTY()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	board := sim.New(sim.Options{USBState: proto.USBConfigured})
	go func() { done <- board.Run(ctx, master) }()
	defer func() { cancel(); _ = master.Close(); <-done }()
	for i := 0; i < 3; i++ {
		port, err := serial.Open(path)
		if err != nil {
			t.Fatalf("open session %d: %v", i, err)
		}
		s, err := link.Open(port, link.Options{AckTimeout: 100 * time.Millisecond})
		if err != nil {
			_ = port.Close()
			t.Fatal(err)
		}
		if other, err := serial.Open(path); err == nil {
			_ = other.Close()
			t.Fatal("second host took an active port")
		} else if !errors.Is(err, serial.ErrBusy) {
			t.Fatal(err)
		}
		if _, err := s.Status(); err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
