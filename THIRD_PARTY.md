# Third-party notices

The Go module and firmware build fetch dependencies separately. The following license files accompany this source so developers can find the notices when packaging a build.

| Component | Use | Upstream notice |
| --- | --- | --- |
| Raspberry Pi Pico SDK 2.3.1 | RP2040 startup, hardware drivers, and ROM API | [BSD-3-Clause](LICENSES/pico-sdk.txt) |
| TinyUSB at SDK submodule revision `86ad6e56c1700e85f1c5678607a762cfe3aa2f47` | USB device stack | [MIT](LICENSES/tinyusb.txt) |
| go-toml/v2 v2.4.3 | Controller configuration | [MIT](LICENSES/go-toml.txt) |
| golang.org/x/sys v0.48.0 | Serial drivers and pseudo-terminals | [BSD-3-Clause](LICENSES/x-sys.txt) |

The Go toolchain, Arm toolchain, C runtime, and SDK subcomponents carry their own notices. FFmpeg runs as a separate program for capture. MediaMTX is an optional test dependency.

The KiCad Library Team provides the standard passive and mounting-hole symbols, footprints, and library 3D models under CC-BY-SA-4.0 with the KiCad design exception. The project-local isolator, capacitor, and two resistor footprints adapt the KiCad 10.0.8 standard land patterns by binding part-specific models. Those four library files retain the upstream license and exception. The Pico H carrier footprint, bridge symbols, and four bundled STEP models are original project files. [KiCad libraries license and exception](https://www.kicad.org/libraries/license/) · [CC-BY-SA-4.0](LICENSES/CC-BY-SA-4.0.txt)
