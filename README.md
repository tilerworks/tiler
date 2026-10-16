# tiler

[中文](README.zh.md)

tiler packs a directory snapshot into a read-only squashfs image and
provides a writable workspace with an overlay. The tool does not
inspect content types. Git repository acceleration is one scenario,
handled by scripts beside the tool.

Licensed under [MIT](LICENSE).

## Installation

Build from this tree with `make build`. The static binary is written
to `bin/tiler`.

`go install` is planned. Release binary downloads are planned.

`tiler doctor` checks these runtime tools:

- `squashfuse_ll`
- `fuse-overlayfs`
- `fusermount3`
- `mksquashfs`

`git` is optional. Doctor always lists it, and a missing binary is a failed probe rather than a silent skip. A failed git probe does not block the other checks.

## Usage

The target workflow is: build an image, mount it, develop, finish the
session, and show status.

Available now:

- `tiler version` prints one version line.
- `tiler doctor` probes `/dev/fuse` and the external tools.

Planned commands, not available yet:

- build an image
- mount a workspace
- finish the session
- show status

## User story

A developer prepares a local environment quickly. On a new machine,
mounting a prepared environment takes seconds. A full clone, dependency
install, and build takes minutes or hours.

The read-only base stays immutable. The upper layer can be discarded
and mounted again.

Sessions isolate projects while they share one read-only base. Across
eight sessions that share one lower layer, each upper layer is about
6.5 MB in steady state.

A crash can be restored by replaying the action record.

## License

Copyright 2026 Jiang Xin.

The project is licensed under [MIT](LICENSE). The license text
is the MIT License.
