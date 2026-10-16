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
- `tiler build [--full|--incr] [--image owner/repo] [dir]` packs a
  prepared directory into a full image, or, inside a writable merged
  view, appends the upper layer as one increment. A plain directory
  defaults to a full build and needs the explicit `owner/repo` name;
  a writable merged view defaults to an incremental build and infers
  the name from the mounted image. Success prints one line:
  `full <owner>/<repo> <id>` or `incr <owner>/<repo> <id>`.
- `tiler images ls [owner/repo] [--archives]` lists every active
  image with its chain length. A name lists each chain id, and
  `--archives` adds the archived and held chain ids.
- `tiler images gc` promotes held chains that nothing references
  into archives, then deletes archives beyond `--keep-versions` or
  `--max-age`. Zero means that rule is unlimited.
- `tiler images rm <owner/repo[/archives/<id>]>` deprecates the
  target on the first call and removes it on the second. Both calls
  refuse while a reference pins the target.

Planned commands, not available yet:

- mount a workspace
- finish the session
- show status

## User story

A developer prepares a local environment quickly. On a new machine,
mounting a prepared environment takes seconds. A full clone, dependency
install, and build takes minutes or hours.

The first stored image takes one command: a scene script prepares a
directory, `tiler build --image alice/app dir` packs it into the
library under `~/.tiler/images/`, and the next full build of the same
name archives the previous image instead of breaking readers.
`tiler images gc` later trims the archives by count and age.

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
