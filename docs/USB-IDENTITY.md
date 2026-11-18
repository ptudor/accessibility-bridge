# TARGET USB identity

The firmware currently enumerates as `1209:0001` for private development. pid.codes reserves this non-unique pair for in-house testing. Devices using this identity must stay in the private test environment. The cable on CONTROL retains its manufacturer's USB identity. The RP2040 ROM bootloader retains Raspberry Pi's identity. [pid.codes test PID](https://pid.codes/1209/0001/)

The proposed product name is **Accessibility Bridge**. `1209:7A79` is a candidate for the publicly documented R1 carrier and its HID firmware. Assignment is pending. The registry requires licensed, editable hardware and firmware sources and accepts applications by pull request. [Application instructions](https://pid.codes/howto/) · [VID 1209 registry](https://pid.codes/1209/)

After the registry publishes an allocation for this project, update the VID and PID, record the allocation URL here, and set `HB_USB_IDENTITY_ASSIGNED` in `firmware/identity.h`. Run `make -C firmware release` to check the identity before producing a distribution image. The test identity fails that check. [USB identity definitions](../firmware/identity.h)
