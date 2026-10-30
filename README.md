# tiler

[中文](README.zh.md)

tiler packs a directory snapshot into a read-only container image and
provides a writable workspace with an overlay. The first containers
are squashfs and erofs. The tool does not inspect content types. Git
repository acceleration is one scenario, handled by scripts beside
the tool.

Licensed under [MIT](LICENSE).

## Installation

Build from this tree with `make build`. The static binary is written
to `bin/tiler`.

`go install` is planned. Release binary downloads are planned.

`tiler doctor` checks the shared runtime tools and each image stack:

- squashfs stack: `mksquashfs`, `squashfuse_ll`
- erofs stack: `mkfs.erofs`, `erofsfuse`
- shared: `fuse-overlayfs`, `fusermount3`, and `/dev/fuse`

A stack is complete only when every binary in it is installed. At
least one complete stack passes the `image_toolchain` probe. A
missing stack is a warning. No complete stack fails that probe.
The shared checks still fail the run on their own. On a host with
both stacks and no format setting, builds stay squashfs with zstd.

`git` is optional. Doctor always lists it, and a missing binary is a failed probe rather than a silent skip. A failed git probe does not block the other checks.

Optional helper tools ship as sources under `contrib/`: `concatfs`
(optional helper for manual byte-block concat; build needs gcc and
libfuse3-dev) and `slowfs` (latency injection for slow-NAS
comparisons; build needs a Go toolchain). Each directory records the
upstream origin, license, and build command. tiler prompts when a
scenario needs one and it is missing; it never installs them
automatically. tiler does not select concatfs as a mount route.

## macOS

macOS images are one `image.dmg` file. Compressions are `ulfo`
(the default), `ulmo`, and `udro`. A writable mount stores changes
in `sessions/<name>/runtime/m<seq>/upper.shadow`. `tiler umount`
leaves that file in place and leaves the mountpoint empty.

`tiler doctor` passes `image_toolchain` when `hdiutil` is on
`PATH`, and fails with a note that it was not found when it is
missing. It does not report `fuse_device`, `fuse_overlayfs`,
`fusermount3`, or `squashfuse_subdir` as failures. A missing `git`
suggests `xcode-select --install`. The single-file size probe stays,
because `image.dmg` is one file. Cold-read measurements are info.
`concatfs` and `slowfs` stay in the report as info and state that
they are unused on macOS, because the dmg path does not use FUSE
helpers. The detail does not repeat the Linux build guidance.

When `attach -shadow` cannot use an APFS image on this system, every
new image is created as HFS+ and doctor states that reason. The
choice is not a configuration key.

## Windows via WSL2

WSL2 runs a real Linux kernel. Inside the distro, `runtime.GOOS` is
`linux`, so tiler uses the same squashfs/erofs and FUSE path as bare
metal Linux. There is no Windows-native mount stack and no dmg path.
Build and run the Linux binary in a WSL shell. A `tiler.exe` built
for Windows cannot mount.

Keep the repository, `$HOME/.tiler` (images, sessions, cache, logs), and
mountpoints on the distro's own filesystem (typically under
`/home/<user>`). Do not put them under `/mnt/c` or `/mnt/d`: those
are Windows volumes seen through 9p (WSL2) or drvfs (WSL1). Atomic
renames, xattrs, whiteouts, and FUSE are unreliable there, and
incremental layers can break. From Windows, open the same trees with
`\\wsl.localhost\<Distro>\home\<user>\...` (or `\\wsl$\...`).

### Setup

1. Install a WSL2 Ubuntu-class distro (`wsl --install`).
2. Inside the distro, install Go 1.25 or newer and the image tools:

   ```bash
   sudo apt update
   sudo apt install squashfs-tools squashfuse fuse3 fuse-overlayfs
   # optional erofs stack:
   sudo apt install erofs-utils
   ```

3. Confirm `/dev/fuse` exists and is usable. `tiler doctor` reports
   that as `fuse_device`; missing FUSE fails the mount path.
4. Clone this repository under `$HOME`, then `make build` and
   `make test`.

### Loop

Run every command in the WSL shell (not from a Windows-built binary):

```bash
tiler doctor
tiler build --image alice/app dir
tiler mount alice/app work
# edit under work; a Windows IDE may use \\wsl$\...
tiler status work
tiler build          # incremental when the cwd is the merged view
tiler umount work
```

From PowerShell you can only invoke the WSL binary, for example:

```powershell
wsl -d Ubuntu -- tiler doctor
```

Do not add a Windows wrapper script. Privileged e2e tests that need a
real mount also run inside WSL; when `/dev/fuse` or permission is
missing they keep calling `t.Skip` and must not be turned into
failures.

## Usage

Text timestamps in image and session commands, status, and GC previews
use the runtime local time zone with second precision
(`YYYY-MM-DD HH:MM:SS`). JSON, activity logs, and stored metadata retain
their existing timestamp formats and precision.

The target workflow is: build an image, mount it, develop, finish the
session, and show status. A quick start covers the workflow:

```bash
tiler build --image alice/app dir   # pack a prepared directory
tiler mount alice/app work          # writable view over the image
# develop inside work; changes land in the upper layer
tiler status work                   # the covering session and its health
tiler umount work                   # upper and work data survives
```

A session groups several mounts into one durable record and is the
unit of crash recovery:

```bash
tiler session new feature                  # durable record
tiler mount --session feature alice/app work
tiler mount --session feature bob/tools tools
tiler session open feature                 # after a crash: replay and restore
tiler session close feature                # reverse unmount, record kept
```

Available now:

- `tiler version` prints one version line.
- `tiler doctor [--json]` probes `/dev/fuse`, the shared external
  tools, the image toolchain (one complete squashfs or erofs stack
  is enough; a missing stack warns), and the squashfuse subdir
  baseline, then inspects the storage: a
  sparse ladder detects a single-file size limit on the filesystems
  behind the image library and the cache directory and advises
  enabling split storage when one is found; a bounded performance
  probe compares cold sequential and random reads of both
  directories and turns the comparison into cache advice — the
  latency baseline in that advice is a trend reference, not an SLA;
  and the optional contrib helpers `concatfs` and `slowfs` are
  looked up, with build guidance when missing — tiler never
  installs them automatically. Any failed probe exits 1; warn and
  info results never affect the exit code.
- `tiler build [--full|-f|--incremental|-i] [--image|-I <name>] [--format squashfs|erofs] [--compression name] [--split|-s size] [dir]` packs a
  prepared directory into a full image, or, inside a writable merged
  view, appends the upper layer as one increment. `--format` wins
  over `image_format`, which wins over toolchain inference: both
  stacks select squashfs, erofs alone selects erofs, and squashfs
  alone selects squashfs. `--compression` wins over
  `image_compression`, which otherwise uses the format default.
  Squashfs defaults to zstd. erofs defaults to zstd when `mkfs.erofs`
  provides it, and otherwise to lz4, then gzip. With neither flag nor
  config on a dual-stack host the
  image stays squashfs with zstd. An incremental build inherits the
  base format and compression; a flag that disagrees is a usage
  error. erofs does not accept xz. A plain directory
  defaults to a full build and needs an explicit 1-to-5 segment
  image name; a writable merged view defaults to an incremental
  build and infers the name from the mounted image. If the recorded
  incremental layers plus the live upper already exceed half the base
  image, that default becomes a full rebuild and a warning names
  `--incremental` to keep appending.
  The packed directory must stay outside every reserved tree and must
  not cover `images_dir`, `sessions_dir`, `cache_dir`, `logs_dir`, or
  `$HOME/.tiler`; comparison uses real paths, so a symlink into or
  over those trees is refused the same way. An identical full or
  incremental payload is a no-op that reprints the same id. A dmg
  pack is not byte-stable under real hdiutil, so a create records
  `content_sha256` of the packed directory tree, a bake records the
  sha256 of the session `upper.shadow` after convert, and either
  skips hdiutil when that fingerprint already matches the active
  image.
  With `--image` naming a different library image, a path under a
  live view (often a subdirectory) packs as that image's full source.
  Success prints one line:
  `full <name> <id>` or `incr <name> <id>`. With
  `split_size` (or `tiler build --split <size>`) greater than zero,
  full and incremental builds store verified byte blocks instead of
  one `image.sqsh` / `_incr/<id>.sqsh` (see Split storage below). The
  active form must stay consistent: blocks active only appends
  blocks layers; single active only appends single layers.
- `tiler images list [--json]` prints each name's latest version and
  complete parent-first tree, using 10-hex IDs. Dependencies have a
  `↳` arrow and two spaces of indentation per level within the ID
  column; the remaining columns stay aligned with spaces. `SIZE`
  is the node's own image payload size, including all blocks for a
  split payload, without adding ancestor sizes. `CREATED` and
  `LAST ACTIVITY` use local time in `YYYY-MM-DD HH:MM:SS` format.
  Activity includes creation, failed actions, initialization, and
  descendant dependency events
  propagated through every ancestor to the full image. It is not
  limited to successful use. Missing or empty logs display `-`;
  unreadable or damaged logs display `unknown` with a warning on
  stderr, while the listing continues. Listing does not renew
  activity. `--json` preserves full IDs and the existing metadata
  fields without reading activity logs.
  `images show <name[:hash]>` accepts a scoped 4-to-64-hex prefix.
- `tiler mount <name[:hash]> <dir>` mounts that immutable tip and its
  ancestors; a bare name selects the newest node on any branch.
- Builds accept `-m/--message` and `-F/--file` (`-F -` reads stdin);
  `images describe|desc <name>:<hash>` also opens `$EDITOR` by default.
- `tiler image rm <name>:<hash>` removes that node and its descendants;
  `tiler image rm <name>` removes the repository. Session references
  block either operation. `images gc` lists expired unreferenced
  image versions, plus pending-cleanup and orphan objects, and
  deletes only after `--yes` or an interactive `yes`. It does not
  remove, close, or unmount sessions. Retention is `--expire`
  (default 30 days); zero expires every inactive object. A pending
  cleanup runs alone; rerun `images gc` afterward to inspect
  expiration. Ancestors required by a retained version stay.
  `session gc` uses the same confirmation and deletes an expired
  idle session directory, upper data included. Sample output is
  under Activity and garbage collection.
- Session records remain authoritative when the reverse-index cache is stale.
- `tiler mount <image> <dir>` serves an image as a workspace. The
  default view is writable: every squashfs member mounts read-only
  under the session's `runtime/m<seq>/` tree, and `fuse-overlayfs`
  merges them over an upper layer there, so remounting the same
  directory is idempotent and reuses the earlier changes. Split
  payload forms resolve through the same reference and mount
  through their own routes (see Split storage below). A healthy
  mount of the same view reports `already mounted`; a different
  identity is a conflict error, never a silent restack.
  `--read-only` mounts a single image straight onto `<dir>`, or
  merges a chain lowerdir-only.
  `<image>` is the path of one squashfs or erofs file, or a 1-to-5
  segment library name that resolves through the active metadata
  into the full image plus its incremental chain, at most eight
  members in one stack; when the cleaned path exists as a regular
  file, that file wins over a library name. Import an external
  image with `tiler build --link` or `tiler build --copy`, then
  mount the library name (see External images below). Library
  members pass a quick size and mtime check first; `--verify`
  re-hashes every recorded sha256 digest instead (a bare file path
  records none). A version whose digest is 64 zeros is unknown:
  `--verify` skips that sha256, still checks reachability, format,
  size, and mtime, and reports the skip. Other versions and known
  blocks stay on the full hash.
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
  outside the runtime tree and every reserved directory, and must
  not be an ancestor of those trees (a mount must not cover
  `sessions_dir`, `images_dir`, `cache_dir`, `logs_dir`, or
  `$HOME/.tiler`).
  Every mount is recorded write-ahead in a session before the
  engine runs: `-s, --session <name>` attaches a session created with
  `session new`, the `TILER_SESSION_ID` environment variable binds
  one, and without either a generated `adjective-animal` session
  (for example `happy-fox`) is created. That mount prints
  `new session created: <name>` before the `mounted` line so a later
  `--session <name>` can record more views on it. `--rm` marks the
  just-created session for removal: once its last view unmounts the
  record is deleted and its runtime directory purged, upper data
  included. It cannot combine with `--session` or `TILER_SESSION_ID`
  — use `session new --rm [name]` for a session created ahead of
  time. A non-`--rm` session stays active after its last view
  unmounts until `session close`. A failed mount keeps its
  recorded action failed, so a later `session open` retries it.
- `tiler umount <dir>` tears a view down by path: the merged point
  first, then the runtime transit points in reverse order. Each
  point falls back from `fusermount3 -u` to the lazy `-uz` when it
  is busy or stale, and a residue sweep of that view's
  `runtime/m<seq>/` tree catches lowers the table no longer
  connects to the target. It is idempotent: an unmounted `<dir>`
  succeeds silently. Upper and work data is never deleted, so a
  later mount of the same directory reuses the earlier changes.
  On macOS the writable file is
  `sessions/<name>/runtime/m<seq>/upper.shadow`; unmount leaves
  that file and leaves the mountpoint empty.
- `tiler session` manages the mount sessions that crash recovery
  replays. `session new [name]` creates an empty record under
  `~/.tiler/sessions/` and refuses a duplicate name; omitting the
  name generates an `adjective-animal` name and prints
  `created <name>`. `--rm` marks
  it so `session close` deletes the record and purges the runtime
  of every view instead of keeping it for audit. `session open
  <name>` walks the record in mount order: a healthy view is
  skipped and a dead one is cleaned (`fusermount3 -u`, escalating
  to `-uz`) and rebuilt, so repeated opens are idempotent and one
  open restores everything after a crash. A closed session resumes
  the same way: its views remount and the record becomes active
  again. `session close <name>`
  unmounts every recorded view in reverse order, then marks the
  record closed and keeps it for audit — except a `--rm` session,
  which is removed instead: its record is deleted and the runtime
  directory of every view is purged, upper data included, and the
  command prints `removed` rather than `closed`. `session remove <name>`
  deletes a closed record and refuses an active one. `session
  list [--json]` prints one aligned row per record (name, state,
  mount count, created and closed times, and an RM marker for
  `--rm` sessions). `session show <name> [--json]` prints a
  multi-line record — identity fields, then one line per action
  — and the `--json` form of show is the record object itself. A
  generated name and an explicit name accept the same commands;
  only `--rm` changes close into a delete.
- `tiler status [<path>] [--json]` reconciles the session records
  with the real mount state. Without a path it prints one block
  per record — every live mount action marked `healthy` or
  `stale` — flags the session named by `TILER_SESSION_ID` as
  `current`, and lists mounted tiler views that no active record
  claims as `orphan` lines. With a path it reports the session
  whose recorded view covers it: a deep path inside a merged view
  and a path inside the runtime tree (an upper or a lower transit
  point) both resolve back to the covering session, and a path no
  session covers is an error.

### Activity and garbage collection

Each session and each image version has its own JSONL activity log
under `logs_dir` (default `$HOME/.tiler/logs`). The session file is
`logs_dir/sessions/<name>.log`. A version file is
`logs_dir/images/<name>/<id>.log`, whether that version is full or
incremental. A multi-segment image name such as `alice/app` is the
directory `images/alice/app/`. The lock file sits beside the log and
is the log file name plus `.lock`. The log and the lock are mode
0600. Activity bytes are not part of the payload, so they do not
change a version id, sha256, or the manifest schema. A mount of a
file that is not a library version updates the session log only.

Records are UTF-8 JSON lines. A new line starts with the integer
format number `"version":1`. The image version id is the
`image_version` field and is omitted when the event has none.
`version` is the log format number. The time of the newest complete
line is the last activity, in UTC. A file that would grow past 100 KiB
(102400 bytes) is compacted in place to its newest complete lines,
about 50 KiB, and the new line is appended. No rotated copy is kept.
Compaction and a tail read do not count as activity.

An image event is stored on the source version and on every
ancestor through the full root. The source line keeps the real
event, such as `image.create.full`, `image.create.incremental`,
or `mount`. Each ancestor line is `image.dependency.activity`,
with `image_version` set to that ancestor, `source_version` set to
the source id, and `source_event` set to the original event name.
The phase is unchanged. One batch shares one timestamp, the later
of the current UTC time and any tail already in the chain. Lines
are synced from the root toward the source, holding one log lock
at a time, so a source line means every ancestor line for that
phase is already durable. An ancestor line does not start another
walk. A sibling, another full root, and another repository are
not updated.

The first GC scan that finds a missing log writes
`activity.initialize` at the current time and keeps that object
for this scan, including `--expire 0`. On an image version that
initialization also appends one dependency line on each ancestor
and may refresh an ancestor that already has a log. The next scan
only reads. It does not append, and it does not extend retention.

`tiler session gc` and `tiler images gc` (and the `sessions` and
`image` aliases) take `--expire N` and `--yes`. `N` is a
nonnegative whole number of days, default 30. A day is 24 hours.
Zero expires objects that are already inactive. A negative number,
a fraction, or a value that overflows the duration range is a
usage error and exits 2.

Both commands accept `-n` / `--dry-run` to list what would be removed
without deleting or asking, even with `--yes`. Missing activity logs may
still be initialized, and those objects are retained during that scan.

`tiler images gc [image ...]` limits cleanup to the union of complete image
names or quoted glob patterns. For example,
`tiler images gc -n 'team/*' other/app` previews both selections. `*` and `?` cross `/`; character classes
and backslash escapes are supported. Matches are sorted and deduplicated.
Unmatched selectors are reported; no matches remove nothing. Without image
arguments GC scans the whole library. Pending cleanup runs alone; if it
belongs to an unselected image, clean that image first and retry.

Both commands print every candidate before deleting anything. An
interactive terminal must answer `yes` in lower case. `--yes`
prints the list and then deletes. Without `--yes`, a pipe, a
file, or any other noninteractive input only prints the list,
even when that input is `yes`. An empty list does not ask.
Nothing is removed, unmounted, or closed in that preview, though
a missing log may be initialized.

A session is listed only when it has no attached mount, no other
process holds its lock, and its last activity is strictly before
the cutoff. An active session with no mount can expire. GC does
not unmount. Removal deletes the whole session directory — the record and
`runtime/` upper data — and the session log and lock under
`logs_dir`.

```text
Expired sessions (last activity before 2026-09-29T03:11:06Z):
  idle  active  last active 2020-01-01T00:00:00Z
Removing a session deletes its whole directory: the record, its
activity log, and runtime/, upper data of every view included.
```

The question and the noninteractive hint go to stderr:

```text
Nothing removed: confirmation needs an interactive terminal; rerun with --yes to remove 1 sessions.
```

`--yes` repeats the list, then prints the outcome:

```text
removed idle
removed 1 of 1 approved sessions, kept 0
```

A log created by that scan is not a candidate:

```text
kept idle: initialized (activity log created by this scan)
```

Image GC decides per version. A version is listed when it is
expired, no session references it, and dropping it would not
remove an ancestor of a version that stays. That closure is
calculated from the manifest at GC time. It is separate from the
activity walk, so an old parent of a retained child stays even
when the parent's log was never refreshed. Another expired root
in the same repository can still be removed. Rows are
child-before-parent. Pending-cleanup and orphan objects are
labeled in the same preview and wait for the same answer.
`images gc` never deletes a session or an external source file.

```text
Image GC candidates (last activity before 2026-10-29T03:11:06Z):
  expired  alice/app:<id>  last active 2026-10-29T03:11:06Z  age 0d
Only listed image objects are eligible; sessions and external source files are preserved.
```

A confirmed run then prints `removed alice/app:<id>` and
`removed 1 of 1 approved image objects, skipped 0`. A version
initialized by the scan is reported on stderr:

```text
kept alice/app:<id>: initialized (activity refreshed by this scan)
```

### External images

NAS shares and vendor trees often hold a ready-made squashfs, erofs,
or dmg without tiler's library layout. Import the file, then mount
the library name. A bare path still mounts without entering the
library.

| Path | Command shape | Library | Session `Name`/`FullID` | `build -i` in view |
|------|---------------|---------|-------------------------|--------------------|
| Bare file | `tiler mount /path/to/file.sqsh work` | none | no | cannot infer name |
| Link, then mount | `tiler build --link /path/to/file.sqsh --image alice/app` then `tiler mount alice/app work` | position file + meta under `images_dir` | yes | yes on Linux |
| Copy, then mount | `tiler build --copy /path/to/file.sqsh --image alice/app` then `tiler mount alice/app work` | local payload + meta | yes | yes on Linux |

`--link` and `--copy` are mutually exclusive, require `--image`, and
accept no positional directory. They do not pack, mount, or open a
session. Ordinary `tiler build [dir]` still packs a directory, and
`-F/--file` still reads a description. Those pack and description
flags cannot combine with import. Usage mistakes exit 2. A missing
file, a bad digest, or a version conflict exits 1.

A regular file imports as one full image with no parent. To keep an
incremental chain or split blocks, point `--link` or `--copy` at a
directory that contains `meta.yaml`. `created_at` is stored as
RFC 3339 in the local zone, to the second
(`2026-10-29T18:07:56+08:00`). Older values, including a UTC
timestamp with a fractional second, still load, and the fraction
is discarded.

Directory selection:

- Omit `--hash` to import the latest version (newest `created_at`,
  then the smallest full id) and every ancestor from the root.
- `--hash <prefix>` imports the one version whose id matches that
  4-to-64 hex prefix, plus its ancestors. The prefix is matched in
  lower case. A missing or ambiguous prefix fails and lists the
  candidates.
- `--hash all` imports every version, parents before children.
  Shared ancestors are planned once. The word is exactly `all`.

`--link` writes a position file whose only content is the absolute
source path and a newline. It does not copy the payload. For one
file it reads `<stem>.yaml` first (`app.sqsh` selects `app.yaml`),
then `<image-file>.sha256`. Either file supplies the digest, and
the payload is not hashed. When both are missing, the import warns
and reads the image to compute sha256. An explicit digest of 64
zero hex digits stays the unknown marker. That marker is not a
content address. It is not used to deduplicate repositories or to
share a content cache. One image name has at most one all-zero
version. The same source with the same format, size, and mtime is
a no-op. A different source, or a changed size or mtime, is a
conflict. An unknown digest does not prove the bytes are unchanged.

Each published version also stores `image.yaml` beside its payload
under `_objects/<id>/`. The file repeats that version's `meta.yaml`
node, including sha256, so an exported image can keep its digest
when `meta.yaml` is not copied with it.

`--copy` reads the same two metadata files. The streamed bytes must
match a digest they supply. When both are missing, the import warns
and records the SHA-256 of the copied bytes. An explicit digest of
64 zeros is an error.

`mount` of a library name still does the quick size and mtime check.
`mount --verify` re-hashes recorded digests. For an unknown version
it skips that sha256, still checks that the source is reachable, and
checks format, size, and mtime. It prints that the digest is unknown
and was skipped. Other versions and every known block are hashed.
A missing or changed source fails the mount. An unknown version is
not copied into the shared content cache.

Merging into an existing name does not overwrite version facts. The
same id is a no-op when the link source and the recorded facts match.
The same id aimed at a different final path is a source conflict,
even when the declared digest matches. A different parent, format,
size, or block list is a conflict. `--copy` of a known link keeps
that id (`converted`), replaces the position file with a local
payload, and leaves session references valid. `--link` of an id that
is already a local copy keeps the local copy. `--copy` of an
all-zero link publishes a new id with the real digest, keeps the old
zero version and its position file for existing references, and does
not rewrite sessions. Success lines are `link`, `copy`, `no-op`, or
`converted`, followed by the image name and version id.

A refused container format exits 1:

| Host | Allowed source | Position or payload name | Refused |
|------|----------------|--------------------------|---------|
| Linux | squashfs, erofs | `image.sqsh` | dmg |
| macOS | dmg only | `image.dmg` | squashfs, erofs |

Removal and GC delete the local position file, metadata, and derived
cache. They do not delete the external source. After `--copy`, the
version no longer depends on that source. With `--cache`, a known
digest may be copied into `cache_dir`; the mount table and session
still name the library path.

Example:

```bash
# Linux: link a NAS squashfs, mount by name, append a local layer
tiler build --link /mnt/nas/vendor/app.sqsh --image vendor/app
tiler mount vendor/app work
# edit under work
tiler build -i
tiler umount work

# Copy the same known link into a local payload and keep its id
tiler build --copy /mnt/nas/vendor/app.sqsh --image vendor/app

# macOS: link an external dmg, then mount by name
tiler build --link /Volumes/share/base.dmg --image vendor/base
tiler mount vendor/base work
```

### Split storage

Split storage is an opt-in form for targets whose filesystem caps
single-file size — quota'd NAS shares, FAT-class volumes with a
4 GiB limit. Set a block size in configuration or on the build
command; zero (the default) keeps the single-file layout:

```yaml
split_size: 2G    # default for tiler build; 0 = off
```

```bash
tiler build --split 64k --full --image alice/app /path/to/src
tiler build --split 0 ...   # this build stays single-file
```

When the effective size is greater than zero, **full and each
incremental layer** are stored as fixed-size byte blocks under
`_blocks/<layer-id>/blk.NNN`. Digests and sizes on metadata still
describe the whole logical squashfs per layer. Mounting reassembles
every layer into `<cache_dir>/<layer-sha256>.sqsh` (each block
verified, then the whole digest) and stacks them newest-incr-first
with the same overlay path as a single-file chain. The cache
directory needs room for the reassembled layers. Mixing forms is
refused: a blocks active cannot append a single-file incr, and a
single active cannot append blocks — rebuild with a matching full
first. The optional contrib `concatfs` helper remains available for
manual use, but tiler no longer selects a concat mount route from
configuration or flags.

When the doctor single-file limit probe finds a cap on the image
library or the cache directory, it advises setting `split_size` (or
`tiler build --split`); with a positive `split_size` the same probe
reports the configured block size.

The list and view commands carry a machine-readable form for CI
gates and agent consumers. The schemas are stable and add-only,
pinned by golden-file tests:

```bash
tiler images list --json | jq -e '.images | length > 0'  # a CI gate
tiler status --json | jq -r '.sessions[].name'           # agent inventory
tiler doctor --json | jq -r '.results[] | select(.status == "fail") | .name'
```

Every command shares one exit-code contract: 0 succeeds, 1 is a
runtime or probe failure, and 2 is a usage error.

`tiler doctor` prints two space-aligned sections. Resolved
configuration comes first, then the probes. Columns are padded with
spaces. A detail that does not fit the line wraps, and the
continuation is indented to the detail column:

```text
📋 Config

  path          /home/alice/.tiler.yaml
  images_dir    /home/alice/.tiler/images
  sessions_dir  /home/alice/.tiler/sessions
  cache_dir     /home/alice/.tiler/cache
  logs_dir      /home/alice/.tiler/logs
  log_level     info

🔧 Checks

  file_limit_images    pass  no single-file size limit below 512 GiB
  storage_perf_images  info  seq 112.5 MiB/s in 145.6 ms; rand avg ...
  contrib_concatfs     warn  optional tool; sources ship in the
                             contrib/ directory ...
```

## Configuration

tiler reads a layered YAML configuration. On first run it creates
`$HOME/.tiler.yaml` from a commented template that pins the built-in
defaults, so the file is a starting point to edit, never a
requirement — a read-only HOME is tolerated and tiler falls back to
the defaults. A compatible existing file is never rewritten.

The user file carries a top-level integer `version` (currently `1`).
When `$HOME/.tiler.yaml` is missing `version`, has `version < 1`, or
still lists a removed key such as `runtime_dir`, the next command
backups the original to `.tiler.yaml.bak` and rewrites the file to the
current schema (comments may be lost on rewrite). Only the user-level
file is upgraded; a project-level `.tiler.yaml` stays read-only and
fails loudly when incompatible.

```yaml
version: 1
images_dir: .tiler/images
sessions_dir: .tiler/sessions      # merged root: records + per-session runtime
cache_dir: .tiler/cache
logs_dir: .tiler/logs
log_level: info
# image_format: squashfs        # squashfs or erofs; omit to infer
# image_compression: zstd       # omit to use the format default
```

`image_format` and `image_compression` are optional. Leave them
unset to infer the container from the installed tools, with
squashfs preferred when both the squashfs and erofs stacks are
complete. `squashfs_options` is consumed only by the squashfs
implementation. There is no `erofs_options` key.

`sessions_dir` is the only session root. Each session is a self-contained
directory under it (`session.json`, `session.lock`, and
`runtime/m<seq>/` combination trees). Activity logs are not stored
there; they live under `logs_dir`. There is no separate global
`runtime_dir`.

`session_max_age` and `session_autoclose_stale` still load, including
an explicit zero, so an older file keeps parsing. Garbage collection
does not read them. `session_max_age: 0s` does not disable
`session gc`. Those commands use `--expire` only. `session close --rm`
still deletes on close and does not ask for a GC confirmation.

Values resolve in layers, each overriding the last: the built-in
defaults, the user file `$HOME/.tiler.yaml`, the project-level
`.tiler.yaml` files found by walking upward from the working
directory, and the command-line flags. A relative path in a file
resolves against that file's directory, and a `~/` prefix expands to
`$HOME`.

The upward walk stops at `$HOME` when the working directory is inside
it, otherwise at the filesystem root. `TILER_CEILING_DIR` stops it
earlier: when the ceiling is an ancestor of the working directory the
ceiling's own `.tiler.yaml` still applies but nothing above it is
read, and a ceiling that is not an ancestor is ignored. This bounds
discovery inside a test sandbox or a checkout that must not inherit a
parent directory's configuration.

`tiler doctor` reports the resolved configuration in the `📋 Config`
section before `🔧 Checks`: the nearest `.tiler.yaml` the walk
actually read (the user file when no project file exists) and the
core directories, one space-aligned row each.

The `--json` form carries the same data as a top-level `config`
object (`path`, `images_dir`, `sessions_dir`, `cache_dir`,
`logs_dir`, `log_level`) alongside `results`. The config section
never affects the doctor verdict.

## User story

A developer prepares a local environment quickly. On a new machine,
the mount itself completes in milliseconds and the workspace is usable
in seconds, while a full clone, dependency install, and build takes
minutes or hours.

The first stored image takes one command: a scene script prepares a
directory, `tiler build --image alice/app dir` packs it into the
library under `~/.tiler/images/`, and the next full build of the same
name publishes an immutable version without moving earlier payloads.

The read-only base stays immutable. The upper layer can be discarded
and mounted again.

Sessions isolate projects while they share one read-only base. Across
eight sessions that share one lower layer, each upper layer is about
6.5 MB in steady state.

A crash is recoverable because every mount action is recorded
write-ahead: after a `kill -9` of tiler or a FUSE daemon, one
`tiler session open <name>` replays the record, skips the views
that are still healthy, cleans and rebuilds the dead ones, and the
upper-layer data survives.

A large image on limited storage keeps working through the same
library. When the target filesystem caps single-file size — a
quota'd NAS share or a FAT-class volume — `tiler doctor` reports
the limit and advises `split_size` / `tiler build --split`. With a
positive block size, full and incremental layers store as verified
byte blocks; mounting reassembles each layer in the cache directory
and overlays them. One library, one command surface — the block
size is a configuration (or per-build flag) choice.

Machines read the same command surface. Every list and view
command — `images list`, `images show`, `session list`,
`session show`, `status`, and `doctor` — carries a `--json` form
with a stable, add-only schema pinned by golden files: a CI gate
fails on a missing image or a stale mount, and an agent picks the
cache setting from the doctor performance numbers without parsing
human text.

## License

Copyright 2026 Jiang Xin.

The project is licensed under [MIT](LICENSE). The license text
is the MIT License.
