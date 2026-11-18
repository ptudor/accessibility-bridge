# CONTROL software

`hidbridgectl` opens the CONTROL serial port, sends input, and reads device status. `hidbridge-sim` provides the same protocol on a pseudo-terminal and prints the HID reports that a target would receive. The serial backends support macOS, Linux, and FreeBSD. The interactive pseudo-terminal simulator supports macOS and Linux.

```sh
cd host
make build
bin/hidbridgectl keys
bin/hidbridgectl -port "$PORT" status
```

Set `PORT` to your USB UART or the port printed by the simulator. On Linux, a stable adapter name is typically available under `/dev/serial/by-id/`. A port glob must match exactly one device. Use `-config` to select a local TOML file. [Configuration example](hidbridge.toml.example)

## Drive an interaction

In a blank document on the target, the following script types a line and selects its last letter. Run it with `bin/hidbridgectl -port "$PORT" run examples/text-selection.hid`. A script keeps one session open through the entire interaction.

```text
expect usb configured
type "Accessibility Bridge"
key shift+left
sync
expect held no
```

| Action | Command |
| --- | --- |
| Type with a US keyboard layout | `type "Bridge ready"` |
| Move focus forward or backward | `key tab`, `key shift+tab` |
| Hold a modifier through a gesture | `down shift`, then the gesture, then `up shift` |
| Move by a relative distance | `move 40 0` |
| Click a pixel or drag between pixels | `click 320 240`, `drag 320 240 520 240` |
| Press and release a consumer usage | `consumer volume_up` |
| Wait in the board queue | `wait 150ms` |
| Observe keyboard state reported by the target | `expect leds caps=on` |
| Release every held input | `release` |

Set `[target] screen` to the target desktop dimensions for pixel coordinates. Use `hidbridgectl keys` for key and consumer names and `hidbridgectl -h` for the full command list. The example board check starts with Caps Lock off, tests keyboard LED feedback, and restores it. [Board check](examples/check.hid)

## Watch the target display

Configure a separate HDMI capture device or an RTSP or RTMP encoder, then focus a blank editor on the target. The following script saves one frame before typing and one after a short display-settling delay.

```sh
bin/hidbridgectl -port "$PORT" -config capture.local.toml -session text-entry run - <<'END'
capture before
type "Bridge ready"
sync
sleep 300ms
capture after
END
```

Adjust the delay for the display and capture device. The JSONL transcript records actions, board events, and capture paths. `run` writes a transcript by default. Add `-session NAME` to record a one-shot command. [Capture setup and supported inputs](../docs/CAPTURE.md)

## Session behavior

A command-line invocation releases held input when it closes the port. Use a script file or `run -` for a sequence that needs a continuous hold. Before opening a physical port, the tool validates the command. During a session it sends heartbeats every 100 ms. Exit status is 0 on success, 1 for an unmet expectation, 2 for a command error, and 130 on interruption.

The simulator can serve raw protocol frames on stdin and stdout with `-stdio`. For example, `hidbridge-sim -usb suspended -wakeup -manual-resume` models a target that permits remote wakeup but stays asleep. [Wire protocol](../docs/PROTOCOL.md)
