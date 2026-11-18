// Package capture grabs one frame of the target's display from a network
// stream or local video capture device using FFmpeg on the control computer.
// Files land under YYYY/MM/DD so a day's test runs stay together.
package capture

import (
	"context"
	"errors"
	"fmt"
	"image/png"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Grabber holds the capture settings; config.Capture maps onto it.
type Grabber struct {
	FFmpeg        string
	URL           string
	RTSPTransport string
	Format        string
	Device        string
	FormatCode    string
	Size          string
	Framerate     int
	PixelFormat   string
	Timeout       time.Duration
}

// ValidateInput checks the selected input without opening a device or stream.
// A URL selects network capture; otherwise Format and Device select local input.
func (g Grabber) ValidateInput() error {
	if g.FFmpeg == "" {
		return errors.New("capture is not configured: capture.ffmpeg is empty")
	}
	if g.URL != "" {
		u, err := url.Parse(g.URL)
		if err != nil || u.Hostname() == "" || u.Opaque != "" || u.Fragment != "" || strings.IndexFunc(g.URL, unicode.IsSpace) >= 0 {
			// Parse errors include the original URL, which may contain credentials.
			return errors.New("capture.url must be a valid RTSP or RTMP URL with a hostname, escaped whitespace and no fragment")
		}
		switch strings.ToLower(u.Scheme) {
		case "rtsp", "rtsps", "rtmp", "rtmps":
		default:
			return errors.New("capture.url must use rtsp://, rtsps://, rtmp:// or rtmps://")
		}
		if port := u.Port(); port != "" {
			n, err := strconv.Atoi(port)
			if err != nil || n < 1 || n > 65535 {
				return errors.New("capture.url port must be in 1..65535")
			}
		}
		if g.RTSPTransport != "" && g.RTSPTransport != "tcp" && g.RTSPTransport != "udp" {
			return errors.New("capture.rtsp_transport must be tcp or udp")
		}
		return nil
	}
	if g.Format == "" || g.Device == "" {
		return errors.New("capture is not configured: set capture.url or capture.format and capture.device")
	}
	if g.FormatCode != "" && g.Format != "decklink" {
		return errors.New("capture.format_code is only supported with format = decklink")
	}
	return nil
}

func (g Grabber) timeout() time.Duration {
	if g.Timeout > 0 {
		return g.Timeout
	}
	return 10 * time.Second
}

// Args is the ffmpeg command line that writes one PNG frame to out.
// Call ValidateInput before executing it.
func (g Grabber) Args(out string) []string {
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y"}
	device := g.Device
	if g.URL != "" {
		scheme, rest, _ := strings.Cut(g.URL, ":")
		scheme = strings.ToLower(scheme)
		device = scheme + ":" + rest
		micros := strconv.FormatInt(g.timeout().Microseconds(), 10)
		if scheme == "rtsp" || scheme == "rtsps" {
			transport := g.RTSPTransport
			if transport == "" {
				transport = "tcp"
			}
			args = append(args, "-rtsp_transport", transport, "-allowed_media_types", "video", "-timeout", micros)
		} else {
			// Read the live publisher, never a recorded stream with the same name.
			args = append(args, "-rw_timeout", micros, "-rtmp_live", "live", "-rtmp_buffer", "0")
		}
		if scheme == "rtsps" || scheme == "rtmps" {
			args = append(args, "-tls_verify", "1")
		}
	} else {
		args = append(args, "-f", g.Format)
		if g.Format == "decklink" {
			if g.FormatCode != "" {
				args = append(args, "-format_code", g.FormatCode)
			}
			if g.PixelFormat != "" {
				args = append(args, "-raw_format", g.PixelFormat)
			}
		} else {
			if g.Framerate > 0 {
				args = append(args, "-framerate", fmt.Sprint(g.Framerate))
			}
			if g.Size != "" {
				args = append(args, "-video_size", g.Size)
			}
			if g.PixelFormat != "" {
				args = append(args, "-pixel_format", g.PixelFormat)
			}
		}
		if g.Format == "avfoundation" && !strings.Contains(device, ":") {
			device += ":none" // video only; no audio device
		}
	}
	return append(args, "-i", device, "-map", "0:v:0", "-frames:v", "1", "-an", "-sn", "-dn", out)
}

// Grab writes one frame to out and fails unless a non-empty file results.
func (g Grabber) Grab(ctx context.Context, out string) error {
	if err := g.ValidateInput(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	// Reserve this name before invoking ffmpeg; only this invocation's file
	// may be overwritten or removed, even when a previous capture exists.
	file, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("reserve capture: %w", err)
	}
	if err = file.Close(); err != nil {
		return err
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.Remove(out)
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, g.timeout())
	defer cancel()
	cmd := exec.CommandContext(ctx, g.FFmpeg, g.Args(out)...)
	// Bound pipe draining too if a wrapper leaves a child holding stderr open.
	cmd.WaitDelay = time.Second
	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("capture: %w", ctx.Err())
		}
		return fmt.Errorf("%s: %w: %s", g.FFmpeg, err, redactStreamURLs(strings.TrimSpace(string(output))))
	}
	info, err := os.Stat(out)
	if err != nil {
		return fmt.Errorf("%s produced no file: %w", g.FFmpeg, err)
	}
	if info.Size() == 0 {
		return fmt.Errorf("%s produced an empty file", g.FFmpeg)
	}
	result, err := os.Open(out)
	if err != nil {
		return err
	}
	defer result.Close()
	if _, err = png.Decode(result); err != nil {
		return fmt.Errorf("capture is not a PNG: %w", err)
	}
	complete = true
	return nil
}

// FFmpeg errors often echo the complete input URL, including camera passwords
// or stream keys. Those errors also go into session transcripts.
var streamURL = regexp.MustCompile(`(?i)rt(?:sp|mp)s?://\S+`)

func redactStreamURLs(s string) string { return streamURL.ReplaceAllString(s, "[stream URL]") }

var unsafeLabel = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// Label makes a file-name-safe label.
func Label(s string) string {
	s = unsafeLabel.ReplaceAllString(strings.TrimSpace(s), "-")
	s = strings.Trim(s, "-.")
	if s == "" {
		return "frame"
	}
	if len(s) > 48 {
		s = s[:48]
	}
	return s
}

// Path is where the n-th capture of a session goes:
// dir/YYYY/MM/DD/HHMMSS.nanoseconds-<session>-NNN-<label>.png.
func Path(dir, session, label string, n int, at time.Time) string {
	return filepath.Join(dir, at.Format("2006/01/02"), fmt.Sprintf("%s-%s-%03d-%s.png", at.Format("150405.000000000"), Label(session), n, Label(label)))
}
