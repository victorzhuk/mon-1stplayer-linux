# mon-1stplayer-linux

Linux daemon for a 1STPLAYER USB display using the page-2 protocol recovered from `1ST PC Monitoring V5.9`. It displays four metrics: CPU temperature, GPU temperature, CPU utilization, and GPU utilization.

## Scope and requirements

- Linux only, using `/dev/hidraw*`, hwmon, and `/proc/stat`.
- One USB HID display with VID:PID `5131:2007` and the exact report descriptor in [the protocol contract](docs/protocol.md). These IDs are shared by incompatible firmware. Descriptor acceptance does not prove firmware compatibility.
- AMD CPU temperature from exactly one `k10temp` sensor labeled `Tctl`. Intel CPU temperature sources are not supported.
- Exactly one NVIDIA GPU visible to NVML. AMD GPU metrics and multiple NVIDIA GPUs are not supported.
- The NVIDIA driver and its runtime library `libnvidia-ml.so.1` must be available to the daemon. On Arch Linux, the userspace library comes from `nvidia-utils` and must match the installed driver. The Go binding is pinned to `github.com/NVIDIA/go-nvml v0.13.4-1`.

CPU utilization is the aggregate busy-time percentage between `/proc/stat` samples, excluding idle and I/O wait. GPU temperature and utilization come from NVML. Temperature display supports Celsius and Fahrenheit.

Physical USB writes and reconnect recovery have been verified. Pixel rendering, visible metric placement, symbols, bars, and the close command's visible effect remain unobserved. See [protocol evidence and validation limits](docs/protocol.md#source-methods-and-validation-limits).

## Build and test

Use Go 1.27.0 or newer on a supported Go release line, with the latest patch release. The module declares Go 1.27.3. Building requires CGO and a C compiler, provided by `gcc` on Arch Linux. Do not use `CGO_ENABLED=0`. Normal operation requires the NVIDIA runtime library `libnvidia-ml.so.1`.

Run from the repository root as your regular user:

```sh
make build
make test
```

`make build` produces `bin/mon-1stplayer`. The Makefile exports `CGO_ENABLED=1` and `GOMAXPROCS=2`.

| Target | Purpose |
| --- | --- |
| `build` | Build the daemon. |
| `test` | Run hardware-free tests. |
| `test-race` | Run hardware-free tests with the race detector. |
| `acceptance-build` | Compile acceptance-tagged tests into `bin/acceptance.test`, without running them. |
| `lint`, `lint-acceptance` | Lint ordinary and acceptance-tagged code. Require `golangci-lint v2.15.0` on `PATH`. |
| `vuln` | Run `golang.org/x/vuln/cmd/govulncheck@v1.9.0`. |
| `clean` | Remove `bin/`. |
| `all` | Run `build` and `test`. |

Lint uses the v2 configuration in `.golangci.yml`. The GitHub Actions workflow in `.github/workflows/ci.yml` has `test`, `lint`, and `security` jobs. CI compiles acceptance-tagged tests but does not execute hardware scenarios.

### UHID acceptance

Build both executables from the repository root:

```sh
make build
make acceptance-build
```

Run the suite only in an isolated environment with no real `5131:2007` device discoverable in sysfs. It requires root access to `/dev/uhid`, the dedicated `mon-1stplayer` account, and an inactive `mon-1stplayer.service`. A failed service-state query is a refusal.

| Required environment variable | Value |
| --- | --- |
| `MON1STPLAYER_ACCEPTANCE_BIN` | Absolute path to the freshly built daemon executable. No installed-binary fallback. |
| `MON1STPLAYER_ACCEPTANCE_DETACHED` | `1` |

The ten-scenario suite passed with no skips in an isolated mount namespace with the physical hidraw node masked. It ran against a freshly built binary selected by `MON1STPLAYER_ACCEPTANCE_BIN`, not `/usr/bin/mon-1stplayer`. This result does not validate physical pixels.

## Usage

Run with device permission as an unprivileged user. Install the permission rule below before accessing the display.

```sh
bin/mon-1stplayer -h
bin/mon-1stplayer
bin/mon-1stplayer -show temperature -unit f
bin/mon-1stplayer -show utilization
bin/mon-1stplayer -show auto -unit c -auto-interval 6s
```

| Flag | Default | Behavior |
| --- | --- | --- |
| `-show temperature\|utilization\|auto` | `temperature` | Select a fixed mode or alternate temperature and utilization. |
| `-unit c\|f` | `c` | Select the temperature unit. |
| `-auto-interval duration` | `6s` | Positive Go duration between mode changes in `auto`. Fixed modes ignore this interval. |
| `-validate-frame` | `false` | Send the fixed physical diagnostic without collecting metrics. |
| `-h`, `-help` | | Print help and exit successfully. |

Positional arguments are rejected. Auto mode starts with the selected temperature unit, then alternates with utilization at each interval.

Each setting resolves separately: an explicit flag overrides the environment, a present environment variable overrides the built-in default. An empty or invalid value refuses startup with exit status `2`. `-validate-frame` is flag-only and has no environment variable.

| Environment variable | Flag | Default |
| --- | --- | --- |
| `MON1STPLAYER_SHOW` | `-show` | `temperature` |
| `MON1STPLAYER_UNIT` | `-unit` | `c` |
| `MON1STPLAYER_AUTO_INTERVAL` | `-auto-interval` | `6s` |
| `MON1STPLAYER_FRAME_INTERVAL` | `-frame-interval` | `200ms` |
| `MON1STPLAYER_RETRY_DELAY` | `-retry-delay` | `1s` |

The service reads these settings from `/etc/mon-1stplayer.conf` through the unit's `EnvironmentFile`. The daemon reads environment variables only; it never opens or parses that file. Normal startup is `sudo systemctl start mon-1stplayer`. Apply configuration edits with `sudo systemctl restart mon-1stplayer`; only unit changes need `daemon-reload`.

Normal operation samples every frame interval (default 200 ms). If the display is absent, discovery retries once per retry delay (default one second) without writes. Acquisition failures caused by disappearance (`ENOENT`, `ENODEV`, `ENXIO`) use the same retry path. Failed or short writes close the failed connection and resume discovery after one retry delay. Metric errors stop the daemon. Ambiguity, identity or descriptor mismatches, permission errors, and acquisition `EIO` remain refusals.

Press Ctrl-C to stop a foreground daemon. Shutdown attempts one close frame on a healthy connection, then releases the device and metric collector. It does not reopen a failed connection to send close. Cancellation does not interrupt a blocking ioctl or NVML call.

Exit status is `0` for help or clean cancellation, `2` for CLI or device refusals, and `1` for runtime failures.

The daemon takes a nonblocking advisory `flock` on the opened hidraw device. A competing instance refuses with `display is already in use` and exit status `2`. The lock excludes only cooperating current binaries using the same device inode, not arbitrary external writers.

## Install

Run these commands from the repository root after building. Root is needed only for installation and service management, not for building or running the daemon.

```sh
sudo install -Dm755 bin/mon-1stplayer /usr/bin/mon-1stplayer
sudo install -Dm644 deploy/mon-1stplayer.sysusers /usr/lib/sysusers.d/mon-1stplayer.conf
sudo systemd-sysusers /usr/lib/sysusers.d/mon-1stplayer.conf
sudo install -Dm644 deploy/70-mon-1stplayer.rules /etc/udev/rules.d/70-mon-1stplayer.rules
sudo install -Dm644 deploy/mon-1stplayer.service /usr/lib/systemd/system/mon-1stplayer.service
[ -e /etc/mon-1stplayer.conf ] || sudo install -Dm644 deploy/mon-1stplayer.conf /etc/mon-1stplayer.conf
sudo udevadm control --reload-rules
sudo udevadm trigger --action=change --subsystem-match=hidraw
sudo udevadm settle
sudo systemctl daemon-reload
```

The service runs as the dedicated `mon-1stplayer` user and group without capabilities. The udev rule grants that group read/write access with mode `0660` to matching `5131:2007` hidraw nodes. The daemon still checks the identity and exact descriptor before writing.

For foreground usage and physical validation, grant your regular user membership during installation:

```sh
sudo usermod -aG mon-1stplayer "$USER"
```

Log out and log in again before running the diagnostic. Do not run it as root to bypass permissions. Keep the service stopped until validation is complete. This installation does not enable boot startup.

### Arch Linux packaging

Three x86_64 packages are published on AUR, with recipes maintained in this repository:

| Package | Source | Recipe |
| --- | --- | --- |
| [`mon-1stplayer`](https://aur.archlinux.org/packages/mon-1stplayer) | Stable source archive | [`pkg/archlinux/stable/PKGBUILD.in`](pkg/archlinux/stable/PKGBUILD.in) |
| [`mon-1stplayer-bin`](https://aur.archlinux.org/packages/mon-1stplayer-bin) | Stable release binary | [`pkg/archlinux/bin/PKGBUILD.in`](pkg/archlinux/bin/PKGBUILD.in) |
| [`mon-1stplayer-git`](https://aur.archlinux.org/packages/mon-1stplayer-git) | GitHub VCS checkout | [`pkg/archlinux/PKGBUILD`](pkg/archlinux/PKGBUILD) |

Tags matching `vX.Y.Z` trigger GitHub release artifacts and automatic publication of the stable source and binary packages to AUR. CI renders the stable templates with the tag version and downloaded archive hashes, then generates `.SRCINFO`. The templates are not directly publishable. No manual `pkgver` edit is needed. The VCS package remains separate and is not automatically updated for version-only upstream changes.

The package installs the daemon, systemd unit, sysusers file, udev rule, `/etc/mon-1stplayer.conf`, and Apache-2.0 license. The config is backup-protected with `backup=('etc/mon-1stplayer.conf')`. Installation does not start or enable the service.

[Release v0.1.2](https://github.com/victorzhuk/mon-1stplayer-linux/releases/tag/v0.1.2) provides `mon-1stplayer-x86_64-linux.tar.gz` and its `.sha256` checksum. The stable source and binary AUR packages are published at `0.1.2-1`, and the VCS package at `r1.2d82ba4-1`. See the [release runbook](docs/RELEASE.md) for prerequisites and release steps.

## Physical validation: writes to hardware

This is not a dry run. It requires write permission on the display and must not run while another daemon or application owns the display. Stop any running service first:

```sh
sudo systemctl stop mon-1stplayer
```

Run as your regular user after the new group membership takes effect:

```sh
timeout --signal=INT --kill-after=5s 15s /usr/bin/mon-1stplayer -validate-frame
```

The diagnostic opens and validates the device once, with no discovery retry or metric collection. It sends:

1. Celsius frame, sequence `0`: CPU `41 C`, GPU `62 C`.
2. After three seconds, utilization frame, sequence `1`: CPU `23%`, GPU `84%`.
3. After another three seconds, the close frame.

Progress is printed to stdout. An active-frame write failure is a runtime error. Cancellation and deadline expiry each attempt exactly one close payload before the device descriptor closes. Cancellation succeeds when cleanup succeeds. Deadline expiry remains a runtime error. The timeout is an external bound, not a guarantee that blocked hardware calls honor cancellation.

Verified on the physical `5131:2007` device:

- HID bus `0003`, the exact 34-byte descriptor, unnumbered 64-byte Input/Output reports, and interrupt endpoints IN `0x82` and OUT `0x02`.
- USB capture shows 64-byte interrupt OUT reports at about five per second, with header `<seq> 00 02 00` and the fixed TM1721 block beginning `fd 08 08 08 fd 08 dd 0f`.
- After USB re-enumeration, the same daemon process logged a write failure with `no such device`, then `waiting for display`, then `connected: /dev/hidraw3`, and resumed 64-byte reports. The recreated hidraw node had a different inode.
- The service ran as the unprivileged `mon-1stplayer` user with a group-owned hidraw node at mode `0660`. Boot startup was not enabled.
- The two-frame diagnostic exited `0` and emitted Celsius, utilization, and close frames.

No camera or visual observation was available. Physical pixels, the visible meaning of each segment, and the close command's visible effect remain unverified. Observe both frames and close behavior against [the protocol contract](docs/protocol.md). Successful writes and process exits do not establish correct physical rendering.

## Service

Only after physical validation, start the service manually:

```sh
sudo systemctl start mon-1stplayer
systemctl status mon-1stplayer
journalctl -u mon-1stplayer -f
```

The unit loads `/etc/mon-1stplayer.conf`, which selects the default temperature/Celsius mode out of the box. Edit that file and restart the service to change modes. It restarts runtime failures after five seconds, subject to its start limit. Exit status `2` prevents restart. Logs include waiting and connection transitions, absorbed write failures, and final errors, rather than every discovery retry. Journal access depends on your account's journal permissions.

Stop the service with:

```sh
sudo systemctl stop mon-1stplayer
```

No boot enablement is included. Do not run a foreground instance alongside the service.
