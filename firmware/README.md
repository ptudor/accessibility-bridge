# TARGET firmware

The firmware runs on the original Raspberry Pi Pico H with the RP2040 and 2 MiB of flash. UART0 carries commands and acknowledgements at 1,000,000 baud. TinyUSB presents three interfaces to the target computer. The third interface shares an endpoint between the absolute pointer and consumer controls.

## Build

Install Git, CMake, Make or Ninja, an Arm embedded GCC toolchain with newlib, and a native C/C++ compiler. From the repository root, run the following commands. The initial build downloads dependencies and builds the SDK's host-side UF2 utility when needed.

```sh
make -C firmware deps
make firmware
```

The image is `firmware/dist/build/hidbridge.uf2`. The build fetches Pico SDK 2.3.1 at commit `079c6f39023649b154152db30f1d781e884879bc`, including its TinyUSB submodule. To reuse a checkout with initialized submodules, set `PICO_SDK_PATH` to its absolute path and run `make firmware`. Use `PICO_TOOLCHAIN_PATH` for a cross-compiler installed outside your search path.

## Flash and recover

1. Disconnect the Pico's TARGET USB cable. Hold BOOTSEL on the module while reconnecting it to a computer using a USB data cable.
2. Release BOOTSEL when the `RPI-RP2` volume appears. Copy `hidbridge.uf2` onto it. The ROM writes flash, disconnects the volume, and starts the application.
3. Connect the module to the target. The onboard green LINK LED lights after USB configuration. The keyboard, mouse, and pointer/consumer interfaces enumerate together.

BOOTSEL remains available if the application cannot start. A running bridge can also enter the loader with `hidbridgectl -port "$PORT" bootloader`. Pin the destination port when flashing. The development image carries a shared private-test USB ID. [Private testing and release builds](../docs/USB-IDENTITY.md)

## Carrier pin contract

| Purpose | Pico signal | Header pin | Behavior |
| --- | --- | ---: | --- |
| Reply UART | GP0 | 1 | TX toward CONTROL |
| Command UART | GP1 | 2 | RX from CONTROL |
| HELD | GP2 | 4 | Active high while configured with an input held |
| Flow control | GP3 | 5 | Low permits CONTROL transmission |
| LINK | GP25 | Onboard LED | Active high when the target is configured |

The firmware drives GP3 from the software receive-ring watermark. CTS# rises when the 256-byte ring reaches 128 bytes and falls once fewer than 96 remain. GP15 is available to the SDK's USB enumeration workaround for early RP2040 silicon. [Pin definitions](board_pins.h) · [Circuit and connector pinout](../hardware/README.md)

## Behavior and tests

The executor preserves report order across endpoints. A queued wait or synchronization completes after the preceding report reaches the target. On session loss the command watchdog clears held keys and buttons within 500 ms while input is active. A separate hardware watchdog recovers a stalled main loop.

```sh
make -C firmware test
```

This runs the core and TinyUSB adapter tests with address and undefined-behavior sanitizers, then compares frames and reports with the independent Go simulator. USB suspend keeps the RP2040 active. Suspend-current compliance, remote wakeup on the physical carrier, and its provisional 100 mA configuration descriptor need bench measurement. [Protocol details](../docs/PROTOCOL.md)
