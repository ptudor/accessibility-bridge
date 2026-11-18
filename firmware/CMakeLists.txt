cmake_minimum_required(VERSION 3.13)

set(PICO_BOARD pico CACHE STRING "Original Raspberry Pi Pico or Pico H")
set(PICO_PLATFORM rp2040 CACHE STRING "Target platform")

if(NOT PICO_SDK_PATH AND DEFINED ENV{PICO_SDK_PATH})
    set(PICO_SDK_PATH $ENV{PICO_SDK_PATH})
endif()
if(NOT PICO_SDK_PATH)
    message(FATAL_ERROR "Set PICO_SDK_PATH or run make deps and make")
endif()
include(${PICO_SDK_PATH}/external/pico_sdk_import.cmake)

project(accessibility_bridge C CXX ASM)
set(CMAKE_C_STANDARD 11)
set(CMAKE_CXX_STANDARD 17)

if(PICO_SDK_VERSION_STRING VERSION_LESS "2.3.1")
    message(FATAL_ERROR "Raspberry Pi Pico SDK 2.3.1 or later is required")
endif()
pico_sdk_init()

# protocol.h is the one definition of the firmware version; HELLO, IDENTITY,
# bcdDevice and the picotool program version all derive from it.
foreach(part MAJOR MINOR)
    file(STRINGS ${CMAKE_CURRENT_LIST_DIR}/protocol.h line REGEX "^#define HB_FW_${part} [0-9]+$")
    if(NOT line MATCHES "^#define HB_FW_${part} ([0-9]+)$")
        message(FATAL_ERROR "protocol.h must define HB_FW_${part}")
    endif()
    set(HB_FW_${part} ${CMAKE_MATCH_1})
endforeach()

set(HB_SOURCES
    main.c
    board.c
    bridge.c
    protocol.c
    usb.c
    descriptors.c
)
add_executable(hidbridge ${HB_SOURCES})
target_include_directories(hidbridge PRIVATE ${CMAKE_CURRENT_LIST_DIR})
set_source_files_properties(${HB_SOURCES} PROPERTIES COMPILE_OPTIONS
    -Wall -Wextra -Werror -Wshadow -Wundef -Wmissing-prototypes
    -Wstrict-prototypes -ffunction-sections -fdata-sections
)
target_link_libraries(hidbridge PRIVATE
    pico_stdlib
    pico_unique_id
    pico_bootrom
    hardware_irq
    hardware_uart
    hardware_watchdog
    tinyusb_device
)
pico_enable_stdio_usb(hidbridge 0)
pico_enable_stdio_uart(hidbridge 0)
pico_set_program_name(hidbridge "accessibility-bridge")
pico_set_program_version(hidbridge "${HB_FW_MAJOR}.${HB_FW_MINOR}")
pico_add_extra_outputs(hidbridge)
