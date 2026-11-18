package config

import (
	"strings"
	"testing"
	"time"
)

func TestDefaultsValidate(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestParse(t *testing.T) {
	c, err := Parse([]byte(`
[port]
path = "/dev/cu.usbserial-HIDBRIDGE0001"

[target]
name = "x1-carbon"
screen = [2560, 1440]
key_interval = "25ms"

[capture]
device = "USB Video"
size = "1280x720"
`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Port.Path != "/dev/cu.usbserial-HIDBRIDGE0001" || c.Target.Name != "x1-carbon" || c.Target.Screen != [2]int{2560, 1440} ||
		c.Target.KeyInterval.Std() != 25*time.Millisecond || c.Target.ClickHold.Std() != 30*time.Millisecond ||
		c.Capture.Device != "USB Video" || c.Capture.Size != "1280x720" || c.Capture.Framerate != 30 || c.Session.Dir != "sessions" {
		t.Fatalf("parsed = %+v", c)
	}
}

func TestParseRejects(t *testing.T) {
	tests := []struct{ name, toml, want string }{
		{"unknown key", "[port]\npath = \"/dev/x\"\nbaud = 9600\n", "baud"},
		{"unknown table", "[portt]\npath = \"/dev/x\"\n", "portt"},
		{"bad size", "[capture]\nsize = \"wide\"\n", "capture.size"},
		{"bad interval", "[target]\nkey_interval = \"0ms\"\n", "key_interval"},
		{"bad screen", "[target]\nscreen = [0, 1080]\n", "target.screen"},
		{"short screen", "[target]\nscreen = [1920]\n", "screen"},
		{"long screen", "[target]\nscreen = [1920, 1080, 1]\n", "screen"},
		{"empty screen", "[target]\nscreen = []\n", "screen"},
		{"bad retries", "[link]\nretries = 0\n", "link.retries"},
		{"bad duration", "[link]\nack_timeout = 5\n", "ack_timeout"},
		{"bad duration text", "[link]\nack_timeout = \"soon\"\n", "soon"},
		{"misspelled RTSP", "[capture]\nurl = \"rstp://encoder.example/live\"\n", "capture.url"},
		{"bad transport", "[capture]\nurl = \"rtsp://encoder.example/live\"\nrtsp_transport = \"tpc\"\n", "capture.rtsp_transport"},
		{"misplaced format code", "[capture]\nformat_code = \"Hi50\"\n", "capture.format_code"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.toml))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want mention of %q", err, tc.want)
			}
		})
	}
}

func TestStreamAndDeckLinkConfiguration(t *testing.T) {
	for _, raw := range []string{
		"[capture]\nurl = \"rtsp://encoder.example/live\"\n",
		"[capture]\nurl = \"rtmp://encoder.example/live/laptop\"\n",
		"[capture]\nurl = \"rtsp://encoder.example/live\"\nformat = \"\"\ndevice = \"\"\nsize = \"\"\nframerate = 0\n",
		"[capture]\nformat = \"decklink\"\ndevice = \"DeckLink input 1\"\nformat_code = \"Hi50\"\nsize = \"\"\nframerate = 0\n",
	} {
		c, err := Parse([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		g := c.Capture.Grabber()
		if g.URL != c.Capture.URL || g.FormatCode != c.Capture.FormatCode || g.RTSPTransport != "tcp" || g.Timeout != 10*time.Second {
			t.Fatalf("capture config did not reach the grabber: %+v", g)
		}
	}
}
