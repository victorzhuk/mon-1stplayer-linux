# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.1] - 2026-10-26

### Fixed

- Include complete PKGBUILD, .SRCINFO, and packaging license files in AUR publication artifacts.

## [0.1.0] - 2026-10-26

### Added

- Linux daemon for the 1STPLAYER USB display using the recovered 64-byte page-2 protocol, with temperature and utilization modes, Celsius/Fahrenheit, and automatic switching.
- CPU temperature from AMD k10temp Tctl and utilization from `/proc/stat`, plus NVIDIA GPU temperature and utilization through NVML.
- Fingerprinted hidraw discovery with exact descriptor and identity validation, reconnect handling, and nonblocking advisory device ownership that refuses competing instances.
- Five environment-backed runtime settings with explicit CLI flags overriding environment values, and systemd `EnvironmentFile` support through `/etc/mon-1stplayer.conf`.
- Two-frame physical diagnostic mode with close-frame handling on cancellation and deadline expiry.
- systemd service, sysusers account, and udev permissions for an unprivileged daemon.
- Arch Linux `mon-1stplayer-git` VCS package recipe with generated `.SRCINFO` and backup-protected configuration.
- Makefile, golangci-lint configuration, GitHub Actions CI, hardware-free tests, and an isolated UHID acceptance suite.
- Tag-triggered GitHub releases with an x86_64 Linux binary archive and SHA-256 checksum.
- Automatic stable source and binary AUR publication from versioned package templates.
- Protocol and deployment documentation and the Apache-2.0 license.

### Fixed

- Descriptor-length validation now refuses longer descriptors that share the canonical prefix.
- Discovery recovers when the display disappears, while permission and identity failures remain refusals.
- Bar calculations and millidegree-to-Celsius conversion avoid overflow at numeric boundaries.
- Diagnostics attempt close-frame delivery when their context is canceled or reaches its deadline.
- Acceptance subprocess ownership, event validation, fixture teardown, and reconnect sequence assertions.

[unreleased]: https://github.com/victorzhuk/mon-1stplayer-linux/compare/v0.1.1...HEAD
[0.1.1]: https://github.com/victorzhuk/mon-1stplayer-linux/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/victorzhuk/mon-1stplayer-linux/releases/tag/v0.1.0
