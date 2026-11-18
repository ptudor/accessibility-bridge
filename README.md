# Accessibility Bridge

Accessibility Bridge lets you operate a second computer with a keyboard and mouse controlled from the command line. You can replay a keyboard-only interaction, check how a dialog handles focus, or repeat a pointer gesture at the same screen coordinates. The target uses its built-in USB HID drivers. All operator software runs on the control computer.

```text
CONTROL computer                                  TARGET computer
hidbridgectl → USB UART → digital isolator → Pico H → USB HID
                        ← replies and CTS#
```

The Pico presents a boot keyboard, a relative mouse, an absolute pointer, and consumer controls. The two computers power their respective sides of the isolator. To see the target display from CONTROL, connect a separate video capture device or encoder. [Display capture guide](docs/CAPTURE.md)

## Try it without hardware

On macOS or Linux, install Go 1.26 or newer and Make. From the repository root, build the controller and start the simulator.

```sh
make host
host/bin/hidbridge-sim
```

In a second terminal at the repository root, set `PORT` to the device path printed by the simulator. On Linux it usually looks like `/dev/pts/3`. Each invocation below opens a fresh session.

```sh
PORT=/dev/pts/3
host/bin/hidbridgectl -port "$PORT" -json status
host/bin/hidbridgectl -port "$PORT" run host/examples/text-selection.hid
```

The status includes `"usb": "configured"` and an identity ending in `SIMULATED`. Watch the simulator terminal for the individual key presses and releases. The example types “Accessibility Bridge” and selects the final letter. With a physical board, open a blank text document on the target first.

## Build a bridge

| What you need | Where to start |
| --- | --- |
| Carrier, cable, and parts | [KiCad project, BOM, wiring, and first power-up](hardware/README.md) |
| Image for the Pico H | [Build and flash the TARGET firmware](firmware/README.md) |
| Commands and scripts for CONTROL | [Controller and simulator](host/README.md) |
| An implementation in another language | [Serial protocol v1](docs/PROTOCOL.md) |

The R1 carrier has passed KiCad electrical, layout, and schematic-parity checks. Physical assembly and bench qualification of this reference layout are pending. The firmware builds for the original RP2040 Pico H. Its default USB identity is the shared private-test pair `1209:0001`. This project has no assigned PID. [USB identity and release status](docs/USB-IDENTITY.md)

## Verification

```sh
make test            # Go race tests, CLI integration, native C tests, C/Go comparison
make hardware-check  # KiCad 10 electrical and design rules, including schematic parity
```

The native tests exercise HID descriptors, report delivery, retries, queue backpressure, bus events, and release after a lost control link. Hardware bring-up also requires observing actual input and measuring current on each USB port. [Bench procedure](hardware/README.md#first-power-up)

Original software is MIT licensed. The reference hardware is licensed under CERN-OHL-P-2.0. [License](LICENSE) · [Third-party notices](THIRD_PARTY.md)
