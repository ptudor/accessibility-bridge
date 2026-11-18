KICAD_CLI ?= kicad-cli
PYTHON ?= python3
PROJECT := Accessibility-Bridge
OUT := dist

.PHONY: check fabrication

check:
	mkdir -p $(OUT)
	$(KICAD_CLI) sch erc --exit-code-violations -o $(OUT)/erc.rpt $(PROJECT).kicad_sch
	$(KICAD_CLI) sch export netlist --format kicadxml -o $(OUT)/netlist.xml $(PROJECT).kicad_sch
	$(PYTHON) check_pinout.py $(OUT)/netlist.xml ../firmware/board_pins.h
	$(KICAD_CLI) pcb drc --refill-zones --schematic-parity --exit-code-violations -o $(OUT)/drc.rpt $(PROJECT).kicad_pcb

fabrication: check
	mkdir -p $(OUT)/gerbers
	$(KICAD_CLI) pcb export gerbers --check-zones --layers F.Cu,B.Cu,F.Mask,B.Mask,F.Paste,F.Silkscreen,B.Silkscreen,Edge.Cuts -o $(OUT)/gerbers/ $(PROJECT).kicad_pcb
	$(KICAD_CLI) pcb export drill --excellon-separate-th -o $(OUT)/gerbers/ $(PROJECT).kicad_pcb
