# tiler tests

This directory holds tiler's in-repo, shell-based end-to-end (e2e)
tests. They live inside the repository so contributors and CI can
reproduce them, unlike the older out-of-tree smoke scripts.

## Layout

- `git-test-lib/` — a vendored, pinned snapshot of the
  [git-test-lib](https://github.com/jiangxin/git-test-lib) shell test
  framework (Git's own `t/` harness, repackaged). This is the base the
  cases run on. See [`git-test-lib/VENDORED.md`](git-test-lib/VENDORED.md)
  for provenance, license, and the reproducible update procedure.
- `e2e/` — tiler's own e2e cases, one file per scenario, named
  `tNNNN-<name>.sh` following the framework convention.

## Running

Build the binary first, then run every case:

```sh
make build && make e2e
```

`make e2e` iterates all `e2e/t[0-9][0-9][0-9][0-9]-*.sh` cases, so new
cases are picked up automatically with no Makefile change. To run a
single case directly:

```sh
cd test/e2e && sh ./t0000-doctor.sh
```

`make e2e` is **not** part of the default completion gates
(`make fmt`, `make lint`, `make test`); it is an opt-in target.

## Skip semantics

Cases skip cleanly (exit 0) instead of failing when the environment
cannot support them:

- **No tiler binary** — build it with `make build`, or put `tiler` on
  `PATH`. `make e2e` prints a notice and the case reports a TAP skip.
- **No FUSE toolchain** — `tiler doctor` and the mount-driven cases
  `t0003` through `t0014` need `/dev/fuse` and the external
  FUSE/squashfs tools (`squashfuse_ll`, `fuse-overlayfs`,
  `fusermount3`, `mksquashfs`). `t0007-session-recovery.sh` and the
  stale case in `t0009-status.sh` also need `pgrep` to find the daemon
  they SIGKILL. Where they are absent, the case skips rather than
  false-fails. The initial cases in `t0026-activity-gc.sh` preview
  and delete without a mount. Record-only cases need just `tiler`;
  its packing case needs `mksquashfs` and `squashfuse_ll`, because
  the CLI checks the format client even when no mount is requested.
  Its separate activity/ancestor graph needs the FUSE toolchain.
- **No EROFS toolchain** — `t0015-erofs-image.sh` needs `/dev/fuse`,
  `mkfs.erofs`, `erofsfuse`, `fuse-overlayfs`, `fusermount3`, and
  `git`. It skips cleanly on a squashfs-only host.
- **Wrong OS / no hdiutil** — `t0020-build-idempotent.sh` lists
  `LINUX` with the FUSE toolchain so Darwin `make e2e` skips it.
  `t0021-macos-build-idempotent.sh` lists `MACOS` and skips when
  `hdiutil` is missing.

A case only fails when its real assertion breaks on a properly
equipped host, so a genuine regression is never masked by a skip.

## Linux comprehensive workflows

The three workflow suites combine commands over real project data.
Command-specific cases below remain as focused regression coverage.
Each workflow owns a sandbox project with `prepare/`, `.tiler.yaml`,
and `.tiler/{images,sessions,cache,logs}`. The four storage paths are
relative to the project config, so working inside a mounted Git tree
still uses that project's library. Mountpoints stay outside `.tiler/`.
The standard and import projects omit `split_size`; the split project
sets `split_size: 1M`. Each build explicitly selects squashfs.

| Suite | Project | Coverage |
| --- | --- | --- |
| `t0100-linux-standard-workflow.sh` | `ws-standard/` | Tagged Git full, both `--rm` paths, session GC, incremental replay/pruning, descendants, siblings, and a rebuilt full |
| `t0101-linux-import-workflow.sh` | `ws-import/{remote,local}/` | File and dependency-closure link/copy imports, mounted no-op, source outage/recovery, and safe local deletion |
| `t0102-linux-split-workflow.sh` | `ws-split/` | Configured full/incremental blocks, exact boundaries/order, block and reassembled SHA256, and fresh mounted binary content |

The standard project publishes `standard/app`. Its version variables
are `base_id`, `delta_id`, `chain_tip_id`, `branch_tip_id`, and
`rebuilt_full_id`. The graph is `base -> delta -> chain` plus
`base -> branch`, followed by an independent full root.

| Standard scenario | Mountpoint | Session |
| --- | --- | --- |
| Implicit `--rm` | `mnt-auto-rm/` | Generated; captured as `auto_rm_session` |
| Named `--rm` | `mnt-named-rm/` | `scratch-edit` |
| Retention and GC | `mnt-gc/` | `gc-check` |
| Increment and replay | `mnt-edit/` | Generated; captured as `edit_session` |
| Descendant increment | `mnt-chain/` | `chain-edit` |
| Sibling increment | `mnt-branch/` | `branch-edit` |
| Full rebuild | `mnt-rebuild/` | `full-rebuild` |
| Published-version verification | `mnt-verify/` | A fresh generated `--rm` session each time |

Replay checks distinguish files already baked into the new lower from
later upper edits. Published-version checks use new sessions, then
delete those sessions before reusing `mnt-verify/`; residual upper data
cannot make a broken image look correct. The full rebuild also compares
peer session records to prove their immutable pins did not change.

The import source publishes `source/app` in `remote/prepare/`, with
`source-edit` at `remote/mnt-edit/`. Both remote and local have their
own config and storage directories. Local imports use these names:

| Image | Local mountpoint | Source selection |
| --- | --- | --- |
| `linked/file` | `mnt-link-file/` | Full payload file, `--link` |
| `copied/file` | `mnt-copy-file/` | Full payload file, `--copy` |
| `linked/chain` | `mnt-link-chain/` | Image directory, full then incremental `--hash`, `--link` |
| `copied/chain` | `mnt-copy-chain/` | Image directory, full then incremental `--hash`, `--copy` |

Import verification uses generated `--rm` sessions. Before moving the
source offline, it destroys all old verification sessions and clears
the local cache. Fresh link mounts must fail, fresh copy mounts must
succeed, and restored-source link mounts must recover. Source metadata
and payload snapshots remain unchanged after local imports and removal.

The split project publishes `split/app`, edits through `split-edit`
at `mnt-edit/`, and verifies through a fresh `--rm` session at
`mnt-verify/`. Its two 2 MiB source files are
`prepare/bin/base-a.bin` and `base-b.bin`; the increment adds a
2 MiB `bin/delta.bin`. Non-final blocks must be 1,048,576 bytes;
the final block is nonempty and at most that size. The checks compare
all three binaries after mounting the published chain independently.

These suites are ordered lifecycles with shared fixtures; later tests
depend on setup and publications earlier in the same file. Run the
whole suite for workflow validation, rather than selecting an arbitrary
later test with `--run`. For example, from the repository root:

```sh
make build
sh test/e2e/t0100-linux-standard-workflow.sh -v -x
sh test/e2e/t0101-linux-import-workflow.sh -v -x
sh test/e2e/t0102-linux-split-workflow.sh -v -x
```

An absolute script path also works from another working directory;
artifacts remain under `test/e2e/`. Mounting needs
`TILER,LINUX,FUSE_TOOLCHAIN`; the Git-backed suites also require Git.
Pure packing checks use `SQUASHFS_PACK` and
`WORKFLOW_SQUASHFS_CLIENT` without requiring `/dev/fuse`, overlay,
or unmount tools. `lib-workflow.sh` defines the client and Git probes,
project/Git fixture setup, and the limited block-manifest reader.
Assertions still use the existing git-test-lib helpers.

The standard and import scripts set `TEST_NO_MALLOC_CHECK=1` before
loading the framework. Git's glibc allocator instrumentation otherwise
aborts the external squashfuse daemon while reading Git objects; the
Go binary and all content/replay assertions are still exercised.
Neither suite changes the shared bootstrap or disables chain lint.

## Cases

- `e2e/t0000-doctor.sh` — `tiler doctor` exits 0 on an equipped host.
  Proves the vendored framework runs inside the repository.
- `e2e/t0001-config-bootstrap.sh` — tiler creates `$HOME/.tiler.yaml`
  from a template on first run (with `version: 1`), leaves a
  current-schema edit untouched, upgrades a legacy file that still
  lists `runtime_dir` (writing `.tiler.yaml.bak`), and `doctor`
  reports the config location and core dirs (text and `--json`).
- `e2e/t0002-config-ceiling.sh` — `TILER_CEILING_DIR` bounds the upward
  project-config walk so it stays inside the sandbox.
- `e2e/t0003-image-mount.sh` — builds a full image from a source tree,
  mounts it read-write (writing into the upper layer), confirms a repeat
  mount of the same view is idempotent (`already mounted`), builds a
  second image with a distinct id and mounts it at its own target, and
  checks that mounting a different image onto a live view is refused as
  a conflict rather than silently restacked. Needs the FUSE toolchain;
  skips cleanly without it.
- `e2e/t0004-image-incr-cascade.sh` — builds mixed-format sibling
  increments with a shared full parent, checks their history, and
  merges a new independent full. Historical mounts cover immutable
  pins, reference-index recovery, retained dependencies, and edited
  descriptions. Needs both squashfs and EROFS toolchains. Activity
  propagation and ancestor GC coverage live in `t0026`.
- `e2e/t0005-images-ls-gc-rm.sh` — exercises the library subcommands
  over a mounted image: `images list` / `images show` (all images,
  one-name detail, `--json`), a GC preview that does not delete while
  a session is attached, and pinned or complete repository removal.
- `e2e/t0006-session-lifecycle.sh` — drives the `session` command over
  a named session end to end: `new` (with duplicate and invalid-name
  refusals), grouping views with `mount --session` and
  `TILER_SESSION_ID`, `list`/`show` rendering (text and `--json`),
  `show`/`open` on a missing record, `remove` refused while active,
  `umount` of one view leaving the session active, `close` tearing the
  views down in reverse and keeping the record (idempotent once
  closed), a `session gc` preview that lists the closed record and
  does not delete it, a mount into a closed session refused, and
  `remove` deleting the closed record. Needs the FUSE toolchain;
  skips cleanly without it.
- `e2e/t0026-activity-gc.sh` — GC preview, activity, and retention. A legacy
  `session_max_age: 0s` file still loads and does not block
  `session gc`. The session half checks the preview, a piped `yes`,
  one-time log initialization, and `--yes` removal of `runtime/`
  upper data. Its mount-free image case requires the squashfs packer
  and format client. A separate squashfs graph checks exact parent
  activity propagation, unchanged sibling logs, one-time missing-log
  initialization, and retaining an ancestor of a recent child while
  expired siblings and an independent full are removed. That graph
  needs FUSE, but neither EROFS nor the earlier mount-free fixture.
  Use `--run=1-4` for the original independent cases or `--run=5-8`
  for the complete graph sequence.
- `e2e/t0007-session-recovery.sh` — simulates a daemon crash by
  SIGKILLing the FUSE process behind a named session's writable view
  (a `kill_view` helper), confirms the write-ahead record survives
  with its pre-crash `mounts=` count, then recovers with
  `session open`: the stale view is replayed, the upper layer survives
  (a file written before the crash is still there), a second `open` is
  a pure no-op, and a closed session revives through `open`. Needs the
  FUSE toolchain and `pgrep`; skips cleanly without them.
- `e2e/t0008-session-implicit.sh` — exercises the `session` command's
  view of the adjective-animal records a plain `mount` creates:
  stdout prints `new session created:`, `list`/`show` match the named
  shape, umounting the last view leaves a non-`--rm` session active,
  and the session can still be `close`d and `remove`d by name. Needs
  the FUSE toolchain; skips cleanly without it.
- `e2e/t0009-status.sh` — drives the `status` command over real mounts
  in both forms. The no-path overview reports an empty store, a healthy
  session marked `current` via `TILER_SESSION_ID` (and a marker that
  matches no record, which is not an error), a closed session with no
  live views, and a live view whose record was deleted out-of-band as an
  `orphan`. The path form resolves a deep path and the mountpoint to the
  covering session, refuses an uncovered path (exit 1), and marks a
  SIGKILLed view `stale`. Every case is asserted in both text and
  `--json`. Needs the FUSE toolchain (the stale case also needs
  `pgrep`); skips cleanly without them.
- `e2e/t0010-session-rm.sh` — drives the `--rm` opt-in that deletes a
  session and purges its runtime on close. A named `session new --rm`
  shows the `rm` marker and its `close` prints `removed`, deletes the
  record, and force-removes the `sessions/<name>/` directory including
  `runtime/m<seq>/` upper data; `mount --rm` marks the generated
  session so umounting its last view deletes the record and
  purges the runtime; `mount --rm` refuses a session named by
  `--session` or `TILER_SESSION_ID` (exit 2) pointing at `session new
  --rm`; and a plain (non-`--rm`) close keeps the closed record and its
  runtime upper data as the contrast. Needs the FUSE toolchain; skips
  cleanly without it.
- `e2e/t0011-session-remount-different-image.sh` — mounts `alice/one` at
  a workdir, writes `marker.txt`, umounts (leaving the first generated
  session active), then mounts `alice/two` at the same workdir. Asserts
  two distinct generated sessions, that the second view has no `marker.txt`
  (session-scoped upper isolation), and that each session owns its own
  `sessions/<name>/runtime/m1/` tree. Needs the FUSE toolchain; skips
  cleanly without it.
- `e2e/t0012-build-split-size.sh` — builds a full image with a small
  `--split` into multiple blocks, mounts it, writes the upper, builds
  an incr with the same `--split` into `_objects/<incr-id>/`, remounts
  to verify the upper file. Omitting
  `--split` or setting project `split_size: 0` stays a single
  `image.sqsh`. Needs the FUSE toolchain; skips cleanly without it.
- `e2e/t0014-squashfs-compression.sh` — builds a full image with
  non-default `--compression gzip`, mounts it read-write, and appends
  an incremental layer with no format flags so the layer inherits
  `squashfs/gzip`. A fresh remount serves the upper file. A squashfs
  packed outside the library with `mksquashfs -comp xz` mounts
  read-write. The active file stays `image.sqsh`. Needs the FUSE
  toolchain; skips cleanly without it.
- `e2e/t0015-erofs-image.sh` — rejects `erofs` with `xz` (exit 2),
  packs a full image from project `image_format: erofs` (no
  `--format` flag) as `image.sqsh` with the erofs compression
  `mkfs.erofs` can pack (zstd, otherwise lz4, then gzip), mounts it
  read-write, and appends an incremental layer that inherits that
  pair. The same project config with `--split` publishes blocks and
  still mounts. `--subdir` exposes one image directory as the mount
  root. `tiler doctor` exits 0 when `PATH` offers only the erofs
  stack. Needs the EROFS toolchain; skips cleanly without it.
- `e2e/t0020-build-idempotent.sh` — packs the same bytes twice and
  asserts the active id and `meta.yaml` stay put for a plain-directory
  full, an incremental upper after a writable mount, and a subdirectory
  of that mount published under a different `--image` as its own full.
  Needs the FUSE toolchain; Linux-only (`LINUX`); skips on macOS.
- `e2e/t0021-macos-build-idempotent.sh` — drives the three macOS dmg
  shapes twice: plain-directory create, writable-mount shadow bake,
  and a subdirectory of that mount under a different `--image`
  (directory create, not a bake). Each repeat must keep the same id
  and `meta.yaml`. Needs `hdiutil`; macOS-only (`MACOS`);
  skips on Linux and when `hdiutil` is missing.

## Adding cases in later phases

Drop a new `tNNNN-<name>.sh` into `e2e/`, sourcing `lib-tiler.sh`
relative to the script as `t0000-doctor.sh` does. `make e2e` picks it
up with no further wiring. Keep scenario-specific helpers inside the
case or a focused `lib-*.sh`; do not modify the vendored framework.

Shared helpers load automatically from `e2e/sharness.d/*.sh` (see
`e2e/sharness.d/tiler.sh`): `tiler_bin`, the `TILER` prereq, OS
prereqs `LINUX` and `MACOS`, lazy prereqs `FUSE_TOOLCHAIN`,
`EROFS_TOOLCHAIN`, `SQUASHFS_PACK`, and `KILL_VIEW`, plus `kill_view`. Mount cases
list `LINUX` with the toolchain so `make e2e` on macOS skips them
instead of failing; later macOS mount cases will list `MACOS`.
Config-only cases stay on `TILER` alone and run on both hosts.
Case scripts keep their own path variables and `test_atexit`
teardown lists. Assertions use the git-test-lib helpers listed in
[`AGENTS.md`](AGENTS.md) (`test_path_is_file`, `test_cmp`,
`test_grep`, `test_must_fail`, `test_expect_code`).

## Run artifacts

The framework writes `test-results/` and `trash directory.*` into
`test/e2e/` at run time. These are ignored by the root `.gitignore`;
vendored sources and case scripts stay tracked.
