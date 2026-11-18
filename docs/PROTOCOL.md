# HID bridge serial protocol, version 1

The RP2040 firmware, Go controller, and simulator implement this wire protocol.

## Link

1 000 000 baud, 8N1, no parity, no software flow control. The board drives
the control computer's **CTS#** through isolator channel B: asserted (low)
while the firmware is running and its receive ring has room for a complete
frame; deasserted otherwise. The isolator's default-high output means an
unpowered target side reads as CTS# deasserted. The host enables
hardware output flow control, so a deasserted CTS# stops the USB UART rather
than losing bytes, and reads the modem status to report "target side not
running" instead of waiting for a timeout. The USB UART RTS# is not used.

## Framing

Each frame is a payload encoded with **COBS** (consistent overhead byte
stuffing, Cheshire and Baker 1999) followed by one **0x00** delimiter. The
receiver splits the byte stream on 0x00; an empty frame (two delimiters in a
row) is ignored and is the resynchronisation idle. A sender may emit a lone
0x00 before its first frame so a receiver that started mid-stream discards
the partial frame.

Payload, before COBS:

| Offset | Size | Field |
| ---: | ---: | --- |
| 0 | 1 | `seq` — sequence number, see below |
| 1 | 1 | `type` |
| 2 | 0–64 | `body`, length fixed per `type` unless stated |
| end | 2 | CRC-16 over `seq`, `type` and `body`, big-endian |

CRC-16/CCITT-FALSE: polynomial 0x1021, initial value 0xFFFF, no reflection,
no final XOR. Check value: the ASCII bytes `123456789` give **0x29B1**.

Limits: body ≤ 64 bytes, so the payload is ≤ 68 bytes and the frame on the
wire is ≤ 70 bytes (COBS adds one byte below 254). A receiver discards any
frame longer than 70 bytes before the delimiter, and any decoded payload
shorter than 4 bytes.

## Sequence numbers and acknowledgement

The host numbers its command frames with an 8-bit `seq` that increments by
one per frame and wraps. The board answers **every** command frame with
exactly one response frame carrying the **same `seq`**: `ACK` or `NAK`.
Responses are never acknowledged.

The board keeps `expected`, the next `seq` it will accept:

- `HELLO` is accepted with any `seq` and sets `expected = seq + 1`.
- Any other command with `seq == expected` is accepted and `expected`
  advances.
- A command whose `seq` is one of the **eight** values before `expected`
  (`expected-8 … expected-1`, modulo 256) is a retransmission the board has
  already received. The board **repeats its original response without
  executing it again**, retaining the last eight consumed responses. This
  includes `NAK TYPE`, `NAK LENGTH` and `NAK STATE`: losing a refusal must
  never turn it into a successful acknowledgement on retry. `WAKEUP` keeps
  its original `issued` value. `STATUS` and `IDENTITY` may be executed again
  since they change nothing. A sequence with no retained response is
  refused with `NAK SEQUENCE`.
- Any other `seq`, or any command before the first `HELLO` since reset, is
  refused with `NAK SEQUENCE` carrying `expected`, and the board's state does
  not change.

Whether a refusal consumes the sequence number is fixed per code, so the two
sides never disagree about `expected`:

- `NAK QUEUE_FULL` does **not** advance `expected`. The host sends the same
frame again, with the same `seq`, after a short pause.
- `NAK TYPE`, `NAK LENGTH` and `NAK STATE` **do** advance `expected`: the
  command is consumed and not executed, and the host moves on to its next
  sequence number.
- `NAK FRAMING`, `NAK CRC`, `NAK SEQUENCE` and `NAK NO_SESSION` consume
  nothing.

Type and length are checked after the sequence check, so a malformed frame
with a stale `seq` is answered `NAK SEQUENCE`, and a malformed duplicate is
answered with its original response without execution like any other duplicate.

The host keeps at most **8** command frames unacknowledged (its window). A
frame unacknowledged after the host's timeout (1 s by default) is sent again
with the same `seq`, up to five times; then the host declares the link lost
and stops. Because the window is at most 8 and the duplicate window is 8,
a late acknowledgement and a retransmission can never be confused, provided
the host keeps the entire sequence range within eight: acknowledgements
behind a missing response do not open new window slots until the missing
response arrives.

Board events (types 0x90 and above) carry the board's **own** 8-bit
counter in `seq`, incremented per event. A gap tells the host that events
were lost; `STATUS` recovers the full state. Events are not acknowledged.

## Immediate and queued commands

**Immediate** commands execute when the frame is parsed. **Queued**
commands are acknowledged when they are placed on the board's execution
queue and execute later, in order, with the board's own timing. The queue
holds **16** entries (reported in the `HELLO` acknowledgement). A queued
command arriving while the queue is full is refused with `NAK QUEUE_FULL`
and the host sends it again after a short pause; the frame is not lost and
later sequence numbers are retried in order. Retrying the refused frame
feeds the watchdog while the queue drains; the host need not advance to a
new `HEARTBEAT` sequence to keep the session alive.

Queued **input** commands (`KEY_DOWN`, `KEY_UP`, `KEY_REPORT`, `MOUSE`,
`POINTER`, `CONSUMER`) are refused with `NAK STATE` unless the target has
configured the device, or the device is suspended with remote wakeup
enabled (the board then wakes the target first, see *Suspend*).

## Watchdog and heartbeat

The board holds a **held set**: keyboard modifiers and keys, mouse buttons,
pointer buttons and the current consumer usage. While the held set is
non-empty **or** the queue is non-empty (including a currently executing
`WAIT`), the board requires a valid command
frame of any type at least every **500 ms**. Any frame that passes the
sequence check feeds the watchdog, including a duplicate and one refused
with `QUEUE_FULL` or `STATE`. When that lapses it releases
everything on every interface, empties the queue, turns off HELD, and sends
`EVENT_RELEASED` with reason `WATCHDOG`. The red HELD LED is lit exactly
while the held set is non-empty.

The host sends `HEARTBEAT` every **100 ms** for the whole time a session is
open, whether or not it believes anything is held. The board does not
require it while idle, so an idle board and a closed session are both
quiet.

## Session

A session is `HELLO` through the host closing the port. `HELLO` releases
everything, empties the queue and resets `expected`; nothing a previous
session held survives it. Before closing, the host sends `RELEASE_ALL` and
waits for its acknowledgement; if the host dies instead, the watchdog does
the same within 500 ms.

## Commands (host to board)

| Type | Name | Class | Body | ACK body |
| ---: | --- | --- | --- | --- |
| 0x01 | `HELLO` | immediate | `version` u8 = 1 | `version` u8, `fw_major` u8, `fw_minor` u8, `queue` u8 (capacity, 16) |
| 0x02 | `STATUS` | immediate | none | *Status*, 14 bytes, below |
| 0x03 | `IDENTITY` | immediate | none | ASCII, 1–64 bytes: `accessibility-bridge R<pcb> fw <major>.<minor> sn <serial>` |
| 0x10 | `KEY_DOWN` | queued | `usage` u8, keyboard page 0x07 | none |
| 0x11 | `KEY_UP` | queued | `usage` u8 | none |
| 0x12 | `KEY_REPORT` | queued | 8 bytes, the boot keyboard input report | none |
| 0x20 | `MOUSE` | queued | `buttons` u8, `dx` i8, `dy` i8, `wheel` i8 | none |
| 0x21 | `POINTER` | queued | `buttons` u8, `x` u16, `y` u16, `wheel` i8 | none |
| 0x22 | `CONSUMER` | queued | `usage` u16, `action` u8 (0 release, 1 press) | none |
| 0x30 | `RELEASE_ALL` | immediate | none | none |
| 0x31 | `WAIT` | queued | `ms` u16, 1–10000 | none |
| 0x32 | `SYNC` | queued | none | none; `EVENT_DONE` follows on execution |
| 0x33 | `HEARTBEAT` | immediate | none | none |
| 0x40 | `USB_DETACH` | immediate | none | none |
| 0x41 | `USB_ATTACH` | immediate | none | none |
| 0x42 | `WAKEUP` | immediate | none | `issued` u8 (1 if a remote wakeup was signalled) |
| 0x43 | `BOOTLOADER` | immediate | none | none; the board then jumps to the bootloader and the link ends |

Multi-byte integers are little-endian. Unknown types are refused with
`NAK TYPE`; a body of the wrong length with `NAK LENGTH`.

Semantics:

- **`KEY_DOWN` / `KEY_UP`.** Usages 0xE0–0xE7 (Left Control through Right
  GUI) set or clear the corresponding modifier bit (bit 0 = Left Control,
  bit 7 = Right GUI; HID 1.11 Appendix B.1). Any other usage is added to or
  removed from the six-slot key array. A seventh simultaneous key is
  dropped and counted in `Status.dropped`; the host tool refuses to send it.
  Each command produces its own input report, so a press and its release
  are never merged into one poll. The report is sent
  on the boot keyboard interface in boot or report protocol alike.
- **`KEY_REPORT`.** Replaces the whole keyboard state with the given report:
  byte 0 modifiers, byte 1 reserved (sent as zero), bytes 2–7 key codes.
  The held set follows it.
- **`MOUSE`.** The boot mouse interface. `buttons` is the complete button
  state: bit 0 left, bit 1 right, bit 2 middle. `dx`, `dy` and `wheel` are
  relative, −127…127 (−128 is clamped). One report per command.
- **`POINTER`.** The absolute pointer on interface 2, report ID 1. `x` and
  `y` are 0–32767 across the target's whole desktop; the same button bits
  as `MOUSE`; `wheel` relative. One report per command. The pointer and
  the boot mouse have separate button state; the host tool keeps them
  consistent.
- **`CONSUMER`.** Interface 2, report ID 2, one 16-bit usage from the
  consumer page 0x0C (HUT 1.6 §15), 0x0001 to 0x03FF: the report
  descriptor declares no usage above that and the target ignores one. The
  board passes the value through, as it does a pointer coordinate.
  `press` sends a report with the usage;
  `release` sends a report with usage 0. A press replaces any usage already
  pressed.
- **`RELEASE_ALL`.** Empties the queue, then sends a zero report on every
  interface that has anything held, then `EVENT_RELEASED` reason `COMMAND`.
- **`WAIT`.** Holds the queue for `ms` milliseconds. Longer waits are
  several frames; the host tool splits them. Zero or values above 10000 are
  refused with `NAK STATE` and consume the sequence number.
- **`SYNC`.** Executes as a no-op and sends `EVENT_DONE` carrying the
  `SYNC` frame's `seq`. The host uses it to know the queue has reached that
  point, for example before capturing a frame of the target's display.
- **`USB_DETACH` / `USB_ATTACH`.** Detach releases everything, empties the
  queue and removes the D+ pull-up so the target sees an unplug; attach
  restores it. Both report through `EVENT_USB`.
- **`WAKEUP`.** If the device is suspended and the target enabled remote
  wakeup, signal it and answer `issued = 1`; otherwise `issued = 0`.
- **`BOOTLOADER`.** Acknowledge, release everything, detach, then jump to
  the RP2040 ROM bootloader, which accepts UF2 files over USB mass storage. The bootloader
  appears on the TARGET connector.

### Status (14 bytes)

| Offset | Size | Field |
| ---: | ---: | --- |
| 0 | 1 | `usb_state`: 0 `DETACHED`, 1 `ATTACHED` (powered, not configured), 2 `CONFIGURED`, 3 `SUSPENDED` |
| 1 | 1 | `flags`: bit 0 remote wakeup enabled by the target, bit 1 held set non-empty, bit 2 keyboard interface in boot protocol, bit 3 mouse interface in boot protocol |
| 2 | 1 | `leds`: the last keyboard output report from the target (bit 0 Num Lock, 1 Caps Lock, 2 Scroll Lock, 3 Compose, 4 Kana) |
| 3 | 1 | `queue`: entries waiting |
| 4 | 4 | `uptime` seconds since reset, u32 |
| 8 | 2 | `watchdog`: releases by the watchdog since reset, u16 |
| 10 | 2 | `rx_errors`: frames refused for framing, CRC, sequence, type or length since reset, u16 |
| 12 | 2 | `dropped`: key presses dropped for a full key array since reset, u16 |

## Responses (board to host)

| Type | Name | Body |
| ---: | --- | --- |
| 0x80 | `ACK` | per command, above |
| 0x81 | `NAK` | `code` u8, `expected` u8 |

`NAK` codes: 1 `FRAMING` (COBS or length), 2 `CRC`, 3 `SEQUENCE`,
4 `TYPE`, 5 `LENGTH`, 6 `QUEUE_FULL`, 7 `STATE`, 8 `NO_SESSION` (no `HELLO`
since reset). For `FRAMING` and `CRC` the offending `seq` is unknown, so the
frame's `seq` is 0 and the host sends its oldest unacknowledged frame again
at once; for every other code the `seq` is the refused command's.

## Events (board to host)

`seq` is the board's event counter.

| Type | Name | Body |
| ---: | --- | --- |
| 0x90 | `EVENT_USB` | `state` u8 as in *Status*; `cause` u8: 0 power, 1 bus reset, 2 configured, 3 suspend, 4 resume, 5 `USB_DETACH`, 6 `USB_ATTACH`, 7 remote wakeup |
| 0x91 | `EVENT_PROTOCOL` | `interface` u8 (0 keyboard, 1 mouse), `protocol` u8 (0 boot, 1 report) |
| 0x92 | `EVENT_LEDS` | `leds` u8, as in *Status*; sent whenever the target writes the output report |
| 0x93 | `EVENT_WAKEUP` | `enabled` u8, sent when the target changes the remote-wakeup feature |
| 0x94 | `EVENT_DONE` | `seq` u8 of the executed `SYNC` |
| 0x95 | `EVENT_RELEASED` | `reason` u8: 1 `WATCHDOG`, 2 `BUS_RESET`, 3 `SUSPEND`, 4 `DETACH`, 5 `COMMAND` |
| 0x96 | `EVENT_HELD` | `held` u8, 0 or 1, whenever the held set becomes empty or non-empty (the HELD LED) |

A bus reset, suspend or detach releases everything first, reported by `EVENT_RELEASED`, then the `EVENT_USB`.

## Suspend

While suspended with remote wakeup enabled, the first queued input command
makes the board signal remote wakeup, then wait for the target to resume
before executing the queue. If the target has not resumed within 2 s the
queue is emptied and `EVENT_RELEASED` reason `SUSPEND` is sent. While
suspended without remote wakeup, input commands are refused with
`NAK STATE`. The RP2040 stays active during suspend.

## HID personality the firmware presents

For reference; the host tool does not depend on it beyond the units above.

| Interface | Descriptor | Reports |
| --- | --- | --- |
| 0 | Boot keyboard, HID 1.11 Appendix B.1 | IN 8 bytes: modifiers, reserved, six key codes. OUT 1 byte: LEDs |
| 1 | Boot mouse, HID 1.11 Appendix B.2 extended with a wheel | IN 4 bytes in report protocol: buttons, X, Y, wheel (−127…127); 3 bytes in boot protocol: buttons, X, Y |
| 2 | Report IDs | ID 1 absolute pointer: buttons u8, X u16 (0–32767), Y u16 (0–32767), wheel i8. ID 2 consumer control: usage u16 (0–0x03FF) |

All three interrupt IN endpoints poll at 1 ms. The
keyboard LED report is relayed as `EVENT_LEDS` and kept in *Status*. When a
release clears pointer buttons, the firmware's report keeps the last absolute
position (the simulator prints zeros; nothing on the host reads them).
HID reset selects report protocol; the target selects boot protocol with
`SET_PROTOCOL`. The simulator's default boot flags are historical; firmware
uses the HID reset default and reports actual target selections.

## References

- [USB-IF HID Usage Tables 1.6](https://usb.org/sites/default/files/hut1_6.pdf):
  §4 Generic Desktop (X 0x30, Y 0x31, Wheel 0x38), §10 Keyboard/Keypad page
  0x07 (p. 89), §11 LED page 0x08 (p. 97), §12 Button page 0x09 (p. 110),
  §15 Consumer page 0x0C (p. 125).
- [USB-IF Device Class Definition for HID 1.11](https://www.usb.org/sites/default/files/hid1_11.pdf):
  Appendix B boot interface descriptors and reports (pp. 59–61).
- S. Cheshire and M. Baker, *Consistent Overhead Byte Stuffing*, IEEE/ACM
  Transactions on Networking 7(2), 1999.
