# Changelog

All notable changes to tiler are recorded here. Versions are listed newest
first. An unreleased entry describes the next planned release.

## [0.1.0] - Unreleased

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
