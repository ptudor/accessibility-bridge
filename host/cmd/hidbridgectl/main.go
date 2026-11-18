// Command hidbridgectl drives the accessibility-bridge board from the control
// computer: it types, presses keys, moves and clicks, captures the target's
// display, and runs usability-test scripts, over docs/PROTOCOL.md.
//
// Usage:
//
//	hidbridgectl [-config FILE] [-port DEVICE] [-session NAME] [-json] [-quiet] [-screen WxH] COMMAND [ARGS]
//
// Commands that need no board: keys, version.
// Board commands: status, identity, events, run FILE|-, bootloader, and every
// script verb (type, key, down, up, report, move, pointer, click, button,
// hover, drag, scroll, consumer, wait, release, capture, expect, detach, attach, wake).
// A one-shot command releases everything when it exits; sequences that hold
// keys belong in a script or on stdin with run -.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/ptudor/accessibility-bridge/host/internal/buildinfo"
	"github.com/ptudor/accessibility-bridge/host/internal/config"
	"github.com/ptudor/accessibility-bridge/host/internal/keymap"
	"github.com/ptudor/accessibility-bridge/host/internal/link"
	"github.com/ptudor/accessibility-bridge/host/internal/proto"
	"github.com/ptudor/accessibility-bridge/host/internal/script"
	"github.com/ptudor/accessibility-bridge/host/internal/serial"
	"github.com/ptudor/accessibility-bridge/host/internal/session"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

const usageText = `usage: hidbridgectl [flags] COMMAND [ARGS]

  status | identity | events | run FILE|- | bootloader | keys | version
  type TEXT... key CHORD [HOLD]        down KEY... up KEY...
  report HEX16            move DX DY              pointer X Y
  click X Y [BUTTON] [double]   button BUTTON down|up|click   scroll N
  hover X Y               drag X1 Y1 X2 Y2 [BUTTON]
  consumer NAME [HOLD]    wait DURATION           release
  capture [LABEL]         expect leds|usb|held|protocol ...
  detach | attach | wake

flags:
`

var scriptVerbs = map[string]bool{
	"type": true, "key": true, "down": true, "up": true, "report": true, "move": true, "pointer": true,
	"click": true, "hover": true, "drag": true, "button": true, "scroll": true, "consumer": true, "wait": true, "sleep": true, "sync": true,
	"release": true, "capture": true, "expect": true, "echo": true, "detach": true, "attach": true, "wake": true,
}

type options struct {
	config  string
	port    string
	session string
	json    bool
	quiet   bool
	screen  string
	verbose bool
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("hidbridgectl", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var o options
	fs.StringVar(&o.config, "config", "", "TOML configuration file (hidbridge.toml.example)")
	fs.StringVar(&o.port, "port", "", "CONTROL port device or glob; overrides [port] path")
	fs.StringVar(&o.session, "session", "", "session name for the transcript and captures")
	fs.BoolVar(&o.json, "json", false, "status and events as JSON")
	fs.BoolVar(&o.quiet, "quiet", false, "no trace on stdout")
	fs.StringVar(&o.screen, "screen", "", "target screen WxH in pixels; overrides [target] screen")
	fs.BoolVar(&o.verbose, "verbose", false, "link diagnostics on stderr")
	fs.Usage = func() {
		fmt.Fprint(stderr, usageText)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() == 0 {
		fs.Usage()
		return 2
	}
	cmd, rest := fs.Arg(0), fs.Args()[1:]
	if (cmd == "version" || cmd == "keys" || cmd == "status" || cmd == "identity" || cmd == "events" || cmd == "bootloader") && len(rest) != 0 {
		fmt.Fprintf(stderr, "hidbridgectl: %s takes no arguments\n", cmd)
		return 2
	}
	switch cmd {
	case "version":
		fmt.Fprintln(stdout, "hidbridgectl", buildinfo.String(), "protocol", proto.Version)
		return 0
	case "keys":
		fmt.Fprintln(stdout, "keys:", strings.Join(keymap.Names(), " "))
		fmt.Fprintln(stdout, "consumer controls:", strings.Join(keymap.ConsumerNames(), " "))
		fmt.Fprintln(stdout, "buttons: left right middle")
		return 0
	}
	cfg, err := loadConfig(o)
	if err != nil {
		fmt.Fprintln(stderr, "hidbridgectl:", err)
		return 2
	}
	if !scriptVerbs[cmd] && cmd != "status" && cmd != "identity" && cmd != "events" && cmd != "run" && cmd != "bootloader" {
		fmt.Fprintf(stderr, "hidbridgectl: unknown command %q\n", cmd)
		fs.Usage()
		return 2
	}
	if err = board(o, cfg, cmd, rest, stdin, stdout, stderr); err != nil {
		var expectation *script.ExpectationError
		switch {
		case errors.As(err, &expectation):
			fmt.Fprintln(stderr, "hidbridgectl: expectation failed:", err)
			return 1
		case errors.Is(err, context.Canceled):
			fmt.Fprintln(stderr, "hidbridgectl: interrupted:", err)
			return 130
		}
		fmt.Fprintln(stderr, "hidbridgectl:", err)
		return 2
	}
	return 0
}

func loadConfig(o options) (*config.Config, error) {
	cfg := config.Default()
	if o.config != "" {
		var err error
		if cfg, err = config.Load(o.config); err != nil {
			return nil, err
		}
	}
	if o.port != "" {
		cfg.Port.Path = o.port
	}
	if o.screen != "" {
		w, h, ok := strings.Cut(o.screen, "x")
		wi, errW := strconv.Atoi(w)
		hi, errH := strconv.Atoi(h)
		if !ok || errW != nil || errH != nil {
			return nil, fmt.Errorf("-screen %q is not WIDTHxHEIGHT", o.screen)
		}
		cfg.Target.Screen = [2]int{wi, hi}
		if err := cfg.Validate(); err != nil {
			return nil, err
		}
	}
	return cfg, nil
}

func board(o options, cfg *config.Config, cmd string, rest []string, stdin io.Reader, stdout, stderr io.Writer) (result error) {
	// Parse and open input before HELLO, which clears the board's state.
	var prepared []*script.Command
	if cmd == "run" {
		if len(rest) != 1 {
			return errors.New("run takes one script file, or - for stdin")
		}
		if rest[0] != "-" {
			file, err := os.Open(rest[0])
			if err != nil {
				return err
			}
			prepared, err = script.ParseAll(file)
			_ = file.Close()
			if err != nil {
				return err
			}
		}
	} else if scriptVerbs[cmd] {
		c, err := script.ParseLine(buildLine(cmd, rest), 1)
		if err != nil {
			return err
		}
		prepared = []*script.Command{c}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	path, err := serial.Find(cfg.Port.Path)
	if err != nil {
		return err
	}
	port, err := serial.Open(path)
	if err != nil {
		return err
	}
	cts, ctsErr := port.CTS()
	if ctsErr == nil && !cts {
		_ = port.Close()
		return fmt.Errorf("%s: CTS# is deasserted: the TARGET side of the board is unpowered or its firmware is not running", path)
	}
	logLevel := slog.LevelWarn
	if o.verbose {
		logLevel = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: logLevel}))
	s, err := link.OpenContext(ctx, port, link.Options{AckTimeout: cfg.Link.AckTimeout.Std(), Retries: cfg.Link.Retries, Logger: logger})
	if err != nil {
		_ = port.Close()
		return fmt.Errorf("%s: %w", path, err)
	}
	name := o.session
	var transcript *session.Log
	var runner *script.Runner
	defer func() {
		// Keep recording events until the release on close is acknowledged.
		result = errors.Join(result, s.Close())
		if runner != nil {
			runner.Stop()
		}
		result = errors.Join(result, transcript.Close())
	}()
	if cmd == "run" {
		if len(rest) != 1 {
			return errors.New("run takes one script file, or - for stdin")
		}
		if name == "" {
			name = "stdin"
			if rest[0] != "-" {
				name = strings.TrimSuffix(filepath.Base(rest[0]), filepath.Ext(rest[0]))
			}
		}
	}
	if name != "" {
		if transcript, err = session.Open(cfg.Session.Dir, name, time.Now()); err != nil {
			return err
		}
		fmt.Fprintln(stderr, "transcript:", transcript.Path())
	} else {
		name = "hidbridgectl"
	}
	out := io.Writer(stdout)
	if o.quiet {
		out = io.Discard
	}
	r := &script.Runner{
		Session:    s,
		Target:     script.Target{Screen: cfg.Target.Screen, KeyInterval: cfg.Target.KeyInterval.Std(), ClickHold: cfg.Target.ClickHold.Std()},
		Grabber:    cfg.Capture.Grabber(),
		CaptureDir: cfg.Capture.Dir,
		Name:       name,
		Log:        transcript,
		Out:        out,
		ShowEvents: cmd == "run" || scriptVerbs[cmd],
	}
	if transcript != nil {
		id, err := s.IdentityContext(ctx)
		if err != nil {
			return err
		}
		if err = transcript.Record("session", map[string]any{"port": path, "identity": id, "target": cfg.Target.Name, "screen": cfg.Target.Screen}); err != nil {
			return err
		}
	}
	r.Start()
	runner = r
	switch cmd {
	case "status":
		return status(ctx, s, path, cts, ctsErr, o.json, stdout)
	case "identity":
		id, err := s.IdentityContext(ctx)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(stdout, id)
		return err
	case "events":
		return events(ctx, s, o.json, stdout, stderr)
	case "bootloader":
		if _, err := s.SendContext(ctx, proto.Bootloader()); err != nil {
			return err
		}
		s.Abandon()
		_, err = fmt.Fprintln(stdout, "board acknowledged; it is entering its bootloader on the TARGET connector")
		return err
	}
	if cmd == "run" {
		if rest[0] == "-" {
			return r.Stream(ctx, stdin)
		}
	}
	if err = r.RunAll(ctx, prepared); err != nil {
		return err
	}
	return s.Sync(ctx)
}

// buildLine turns command-line arguments into one script line.
func buildLine(cmd string, rest []string) string {
	switch cmd {
	case "type", "echo":
		return cmd + " " + strconv.Quote(strings.Join(rest, " "))
	}
	parts := []string{cmd}
	for _, a := range rest {
		if a == "" || strings.ContainsAny(a, " \t\r\n\"#") {
			a = strconv.Quote(a)
		}
		parts = append(parts, a)
	}
	return strings.Join(parts, " ")
}

func status(ctx context.Context, s *link.Session, path string, cts bool, ctsErr error, asJSON bool, out io.Writer) error {
	id, err := s.IdentityContext(ctx)
	if err != nil {
		return err
	}
	st, err := s.StatusContext(ctx)
	if err != nil {
		return err
	}
	ctsText := "unavailable on this device"
	if ctsErr == nil {
		ctsText = "deasserted (target side unpowered or not running)"
		if cts {
			ctsText = "asserted (board running)"
		}
	}
	h := s.Hello()
	if asJSON {
		body, err := json.MarshalIndent(map[string]any{
			"port": path, "cts": ctsText, "identity": id,
			"firmware": fmt.Sprintf("%d.%d", h.FirmwareMajor, h.FirmwareMinor), "protocol": h.Version, "queue_capacity": h.Queue,
			"status": st, "usb": proto.USBStateName(st.USBState), "leds": proto.LEDNames(st.LEDs),
			"held": st.Flags&proto.FlagHeld != 0, "wakeup_enabled": st.Flags&proto.FlagWakeupEnabled != 0,
			"keyboard_protocol": protocolName(st.Flags&proto.FlagKeyboardBoot != 0), "mouse_protocol": protocolName(st.Flags&proto.FlagMouseBoot != 0),
			"link": s.Stats(),
		}, "", "  ")
		if err != nil {
			return err
		}
		_, err = out.Write(append(body, '\n'))
		return err
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "port\t%s\n", path)
	fmt.Fprintf(w, "cts\t%s\n", ctsText)
	fmt.Fprintf(w, "identity\t%s\n", id)
	fmt.Fprintf(w, "firmware\t%d.%d, protocol %d, queue %d\n", h.FirmwareMajor, h.FirmwareMinor, h.Version, h.Queue)
	fmt.Fprintf(w, "usb\t%s\n", proto.USBStateName(st.USBState))
	fmt.Fprintf(w, "held\t%v\n", st.Flags&proto.FlagHeld != 0)
	fmt.Fprintf(w, "leds\t%s\n", proto.LEDNames(st.LEDs))
	fmt.Fprintf(w, "protocol\tkeyboard %s, mouse %s\n", protocolName(st.Flags&proto.FlagKeyboardBoot != 0), protocolName(st.Flags&proto.FlagMouseBoot != 0))
	fmt.Fprintf(w, "remote wakeup\t%v\n", st.Flags&proto.FlagWakeupEnabled != 0)
	fmt.Fprintf(w, "queue\t%d\n", st.Queue)
	fmt.Fprintf(w, "uptime\t%ds\n", st.UptimeSeconds)
	fmt.Fprintf(w, "watchdog releases\t%d\n", st.Watchdog)
	fmt.Fprintf(w, "rx errors\t%d\n", st.RxErrors)
	fmt.Fprintf(w, "dropped keys\t%d\n", st.Dropped)
	return w.Flush()
}

func protocolName(boot bool) string {
	if boot {
		return "boot"
	}
	return "report"
}

func events(ctx context.Context, s *link.Session, asJSON bool, out, stderr io.Writer) error {
	ch, unsubscribe := s.Subscribe(256)
	defer unsubscribe()
	fmt.Fprintln(stderr, "listening for board events; interrupt to stop")
	enc := json.NewEncoder(out)
	start := time.Now()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case e, ok := <-ch:
			if !ok {
				return s.Err()
			}
			if asJSON {
				if err := enc.Encode(map[string]any{"t": time.Now().UTC().Format(time.RFC3339Nano), "event": e.String(), "detail": e}); err != nil {
					return err
				}
				continue
			}
			if _, err := fmt.Fprintf(out, "[%8.3fs] %s\n", time.Since(start).Seconds(), e); err != nil {
				return err
			}
		}
	}
}
