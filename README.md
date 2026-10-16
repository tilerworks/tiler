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
session, and show status. A quick start covers the available end of
that workflow:

```bash
tiler build --image alice/app dir   # pack a prepared directory
tiler mount alice/app work          # writable view over the image
# develop inside work; changes land in the upper layer
tiler umount work                   # upper and work data survives
```

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
- `tiler mount <image> <dir>` serves an image as a workspace. The
  default view is writable: every squashfs member mounts read-only
  under `~/.tiler/runtime/`, and `fuse-overlayfs` merges them over
  an upper layer named after the target's device and inode, so
  remounting the same directory is idempotent and reuses the
  earlier changes. A healthy mount of the same view reports
  `already mounted`; a different identity is a conflict error,
  never a silent restack. `--read-only` mounts a single image
  straight onto `<dir>`, or merges a chain lowerdir-only.
  `<image>` is the path of one squashfs file, or an `owner/repo`
  library reference that resolves through the active metadata into
  the full image plus its incremental chain, at most eight members
  in one stack; when an argument is both a valid name and a real
  file, the library reference wins. Library members pass a quick
  size and mtime check first; `--verify` re-hashes every recorded
  sha256 digest instead (a file path records none).
  `--subdir <path>` exposes one image subdirectory as the mount
  root: members missing the subdirectory drop out of the stack,
  and when no member has it the mount fails before anything is
  created. A subtree without `.git` lets workspace git commands
  search upward past the mount point, so subdir views suit
  standalone sub-repositories or non-git content. With
  `cache_enabled` in the configuration (off by default; `--cache`
  enables it for one call) every member is copied into `cache_dir`
  under its content digest and verified as a whole before
  mounting, so NAS-backed images mount from a local copy while
  the mount table keeps naming the original library path. The
  target must be an empty directory owned by the current user,
  outside the runtime tree and every reserved directory.
- `tiler umount <dir>` tears a view down by path: the merged point
  first, then the runtime transit points in reverse order. Each
  point falls back from `fusermount3 -u` to the lazy `-uz` when it
  is busy or stale, and a residue sweep of the runtime directory
  named by the target's device and inode catches lowers the table
  no longer connects to the target. It is idempotent: an unmounted
  `<dir>` succeeds silently. Upper and work data is never deleted,
  so a later mount of the same directory reuses the earlier
  changes.

Planned commands, not available yet:

- finish the session
- show status

## User story

A developer prepares a local environment quickly. On a new machine,
the mount itself completes in milliseconds and the workspace is usable
in seconds, while a full clone, dependency install, and build takes
minutes or hours.

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
