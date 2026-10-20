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

Optional helper tools ship as sources under `contrib/`: `concatfs`
(optional helper for manual byte-block concat; build needs gcc and
libfuse3-dev) and `slowfs` (latency injection for slow-NAS
comparisons; build needs a Go toolchain). Each directory records the
upstream origin, license, and build command. tiler prompts when a
scenario needs one and it is missing; it never installs them
automatically. tiler does not select concatfs as a mount route.

## Usage

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
- `tiler doctor [--json]` probes `/dev/fuse`, the external tools,
  and the squashfuse subdir baseline, then inspects the storage: a
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
- `tiler build [--full|--incr] [--image owner/repo] [dir]` packs a
  prepared directory into a full image, or, inside a writable merged
  view, appends the upper layer as one increment. A plain directory
  defaults to a full build and needs the explicit `owner/repo` name;
  a writable merged view defaults to an incremental build and infers
  the name from the mounted image. If the recorded incremental layers
  plus the live upper already exceed half the base image, that default
  becomes a full rebuild and a warning names `--incr` to keep appending.
  Success prints one line:
  `full <owner>/<repo> <id>` or `incr <owner>/<repo> <id>`. With
  `split_size` (or `tiler build --split <size>`) greater than zero,
  full and incremental builds store verified byte blocks instead of
  one `image.sqsh` / `incr/<id>.sqsh` (see Split storage below). The
  active form must stay consistent: blocks active only appends
  blocks layers; single active only appends single layers.
- `tiler images ls [owner/repo] [--archives] [--json]` lists every
  active image with its chain length. A name lists each chain id, and
  `--archives` adds the archived and held chain ids. A split active
  appends a `form=blocks` or `form=members` marker, and the
  single-name listing then shows a `blocks <n>` line or one
  `member <name>` line per partition. `--json` renders one
  machine-readable document instead, with the additive `form` and
  `members` fields for split actives.
- `tiler images gc` promotes held chains that nothing references
  into archives, then deletes archives beyond `--keep-versions` or
  `--max-age`. Zero means that rule is unlimited. The same pass
  cleans the session records: a closed record past
  `--session-max-age` is removed, an active record is never
  auto-deleted but one whose mounts are all dead is reported
  stale, and with `session_autoclose_stale` enabled (off by
  default) an aged stale record flips to closed and becomes
  eligible for a later removal. The one exception is a `--rm`
  active record whose views are all dead: it is torn down, its
  runtime purged, and its record removed whatever its age.
- `tiler images rm <owner/repo[/archives/<id>]>` deprecates the
  target on the first call and removes it on the second. Both calls
  refuse while a reference pins the target.
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
  outside the runtime tree and every reserved directory. Every
  mount is recorded write-ahead in a session before the engine
  runs: `--session <name>` attaches a session created with
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
  list [--json]` prints one line per record and `session show
  <name> [--json]` its full action log; the `--json` form of show
  is the record object itself. A generated name and an explicit
  name accept the same commands; only `--rm` changes close into a
  delete.
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

### Split storage

Split storage is an opt-in form for targets whose filesystem caps
single-file size — quota'd NAS shares, FAT-class volumes with a
4 GiB limit. Set a block size in configuration or on the build
command; zero (the default) keeps the single-file layout:

```yaml
split_size: 2G    # default for tiler build; 0 = off
```

```bash
tiler build --split 64k --full --image owner/repo /path/to/src
tiler build --split 0 ...   # this build stays single-file
```

When the effective size is greater than zero, **full and each
incremental layer** are stored as fixed-size byte blocks under
`blocks/<layer-id>/blk.NNN`. Digests and sizes on metadata still
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
tiler images ls --json | jq -e '.images | length > 0'    # a CI gate
tiler status --json | jq -r '.sessions[].name'           # agent inventory
tiler doctor --json | jq -r '.results[] | select(.status == "fail") | .name'
```

Every command shares one exit-code contract: 0 succeeds, 1 is a
runtime or probe failure, and 2 is a usage error.

`tiler doctor` prints one tab-separated line per probe, for
example (details elided):

```text
file_limit_images	pass	no single-file size limit below 512 GiB
storage_perf_images	info	seq 112.5 MiB/s in 145.6 ms; rand avg 6.12 ms ...
storage_perf_cache	info	seq 4180.2 MiB/s in 3.9 ms; rand avg 0.05 ms ...
cache_advice	warn	image storage random read averages 6.12 ms, ...
contrib_concatfs	warn	optional tool; sources ship in the contrib/ ...
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
images_dir: ~/.tiler/images
sessions_dir: ~/.tiler/sessions      # merged root: records + per-session runtime
cache_dir: ~/.tiler/cache
log_level: info
```

`sessions_dir` is the only session root. Each session is a self-contained
directory under it (`session.json`, `session.lock`, and
`runtime/m<seq>/` combination trees). There is no separate global
`runtime_dir`.

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

`tiler doctor` reports the resolved configuration before the probe
table — the config file location and the core directories, one
tab-separated `config` line each:

```text
config	path	/home/alice/.tiler.yaml
config	images_dir	/home/alice/.tiler/images
config	sessions_dir	/home/alice/.tiler/sessions
```

The `--json` form carries the same data as a top-level `config`
object (`path`, `images_dir`, `sessions_dir`, `cache_dir`,
`log_level`) alongside `results`. The config section never affects
the doctor verdict.

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
command — `images ls`, `session list`, `session show`, `status`,
and `doctor` — carries a `--json` form with a stable, add-only
schema pinned by golden files: a CI gate fails on a missing image
or a stale mount, and an agent picks the cache setting from the
doctor performance numbers without parsing human text.

## License

Copyright 2026 Jiang Xin.

The project is licensed under [MIT](LICENSE). The license text
is the MIT License.
