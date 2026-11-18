# Display capture

The laptop mirrors its display onto HDMI. An encoder or capture card carries
that picture to the control computer; the HID bridge sends keyboard and
mouse input back over TARGET USB. All capture modes use the same `capture`
command and produce one PNG at the input's resolution.

## Two network encoders

Save a configuration per encoder in `host/`, for example
`streamer-1.local.toml` and `streamer-2.local.toml`. These local files are
ignored by Git. Use the actual playback URL from each encoder's settings;
the addresses and stream paths below are documentation placeholders.

`streamer-1.local.toml`:

```toml
[target]
name = "laptop-streamer-1"
screen = [1920, 1080]

[capture]
url = "rtsp://192.0.2.10:554/STREAM_PATH"
timeout = "10s"
```

For the second file, change the name and URL to the other encoder, such as
`rtsp://192.0.2.11:554/STREAM_PATH`. Add `[port] path` to each file if the
setups use different HID bridge boards. Select a file for each session:

```sh
bin/hidbridgectl -config streamer-1.local.toml capture laptop-1
bin/hidbridgectl -config streamer-2.local.toml capture laptop-2
bin/hidbridgectl -config streamer-1.local.toml run -
```

To rehearse capture before a board is assembled, start `bin/hidbridge-sim`
and pass its printed port with `-port` in the commands above.

RTSP defaults to TCP. Set `[capture] rtsp_transport = "udp"` for a device
that needs UDP. `url` also accepts `rtmp://`, `rtsps://` and `rtmps://`.
For RTMP, supply a **playback URL** of a live stream, for example
`rtmp://192.0.2.10/live/laptop`. The tool reads a stream; it does not act as
an ingest server for an encoder that only pushes video. TLS URLs enable
FFmpeg certificate verification and need certificates trusted by that build.
[FFmpeg protocol reference](https://ffmpeg.org/ffmpeg-protocols.html).

A nonempty `url` selects network input and overrides the local `format`,
`device`, `size`, `framerate`, `pixel_format` and `format_code` settings.
The decoder gets dimensions and frame rate from the stream. Each capture
opens a new connection, selects the first video stream, writes one PNG and
disconnects. Audio, subtitles and data are excluded. The timeout covers
connection, probing and decoding; increase it up to `60s` for a slow encoder.

Set `[target] screen` to the captured image's pixel dimensions when issuing
pixel-coordinate clicks. Check the corners once to account for cropping or
letterboxing. `capture` first waits for queued HID input to reach the target;
the display and encoder can still lag. Use an appropriate script `sleep`
after an action before capturing its result, and qualify latency on the
actual encoder. No latency bound or hardware validation is claimed yet.

## USB HDMI capture, including compatible Elgato devices

Leave `url` unset. Choose the device exposed by the operating system:

```sh
ffmpeg -f avfoundation -list_devices true -i ""
```

```toml
[capture]
format = "avfoundation"
device = "DEVICE_NAME_OR_INDEX"
size = "1920x1080"
framerate = 30
```

On Linux, use `format = "v4l2"` and the appropriate `device = "/dev/video0"`.
Select a size, frame rate and optional `pixel_format` that the device
supports. Compatibility depends on the particular capture model and driver;
the project has not qualified an Elgato device.

## Blackmagic HDMI input

Use Blackmagic Desktop Video and an FFmpeg build with the `decklink` input
enabled. A Thunderbolt enclosure does not change the capture configuration;
the card and input must appear to the driver. Discover the exact input name
and available modes using that FFmpeg executable:

```sh
ffmpeg -sources decklink
ffmpeg -f decklink -list_formats 1 -i "DEVICE_NAME"
```

```toml
[capture]
ffmpeg = "/path/to/decklink-enabled/ffmpeg"
format = "decklink"
device = "DEVICE_NAME"
format_code = ""
```

An empty `format_code` requests autodetection where the hardware supports
it; otherwise use a listed FourCC. `pixel_format`, when set, maps to
DeckLink's `raw_format` option. USB-specific `size` and `framerate` options
are omitted; the DeckLink mode defines them. Use the discovered device name
for each input of a multi-input card.
[FFmpeg DeckLink reference](https://ffmpeg.org/ffmpeg-devices.html#decklink).

## Verification

`make test` in `host/` covers input validation, transport selection, DeckLink
options, timeouts, partial-file cleanup and stream URL redaction. Its CLI
test runs through a simulated board and a fake capture process to verify
configuration reaches the snapshot command.

For real network decoding, supply an official
[MediaMTX v1.21.2 executable](https://github.com/bluenviron/mediamtx/releases/tag/v1.21.2)
and an FFmpeg build with `libx264`, AAC and the `lavfi` source:

```sh
HIDBRIDGE_TEST_MEDIAMTX=/absolute/path/to/mediamtx \
HIDBRIDGE_TEST_FFMPEG=ffmpeg make test-network
```

The optional test starts a temporary loopback-only server and two synthetic
video/audio publishers. It selects both feeds through RTSP and RTMP,
decodes the PNGs to check their colours and dimensions, then tests stalled
connections. All processes and temporary files are cleaned up. Downloads and build outputs stay in ignored `host/dist/` or an external scratch directory.
