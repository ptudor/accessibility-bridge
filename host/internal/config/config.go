// Package config loads the hidbridgectl TOML configuration. The decoder is
// strict: unknown keys are errors, so a typo cannot silently disable a
// setting. Every section has a default, so no file is needed to drive a
// board; the file records a target and a capture setup.
package config

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/ptudor/accessibility-bridge/host/internal/capture"
)

// Duration is a time.Duration that TOML carries as a string such as "15ms".
type Duration time.Duration

// UnmarshalText parses the Go duration syntax.
func (d *Duration) UnmarshalText(text []byte) error {
	v, err := time.ParseDuration(string(text))
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// MarshalText renders the duration.
func (d Duration) MarshalText() ([]byte, error) { return []byte(time.Duration(d).String()), nil }

// Std returns the time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// Config is the whole file.
type Config struct {
	Port    Port    `toml:"port"`
	Link    Link    `toml:"link"`
	Target  Target  `toml:"target"`
	Capture Capture `toml:"capture"`
	Session Session `toml:"session"`
}

// Port locates the CONTROL port.
type Port struct {
	// Path is a device path or a glob that must match exactly one device.
	Path string `toml:"path"`
}

// Link tunes retransmission; the protocol defaults suit a working board.
type Link struct {
	AckTimeout Duration `toml:"ack_timeout"`
	Retries    int      `toml:"retries"`
}

// Target describes the computer under test.
type Target struct {
	Name string `toml:"name"`
	// Screen is the desktop size in pixels; click coordinates are pixels
	// and are scaled onto the absolute pointer's 0..32767 range.
	Screen [2]int `toml:"screen"`
	// KeyInterval is the board-side pause between key transitions when
	// typing, and the default hold of a tapped key.
	KeyInterval Duration `toml:"key_interval"`
	// ClickHold is the board-side time a clicked button stays down.
	ClickHold Duration `toml:"click_hold"`
}

// Capture configures frames from a network stream or a local capture device.
type Capture struct {
	FFmpeg        string   `toml:"ffmpeg"`
	URL           string   `toml:"url"`
	RTSPTransport string   `toml:"rtsp_transport"`
	Format        string   `toml:"format"`
	Device        string   `toml:"device"`
	FormatCode    string   `toml:"format_code"`
	Size          string   `toml:"size"`
	Framerate     int      `toml:"framerate"`
	PixelFormat   string   `toml:"pixel_format"`
	Dir           string   `toml:"dir"`
	Timeout       Duration `toml:"timeout"`
}

// Grabber maps the selected configuration to the snapshot implementation.
func (c Capture) Grabber() capture.Grabber {
	return capture.Grabber{
		FFmpeg: c.FFmpeg, URL: c.URL, RTSPTransport: c.RTSPTransport,
		Format: c.Format, Device: c.Device, FormatCode: c.FormatCode,
		Size: c.Size, Framerate: c.Framerate, PixelFormat: c.PixelFormat,
		Timeout: c.Timeout.Std(),
	}
}

// Session configures the transcript written by every run.
type Session struct {
	Dir string `toml:"dir"`
}

// Default is the configuration with no file.
func Default() *Config {
	c := &Config{
		Port:    Port{Path: "/dev/cu.usbserial-*"},
		Link:    Link{AckTimeout: Duration(time.Second), Retries: 5},
		Target:  Target{Screen: [2]int{1920, 1080}, KeyInterval: Duration(15 * time.Millisecond), ClickHold: Duration(30 * time.Millisecond)},
		Capture: Capture{FFmpeg: "ffmpeg", RTSPTransport: "tcp", Format: "avfoundation", Device: "0", Size: "1920x1080", Framerate: 30, Dir: "captures", Timeout: Duration(10 * time.Second)},
		Session: Session{Dir: "sessions"},
	}
	switch runtime.GOOS {
	case "linux":
		c.Port.Path = "/dev/serial/by-id/usb-FTDI_*"
		c.Capture.Format = "v4l2"
		c.Capture.Device = "/dev/video0"
	case "freebsd":
		c.Port.Path = "/dev/cuaU0"
		c.Capture.Format = "v4l2"
		c.Capture.Device = "/dev/video0"
	}
	return c
}

// Load reads and validates path on top of Default.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	return c, nil
}

// Parse decodes TOML on top of Default and validates the result.
func Parse(data []byte) (*Config, error) {
	c := Default()
	dec := toml.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(c); err != nil {
		var strict *toml.StrictMissingError
		if errors.As(err, &strict) {
			return nil, errors.New(strict.String())
		}
		return nil, err
	}
	// The TOML decoder accepts shorter/longer arrays into [2]int, retaining
	// defaults or truncating extras. Validate the supplied dimensions as a
	// slice as well so a malformed screen cannot silently change clicks.
	var dimensions struct {
		Target struct {
			Screen *[]int `toml:"screen"`
		} `toml:"target"`
	}
	if err := toml.Unmarshal(data, &dimensions); err != nil {
		return nil, err
	}
	if screen := dimensions.Target.Screen; screen != nil && len(*screen) != 2 {
		return nil, errors.New("target.screen must contain exactly two dimensions")
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

var sizeRE = regexp.MustCompile(`^[1-9][0-9]{1,4}x[1-9][0-9]{1,4}$`)

// Validate reports every problem it finds in one error.
func (c *Config) Validate() error {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	if c.Port.Path == "" {
		add("port.path is empty")
	}
	if c.Link.AckTimeout.Std() < 100*time.Millisecond || c.Link.AckTimeout.Std() > 10*time.Second {
		add("link.ack_timeout %v is outside 100ms..10s", c.Link.AckTimeout)
	}
	if c.Link.Retries < 1 || c.Link.Retries > 20 {
		add("link.retries %d is outside 1..20", c.Link.Retries)
	}
	if c.Target.Screen[0] < 1 || c.Target.Screen[1] < 1 || c.Target.Screen[0] > 32768 || c.Target.Screen[1] > 32768 {
		add("target.screen %v must be two sizes in 1..32768", c.Target.Screen)
	}
	if c.Target.KeyInterval.Std() < time.Millisecond || c.Target.KeyInterval.Std() > time.Second {
		add("target.key_interval %v is outside 1ms..1s", c.Target.KeyInterval)
	}
	if c.Target.ClickHold.Std() < time.Millisecond || c.Target.ClickHold.Std() > time.Second {
		add("target.click_hold %v is outside 1ms..1s", c.Target.ClickHold)
	}
	if err := c.Capture.Grabber().ValidateInput(); err != nil {
		add("%s", err)
	}
	if c.Capture.URL == "" && c.Capture.Format != "decklink" {
		if !sizeRE.MatchString(c.Capture.Size) {
			add("capture.size %q is not WIDTHxHEIGHT", c.Capture.Size)
		}
		if c.Capture.Framerate < 1 || c.Capture.Framerate > 240 {
			add("capture.framerate %d is outside 1..240", c.Capture.Framerate)
		}
	}
	if c.Capture.Dir == "" {
		add("capture.dir is empty")
	}
	if c.Capture.Timeout.Std() < time.Second || c.Capture.Timeout.Std() > time.Minute {
		add("capture.timeout %v is outside 1s..1m", c.Capture.Timeout)
	}
	if c.Session.Dir == "" {
		add("session.dir is empty")
	}
	if len(problems) == 0 {
		return nil
	}
	return errors.New(strings.Join(problems, "; "))
}
