"""Check the carrier interfaces shared by the firmware, Pico H, and CONTROL cable."""

import re
import sys
from pathlib import Path
from xml.etree import ElementTree


def check(netlist, pin_header):
    # Physical header numbering on the original Pico H. GP25 drives its onboard LED.
    gpio_by_pin = {1: 0, 2: 1, 4: 2, 5: 3}
    signals = {
        "TGT_TXD": ("UART_TX", 1, 3),
        "TGT_RXD": ("UART_RX", 2, 5),
        "TGT_RTS_N": ("RTS_N", 5, 4),
        "TGT_HELD": ("LED_HELD", 4, None),
    }
    pins = dict(re.findall(r"^#define HB_PIN_(\w+)\s+(\d+)$", pin_header.read_text(), re.M))
    nets = {
        net.get("name"): {(n.get("ref"), int(n.get("pin"))) for n in net.findall("node")}
        for net in ElementTree.parse(netlist).findall("./nets/net")
    }

    def require_pin(net, ref, pin):
        if (ref, pin) not in nets.get("/" + net, set()):
            raise ValueError(f"{ref} pin {pin} must connect to {net}")

    for net, (function, pico_pin, isolator_pin) in signals.items():
        if pins.get(function) != str(gpio_by_pin[pico_pin]):
            raise ValueError(f"HB_PIN_{function} must match Pico H pin {pico_pin}")
        require_pin(net, "U2", pico_pin)
        if isolator_pin is not None:
            require_pin(net, "U1", isolator_pin)

    if pins.get("LED_LINK") != "25":
        raise ValueError("LINK must drive the Pico H onboard LED at GP25")

    for pin, (net, isolator_pin) in enumerate(
        [("CTL_3V3", 16), ("CTL_GND", 15), ("CTL_RXD", 14),
         ("CTL_CTS_N", 13), ("CTL_TXD", 12)], start=1
    ):
        require_pin(net, "J1", pin)
        require_pin(net, "U1", isolator_pin)


if __name__ == "__main__":
    try:
        check(Path(sys.argv[1]), Path(sys.argv[2]))
    except ValueError as error:
        sys.exit(str(error))
    print("Pico GPIOs, CONTROL pinout, and isolator channels match.")
