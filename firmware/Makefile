SHELL := /bin/sh
.DELETE_ON_ERROR:

SDK_TAG := 2.3.1
SDK_REV := 079c6f39023649b154152db30f1d781e884879bc
SDK_DIR := dist/deps/pico-sdk
PICO_SDK_PATH ?= $(abspath $(SDK_DIR))
PICO_TOOLCHAIN_PATH ?=
SDK_DEP := $(if $(filter $(abspath $(SDK_DIR)),$(abspath $(PICO_SDK_PATH))),$(SDK_DIR)/.verified)
BUILD_DIR ?= dist/build
CC_HOST ?= cc
GO ?= go
CMAKE ?= cmake
PICOTOOL ?= picotool
CORE := protocol.c bridge.c
HOST_FLAGS := -std=c11 -O1 -g -Wall -Wextra -Werror -Wshadow -Wstrict-prototypes -I. \
              -fsanitize=address,undefined -fno-sanitize-recover=all -fno-omit-frame-pointer

.PHONY: all deps configure firmware test check clean identity-assigned release flash
all: firmware

dist:
	mkdir -p $@

deps: $(SDK_DIR)/.verified

$(SDK_DIR)/pico_sdk_init.cmake: | dist
	mkdir -p dist/deps
	git clone --depth 1 --branch $(SDK_TAG) --recurse-submodules --shallow-submodules \
		https://github.com/raspberrypi/pico-sdk.git $(SDK_DIR)

$(SDK_DIR)/.verified: $(SDK_DIR)/pico_sdk_init.cmake
	test "$$(git -C $(SDK_DIR) rev-parse HEAD)" = "$(SDK_REV)"
	touch $@

configure: $(SDK_DEP)
	$(CMAKE) -S . -B $(BUILD_DIR) -DPICO_SDK_PATH=$(PICO_SDK_PATH) \
		$(if $(PICO_TOOLCHAIN_PATH),-DPICO_TOOLCHAIN_PATH=$(PICO_TOOLCHAIN_PATH),) \
		-DPICO_BOARD=pico -DPICO_PLATFORM=rp2040

firmware: configure
	$(CMAKE) --build $(BUILD_DIR) --target hidbridge

dist/test_core: $(CORE) descriptors.c tests/board_fake.c tests/core.c $(wildcard *.h) | dist
	$(CC_HOST) $(HOST_FLAGS) $(CORE) descriptors.c tests/board_fake.c tests/core.c -o $@

dist/test_usb: $(CORE) usb.c tests/usb.c tests/fake/tusb.h tests/fake/pico/stdlib.h $(wildcard *.h) | dist
	$(CC_HOST) $(HOST_FLAGS) -Itests/fake $(CORE) usb.c tests/usb.c -o $@

dist/native: $(CORE) tests/native.c $(wildcard *.h) | dist
	$(CC_HOST) $(HOST_FLAGS) $(CORE) tests/native.c -o $@

test: dist/test_core dist/test_usb dist/native
	./dist/test_core
	./dist/test_usb
	$(CMAKE) -E make_directory dist/go-cache dist/go-tmp
	cd ../host && GOCACHE=$(abspath dist/go-cache) GOTMPDIR=$(abspath dist/go-tmp) \
		HIDBRIDGE_FIRMWARE_TEST=$(abspath dist/native) $(GO) test -count=1 -run Firmware ./internal/sim

check: test firmware

identity-assigned:
	$(CC_HOST) -E -x c -I. -DHB_REQUIRE_ASSIGNED_IDENTITY identity.h -o /dev/null

# Development images use the shared pid.codes private-test identity until allocation.
release: identity-assigned firmware

# The board must already be in the ROM bootloader: hold BOOTSEL while connecting
# TARGET USB, or send the BOOTLOADER protocol command. The application exposes no
# picoboot reset interface, so picotool cannot force it there by itself.
flash: firmware
	@command -v $(PICOTOOL) >/dev/null 2>&1 || { \
		echo "No USB-capable picotool found; copy $(BUILD_DIR)/hidbridge.uf2 to the RPI-RP2 drive instead"; \
		exit 1; \
	}
	$(PICOTOOL) load -x $(BUILD_DIR)/hidbridge.uf2

clean:
	$(CMAKE) -E remove_directory $(BUILD_DIR)
