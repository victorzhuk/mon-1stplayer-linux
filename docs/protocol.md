# 1STPLAYER display protocol

This contract describes the active page-2 encoder in the supplied Windows application.
It defines the Linux encoder target, not a universal protocol for devices with the same USB IDs.

## Evidence and scope

| Artifact | Identity |
| --- | --- |
| Installer | [`1ST PC Monitoring Setup.msi`](1ST%20PC%20Monitoring%20Setup.msi) |
| Installer SHA-256 | `720fbbfd4a7047c386dc2dd8bb32b3700ed26809f375ed3a0fb87c8331daa764` |
| Application | `1ST PC Monitoring.exe`, product `1ST PC Monitoring V5.9`, file version `1.0.0.0` |
| Application SHA-256 | `cfc1bd0d924a8b3c455d6c306bfe81d5059cb0aeb2e95fa88bbf26e2e5624b8f` |

Static analysis establishes payload construction, glyph tables, counters, and runtime settings.
Read-only sysfs inspection establishes the connected device identity and report descriptor.
The golden payloads below are independently derived from the static encoder, not captured USB traffic.

**VID:PID `5131:2007` is reused by incompatible firmware**, including other displays, relay boards, and watchdogs.
Matching IDs or report sizes alone does not establish compatibility with this dialect.
For example, [cooler-lcd-linux](https://github.com/coldwelderx/cooler-lcd-linux) uses different refresh commands and metric offsets with these IDs.

## Hardware and transport

Observed identity:

- USB VID `0x5131`, PID `0x2007`, revision `0x0000`.
- HID bus `0003` (USB), name `HID 5131:2007`, empty unique identifier.
- Observation used `/dev/hidraw3`, under USB device `1-7.1`. These paths are not stable identifiers.
- Interrupt OUT endpoint `0x02` and interrupt IN endpoint `0x82` carry 64-byte reports.

Implementation note: the HID device uevent reports `HID_ID=0003:00005131:00002007`.
The bus uses four hexadecimal digits. Vendor and product each use eight.
Read identity from `/sys/class/hidraw/<hidraw>/device/uevent`.
The class uevent at `/sys/class/hidraw/<hidraw>/uevent` contains only `MAJOR`, `MINOR`, and `DEVNAME`, not HID identity.

The exact 34-byte HID report descriptor is:

```text
06 00 ff 09 01 a1 01 09 02 15 00 26 00 ff 75 08 95
40 81 06 09 02 15 00 26 00 ff 75 08 95 40 91 06 c0
```

| Descriptor property | Value |
| --- | --- |
| Usage page | Vendor-defined `0xff00` |
| Application collection usage | `0x01` |
| Input and Output usage | `0x02` |
| Report IDs | None |
| Input report | 64 fields, 8 bits each, 64 bytes |
| Output report | 64 fields, 8 bits each, 64 bytes |
| Field flags | `0x06`: Data, Variable, Relative |
| Logical range | `0` to `65280` (`26 00 ff` encodes `0xff00`, not `255`) |
| Feature reports | None |

The Linux implementation checks the complete sysfs descriptor, then validates the opened fd.
`HIDIOCGRDESCSIZE` must return exactly `34` before descriptor bytes are read and compared.
A longer descriptor with the same 34-byte prefix is refused.

The opened hidraw device has a nonblocking advisory `flock`, held until fd closure.
Contention refuses acquisition with `display is already in use` and exit status `2`, without writes.
This excludes only cooperating current binaries on the same device inode, not arbitrary external writers.

An encoder returns **64 payload bytes**.
A Linux `hidraw` write supplies **65 bytes**: a leading `0x00` Report ID placeholder, then the complete payload.
For this unnumbered USB report, the kernel removes the placeholder before transmitting the 64-byte USB report.
The leading placeholder is not the sequence byte.
A successful complete write returns `65`.
Do not write the 64-byte payload directly to `hidraw`.

The [official Linux hidraw documentation](https://docs.kernel.org/hid/hidraw.html) defines this prefix under `write()`.
Input reads differ: an unnumbered input report has no extra Report ID prefix.
The application does not require a reply or acknowledgement for refresh frames.

## Payload layout

All offsets below are zero-based **payload** offsets, excluding the Linux Report ID placeholder.
Start every payload with 64 zero bytes.
There is no checksum, terminator, ASCII field, or multibyte numeric field in the active payload.

| Offset | Length | Refresh contents |
| --- | --- | --- |
| `0` | 1 | Sequence byte |
| `1` | 1 | Command `0x00` |
| `2` | 1 | Page `0x02` |
| `3` | 1 | Reserved `0x00` |
| `4..19` | 16 | Legacy digit fields, all zero in this build |
| `20..23` | 4 | Unused, zero |
| `24..39` | 16 | Fixed `TM1721_show1` block below |
| `40..55` | 16 | Live `TM1721_show4` block |
| `56..63` | 8 | Padding, zero |

The legacy fields originally represent four decimal values at `4..7`, `8..11`, `12..15`, and `16..19`.
Their input, `ValueArray`, remains zero in the active application.
Live metrics come directly from `ClsMain` fields and occur only at `40..55`.
Neither TM1721 block is optional.
The unused page-1 builder is outside this contract.

### Fixed TM1721 block

Copy these bytes unchanged to offsets `24..39`:

```text
fd 08 08 08 fd 08 dd 0f d0 df 8f 00 00 00 00 00
```

This block includes blanking substitutions, glyph nibble packing, and punctuation masks.
It is not sixteen zeroes or raw decimal digits.
Its index `7` is `0x0f`, not `0x8f`: `TM1721_show1` explicitly clears bit 7 for this input.
A Linux encoder does not need to recreate the inactive legacy transformations.

## Live metric encoding

The encoder inputs are integer CPU temperature in Celsius, GPU temperature in Celsius, CPU utilization, GPU utilization, and one display mode.
The three display modes are Celsius (`0`), Fahrenheit (`1`), and utilization (`2`).
The CPU uses the source's `smgl*` digit group, and the GPU uses `smgr*`.
The names mean logical left and right groups, not physically validated faceplate positions.

Let `L[0..15]` be a zeroed live block copied to payload offsets `40..55`.
Every operation below bitwise-ORs a mask into `L[index]`.
Do not overwrite an earlier digit, icon, or bar bit.
Indices `13..15` stay zero.
There is no nibble swap in this block.

### Mode selection and numbers

| Mode | CPU digits | GPU digits | Bar inputs |
| --- | --- | --- | --- |
| Celsius | CPU Celsius | GPU Celsius | Celsius temperatures |
| Fahrenheit | `floor(CPU Celsius * 1.8 + 32)` | `floor(GPU Celsius * 1.8 + 32)` | Celsius temperatures, not Fahrenheit |
| Utilization | CPU utilization | GPU utilization | Utilization values |

For each selected number:

1. Clamp numbers above `999` to `999`.
2. Draw the hundreds digit only when the number is at least `100`.
3. Draw the tens digit only when the number is at least `10`.
4. Draw the units digit only when the number is nonnegative.

Thus zero draws one zero, while a negative number draws no digits and no minus sign.
Icons and bars remain independent of digit visibility.
The Windows metric collector floors Celsius values before deriving Fahrenheit.
`floor` means mathematical floor, including for negative values, not truncation toward zero.

### Digit glyph tables

Each cell lists the live-block indices that receive the column's mask for that digit.
Indices are decimal. Masks are hexadecimal.
An omitted hundreds or tens digit adds no bits.

| Digit | CPU hundreds `0x01` (`smgl3`) | CPU tens `0x02` (`smgl2`) | CPU units `0x04` (`smgl1`) |
| --- | --- | --- | --- |
| 0 | 0,2,12,10,8,4 | 12,4,2,6,0,10 | 2,10,4,12,8,0 |
| 1 | 2,12 | 4,2 | 10,4 |
| 2 | 0,2,6,8,10 | 12,4,8,0,6 | 2,10,6,8,12 |
| 3 | 0,2,6,12,10 | 12,4,8,2,6 | 2,10,6,4,12 |
| 4 | 2,12,4,6 | 4,2,10,8 | 10,4,0,6 |
| 5 | 0,12,10,4,6 | 12,2,6,10,8 | 2,4,12,0,6 |
| 6 | 0,12,10,8,4,6 | 12,2,6,0,10,8 | 2,4,12,8,0,6 |
| 7 | 0,2,12 | 12,4,2 | 2,10,4 |
| 8 | 0,2,12,10,8,4,6 | 12,4,2,6,0,10,8 | 2,10,4,12,8,0,6 |
| 9 | 0,2,12,10,4,6 | 12,4,2,6,10,8 | 2,10,4,12,0,6 |

| Digit | GPU hundreds `0x08` (`smgr3`) | GPU tens `0x10` (`smgr2`) | GPU units `0x20` (`smgr1`) |
| --- | --- | --- | --- |
| 0 | 10,0,6,8,4,12 | 4,2,12,8,10,0 | 8,10,2,4,0,12 |
| 1 | 0,6 | 2,12 | 10,2 |
| 2 | 10,0,2,4,8 | 4,2,6,10,8 | 8,10,6,0,4 |
| 3 | 10,0,2,6,8 | 4,2,6,12,8 | 8,10,6,2,4 |
| 4 | 0,6,12,2 | 2,12,0,6 | 10,2,12,6 |
| 5 | 10,6,8,12,2 | 4,12,8,0,6 | 8,2,4,12,6 |
| 6 | 10,6,8,4,12,2 | 4,12,8,10,0,6 | 8,2,4,0,12,6 |
| 7 | 10,0,6 | 4,2,12 | 8,10,2 |
| 8 | 10,0,6,8,4,12,2 | 4,2,12,8,10,0,6 | 8,10,2,4,0,12,6 |
| 9 | 10,0,6,8,12,2 | 4,2,12,8,0,6 | 8,10,2,4,12,6 |

### Icons

Always add the CPU and GPU label bits.
Add only the row for the selected mode after those labels.
Each pair below means `(live-block index, OR mask)`.

| Function | CPU pairs | GPU pairs |
| --- | --- | --- |
| Label, every mode | `(1,0x04)` | `(11,0x04)` |
| Celsius | `(5,0x02)` | `(7,0x02)` |
| Fahrenheit | `(3,0x02)` | `(9,0x02)` |
| Utilization | `(1,0x02)`, `(3,0x04)`, `(5,0x04)` | `(11,0x02)`, `(7,0x04)`, `(9,0x04)` |

The utilization masks identify a mode in code. Their exact visible symbols remain unverified.

### Bar order

Calculate `levels = max(0, min(10, ceil(value / 10.0)))` from the mode's bar input.
Add every segment from level `1` through `levels`.
Values at or below zero light no segments. Values `1..10` light one, `11..20` light two, and values above `90` light ten.
The bars saturate independently of the three-digit limit.

| Level | CPU `(index, mask)` | GPU `(index, mask)` |
| --- | --- | --- |
| 1 | `(6,0x40)` | `(9,0x01)` |
| 2 | `(8,0x40)` | `(11,0x01)` |
| 3 | `(10,0x40)` | `(0,0x80)` |
| 4 | `(12,0x40)` | `(2,0x80)` |
| 5 | `(1,0x01)` | `(4,0x80)` |
| 6 | `(5,0x01)` | `(7,0x01)` |
| 7 | `(3,0x01)` | `(12,0x80)` |
| 8 | `(2,0x40)` | `(10,0x80)` |
| 9 | `(4,0x40)` | `(8,0x80)` |
| 10 | `(0,0x40)` | `(6,0x80)` |

## Sequence and close

The Windows sequence field starts at `0` and post-increments for every refresh payload construction.
It wraps modulo 256: `254, 255, 0, 1`.
Reconnect does not reset the field during the same process lifetime.
It advances on construction, not on confirmed delivery.
The encoder accepts a sequence byte. The caller owns sequence state.

A close payload contains `0x0f` at payload offset `1` and zero at every other offset.
Its sequence byte is always zero. It neither consumes nor embeds the active refresh counter.
Its Linux write still requires the leading zero Report ID placeholder.
Static code establishes the close command, but not whether firmware blanks digits, disables lighting, or retains state.

## Runtime defaults

These settings distinguish the approved Linux behavior from historical application defaults:

| Setting | Linux contract | Supplied Windows evidence |
| --- | --- | --- |
| Normal mode | Temperature | Bundled `ShowMode=0` |
| Temperature unit | Celsius | Bundled `TempShowMode=0` |
| Auto interval | `6s` | Bundled `AutoChangeModeTime=6`, code fallback `2` seconds |
| Auto behavior | Alternate selected temperature unit and utilization | `NowShowMode` alternates between `TempShowMode` and `2` |
| Historical refresh interval | Reference: `200ms` | `Thread_Send` sleeps `200ms` between frames |

Linux diagnostic mode sends one fixed Celsius frame, waits three seconds, sends one fixed utilization frame, waits three seconds, then sends close.
The sample values are CPU `41 C`, GPU `62 C`, CPU utilization `23%`, and GPU utilization `84%`.
The two diagnostic data payloads appear below.
Automatic boot enablement is not part of the approved deployment.

## Golden payloads

Each example contains exactly four rows of sixteen payload bytes.
To obtain a Linux write buffer, prepend one `00` byte to the entire example, not to each row.

### Celsius, sequence 0

Inputs: CPU temperature `41 C`, GPU temperature `62 C`, CPU utilization `23%`, GPU utilization `84%`.
The temperature bars contain five CPU segments and seven GPU segments.

```text
00 00 02 00 00 00 00 00 00 00 00 00 00 00 00 00
00 00 00 00 00 00 00 00 fd 08 08 08 fd 08 dd 0f
d0 df 8f 00 00 00 00 00 b0 05 82 00 b6 02 70 03
72 01 76 05 d0 00 00 00 00 00 00 00 00 00 00 00
```

### Utilization, sequence 1

Inputs: the same sample, utilization mode.
The utilization bars contain three CPU segments and nine GPU segments.

```text
01 00 02 00 00 00 00 00 00 00 00 00 00 00 00 00
00 00 00 00 00 00 00 00 fd 08 08 08 fd 08 dd 0f
d0 df 8f 00 00 00 00 00 92 06 b4 04 96 04 76 05
d2 05 f4 07 b6 00 00 00 00 00 00 00 00 00 00 00
```

## Source methods and validation limits

The source references identify methods in the supplied application's `ComputereMonitor` namespace, not Linux implementation files.
They describe recovered behavior without requiring the original application code in this repository.

| Evidence | Methods or fields |
| --- | --- |
| Active payload and fixed block | `frmMain.SendData2`, `ValueArray`, `TM1721_show1`, `TM1721_CHAR`, `tran_num`, `fuhao_in_buf` |
| Live digits, icons, and bars | `frmMain.TM1721_show4`, `smg_tab3`, `smgl1`, `smgl2`, `smgl3`, `smgr1`, `smgr2`, `smgr3` |
| Sequence | `frmMain.sendi`, `SendData2`, attachment/removal handlers, `Get_Devices` |
| Transport | `frmMain.send_usb_data`, `CyUSB.CyHidDevice.WriteOutput` |
| Close | `frmMain.frmMain_FormClosing` |
| Mode switching and defaults | `frmMain.TimerAdd`, `ClsMain` field initializers, `ClsMain.GetParam`, bundled `config.ini` |
| Celsius and Fahrenheit conversion | `frmMain.GetPCParam`, `ClsMain.GetF` |
| Refresh interval | `frmMain.Thread_Send` |
| Observed descriptor | USB/HID sysfs `uevent` and `report_descriptor` for the connected `5131:2007` device |
| Linux Report ID handling | [hidraw API documentation](https://docs.kernel.org/hid/hidraw.html), `hidraw_send_report`, `usbhid_output_report` |

Physical USB capture shows 64-byte interrupt OUT reports at about five per second.
The reports contain header `<seq> 00 02 00` and the fixed TM1721 block beginning `fd 08 08 08 fd 08 dd 0f`.
After USB re-enumeration recreated the hidraw node, the same daemon process recovered from `no such device` and resumed reports.
The two-frame diagnostic exited `0` and emitted Celsius, utilization, and close frames.

Physical validation remains necessary for:

- Pixel rendering, CPU/GPU placement, bar geometry, segment orientation, unit symbols, and colors.
- The close command's visible effect.
- Comparison against captured Windows traffic, refresh tolerance, watchdog behavior, and any unsolicited input reports.

No camera or visual observation was available.
Successful USB writes establish transport, not the firmware's visible interpretation of the payload.
The descriptor proves report structure, not the device's interpretation of these payload bytes.
