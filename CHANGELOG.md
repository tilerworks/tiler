# Changelog

All notable changes to tiler are recorded here. Versions are listed newest
first. An unreleased entry describes the next planned release.

## [0.2.0] - [Unreleased]

### Added

- Configure image chain depth with `max_layers` in `.tiler.yaml`, defaulting
  to 32 layers instead of 8 for builds and mounts.

- `make cross` builds static Linux binaries for arm64, amd64 (named
  `x86_64`), and 386 (named `i386`) under `dist/`.

### Fixed

- Keep doctor progress in a temporary terminal screen that adapts to small
  windows and resizing, then restore the original screen before the report.
- Include EPEL and CRB repository setup in doctor installation guidance on
  Rocky Linux and related Enterprise Linux distributions.
- Improve errors, warnings, and recovery hints with image and session context,
  expected and actual integrity values, actionable FUSE permission and timeout
  guidance, explicit skipped validation and partial-success notices, consistent
  warning formatting, and terminal-safe text that preserves trusted multiline
  hints.

## [0.1.0] - 2026-11-02

### Added

- Full and incremental container images with immutable version storage,
  dependency trees, hash selection, descriptions, and Git source metadata.
- Linux squashfs and erofs packing and FUSE mounts, with writable overlay
  workspaces and image subdirectory selection.
- macOS disk images with writable session shadows and host filesystem
  selection through hdiutil.
- External image and repository imports through link and copy modes,
  including dependency closure and payload integrity verification.
- Configurable byte-block storage, verified cache reassembly, and automatic
  concatfs mounts on Linux when the optional helper is available.
- Persistent sessions with multiple mounts, crash recovery, automatic
  removal, and replay that preserves unpublished edits and deletions.
- Filtered status and session views, layered mount trees, and JSON output.
- Per-session and per-image activity logs, retention previews, and
  dependency-aware garbage collection.
- Layered project and user configuration, configuration upgrades, runtime
  diagnostics, and storage performance benchmarks.
- Static builds with source-derived versions through both `tiler version`
  and `tiler --version`.
- Unit, integration, and shell end-to-end tests for the supported workflows.
