// Command hidbridge-sim is the board, in software, on a pseudo-terminal:
// hidbridgectl opens the printed device exactly as it would the real
// CONTROL port, and every report the target would receive is printed here.
// It is how scripts are rehearsed before a board exists, and the reference
// behaviour the firmware must match (docs/PROTOCOL.md).
//
// Usage:
//
//	hidbridge-sim [-usb configured|attached|detached|suspended] [-wakeup] [-identity S] [-quiet]
//	hidbridge-sim -stdio      serve the protocol on stdin and stdout
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ptudor/accessibility-bridge/host/internal/buildinfo"
	"github.com/ptudor/accessibility-bridge/host/internal/proto"
	"github.com/ptudor/accessibility-bridge/host/internal/sim"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

type stdio struct {
	io.Reader
	io.Writer
}

func (s stdio) Close() error {
	var err error
	if c, ok := s.Reader.(io.Closer); ok {
		err = c.Close()
	}
	if c, ok := s.Writer.(io.Closer); ok {
		err = errors.Join(err, c.Close())
	}
	return err
}

func run(args []string) int {
	fs := flag.NewFlagSet("hidbridge-sim", flag.ContinueOnError)
	useStdio := fs.Bool("stdio", false, "serve on stdin and stdout instead of a pseudo-terminal")
	usb := fs.String("usb", "configured", "initial USB state: configured, attached, detached or suspended")
	wakeup := fs.Bool("wakeup", false, "the target has enabled remote wakeup")
	manualResume := fs.Bool("manual-resume", false, "keep a suspended target asleep to exercise the remote-wakeup timeout")
	identity := fs.String("identity", "accessibility-bridge R1 fw 0.2 sn SIMULATED", "IDENTITY reply")
	quiet := fs.Bool("quiet", false, "do not print the reports the target would see")
	version := fs.Bool("version", false, "print version and exit")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *version {
		fmt.Println("hidbridge-sim", buildinfo.String(), "protocol", proto.Version)
		return 0
	}
	if _, err := proto.ParseIdentity([]byte(*identity)); err != nil {
		fmt.Fprintln(os.Stderr, "hidbridge-sim:", err)
		return 2
	}
	state := map[string]uint8{"configured": proto.USBConfigured, "attached": proto.USBAttached, "detached": proto.USBDetached, "suspended": proto.USBSuspended}
	initial, ok := state[*usb]
	if !ok || fs.NArg() != 0 {
		fs.Usage()
		return 2
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	opts := sim.Options{USBState: initial, WakeupEnabled: *wakeup, ManualResume: *manualResume, Identity: *identity, FirmwareMajor: 0, FirmwareMinor: 2, Logger: logger}
	if !*quiet {
		start := time.Now()
		opts.Reports = func(r sim.Report) {
			fmt.Fprintf(os.Stderr, "[%8.3fs] target sees: %s\n", time.Since(start).Seconds(), r)
		}
	}
	board := sim.New(opts)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var rw io.ReadWriter
	var closer io.Closer
	if *useStdio {
		rw = stdio{os.Stdin, os.Stdout}
		closer = rw.(io.Closer)
	} else {
		master, slave, err := openPTY()
		if err != nil {
			fmt.Fprintln(os.Stderr, "hidbridge-sim:", err)
			return 2
		}
		rw, closer = master, master
		fmt.Fprintf(os.Stderr, "hidbridge-sim %s: CONTROL port is %s\n", buildinfo.String(), slave)
		fmt.Fprintf(os.Stderr, "  hidbridgectl -port %s status\n", slave)
	}
	defer closer.Close()
	done := make(chan error, 1)
	go func() { done <- board.Run(ctx, rw) }()
	var err error
	select {
	case err = <-done:
	case <-ctx.Done():
		if closer != nil {
			_ = closer.Close()
		}
		select {
		case err = <-done:
		case <-time.After(500 * time.Millisecond):
		}
	}
	switch {
	case err == nil, errors.Is(err, context.Canceled), errors.Is(err, io.EOF):
		return 0
	case errors.Is(err, sim.ErrBootloader):
		if terminal, ok := rw.(*pty); ok {
			terminal.drain()
		}
		fmt.Fprintln(os.Stderr, "hidbridge-sim: BOOTLOADER received; the board would now be a DFU device on the TARGET connector")
		return 0
	}
	fmt.Fprintln(os.Stderr, "hidbridge-sim:", err)
	return 1
}
