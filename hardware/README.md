# R1 reference carrier

The carrier joins a Raspberry Pi Pico H to a 3.3 V USB UART through a TI ISO6731DWR. The target computer powers the Pico through its micro-USB port. The control cable powers the other side of the isolator. Three channels cross the barrier for commands, replies, and CTS# flow control. The two grounds have separate copper on both layers.

Open **Accessibility-Bridge.kicad_pro** in KiCad 10 with the standard footprint and 3D model libraries installed. The editable schematic, routed PCB, project symbols, custom footprints, and four part-specific STEP models are included. The board measures 58 × 56.5 mm and uses a 1.6 mm, two-layer stack. A design rule enforces 6 mm clearance between the two named domains. [KiCad project](Accessibility-Bridge.kicad_pro) · [Schematic](Accessibility-Bridge.kicad_sch) · [PCB](Accessibility-Bridge.kicad_pcb)

## Parts for one bridge

| Position | Qty | Manufacturer and part | Function |
| --- | ---: | --- | --- |
| U1 | 1 | Texas Instruments ISO6731DWR | SOIC-16 wide, two forward channels and one reverse, default-high outputs |
| U2 | 1 | Raspberry Pi Pico H, SC0917 | Original non-wireless RP2040 module with soldered headers |
| U2 sockets | 2 | Samtec SSW-120-01-G-S | 1 × 20, 2.54 mm pitch, 8.51 mm body height |
| J1 | 1 | JST B5B-XH-A(LF)(SN) | Keyed vertical five-position XH header, 2.50 mm pitch |
| C1, C2 | 2 | Yageo CC0805KRX7R9BB104 | 100 nF X7R, 0805, one per isolator supply |
| R1, R2 | 2 | Uni-Royal 0805W8F1002T5E | 10 kΩ, 1%, idle pull-ups on TARGET TX and RTS# |
| R3 | 1 | Uni-Royal 0805W8F1001T5E | 1 kΩ, 1%, HELD LED current limit |
| D1 | 1 | Everlight 17-21SURC/S530-A3/4T | Red 0805 indicator, cathode to pad 1 |
| CONTROL cable | 1 | FTDI TTL-232RG-VREG3V3-WE | USB-A to wire ends, regulated 3.3 V supply and 3.3 V UART with CTS# |
| Cable housing and contacts | 1 + 5 | JST XHP-5 and SXH-001T-P0.6 | Crimp to the five used cable conductors |
| TARGET cable | 1 | USB data cable with a micro-B plug | Connects the Pico to the target |

The Pico H supplies the flash, crystal, regulator, USB connector, BOOTSEL button, and green LINK LED. Its plug-in sockets are 17.78 mm apart. The board also has four unplated 2.7 mm mounting holes. [Pico module drawing and pinout](https://datasheets.raspberrypi.com/pico/pico-datasheet.pdf) · [Socket drawing](https://www.samtec.com/products/ssw-120-01-g-s)

## Wire the CONTROL connector

Pin numbers refer to **J1 on the PCB**. Pin 1 has a square copper pad. Match the housing cavity numbers before crimping. RXD, TXD, and CTS# below are named from the cable's point of view.

| J1 | Cable wire | Connection |
| ---: | --- | --- |
| 1 | Red, VCC | Regulated 3.3 V to U1 VCC2 and EN2 |
| 2 | Black, GND | CONTROL ground |
| 3 | Yellow, RXD | Replies from Pico GP0 through U1 channel A |
| 4 | Brown, CTS# | Receive permission from Pico GP3 through U1 channel B |
| 5 | Orange, TXD | Commands to Pico GP1 through U1 channel C |

Insulate the spare green RTS# wire. Check for 3.3 V between J1 pins 1 and 2 on the cable **before mating it**. Similar FTDI cables can have 3.3 V logic and a 5 V power lead. The specified cable supplies 3.3 V on both. [FTDI cable datasheet, section 4](https://ftdichip.com/wp-content/uploads/2023/07/DS_TTL-232RG_CABLES.pdf) · [JST XH drawing](https://www.jst-mfg.com/product/pdf/eng/eXH.pdf)

U1 side 1 faces the Pico. Target TX, RTS#, and RX connect to U1 pins 3, 4, and 5 respectively. U1 pins 14, 13, and 12 face J1. Each enable is tied to its local 3.3 V supply. With TARGET unpowered, the default-high isolator output deasserts the cable's CTS#. The default-high part and a cable with hardware flow control are essential to this behavior. [TI ISO6731 datasheet, pin functions and device functional modes](https://www.ti.com/lit/ds/symlink/iso6731.pdf)

## First power-up

1. With both cables disconnected, orient U1 by the pin-1 mark and D1 by its cathode. Solder the passives, J1, and the two parallel Pico sockets. Fit the Pico H with GP0 at square pad 1 and its USB connector at that end of the carrier.
2. Check that each supply is free of shorts to its own ground and that CONTROL and TARGET ground have no DC continuity. Connect CONTROL alone. Measure about 3.3 V across C1 and an unpowered rail across C2. Disconnect CONTROL and repeat with TARGET only. C2 should now be powered and C1 unpowered.
3. Flash the Pico and connect it to the target. The green LED should light after USB configuration. Connect CONTROL and run `hidbridgectl -port "$PORT" status`. Expect firmware 0.2, a configured USB device, and no held inputs. [Flashing instructions](../firmware/README.md)
4. Start with Caps Lock off and run the keyboard LED check. Open a blank document on the target and run the text-selection example. Check each physical effect against its session transcript. [Controller examples](../host/examples/)
5. For loss-of-link testing, hold Shift in an interactive `run -` session and unplug CONTROL while TARGET stays powered. HELD should extinguish after the 500 ms watchdog interval and the target should release Shift. Reconnect, read the watchdog count, and check that typing has returned to normal.

This layout awaits physical verification of harness fit, signal timing at 1 Mbaud, watchdog release, and active and suspend current. Connectors, debug leads, and test equipment can provide an external ground path. The board is designed for two USB-powered computer interfaces and has no system-level electrical safety certification.

## Check and export

```sh
make -C hardware check
make -C hardware fabrication
```

Run these commands at the repository root with KiCad 10, Make, and Python 3 on your path. The checks refill the ground zones, compare the PCB with the schematic, and verify the firmware GPIOs and cable pinout against the exported nets. Manufacturing plots and separate plated and non-plated drills go into `hardware/dist/gerbers/`. [3D model provenance](models/README.md)
