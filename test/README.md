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
  `t0003` through `t0011` need `/dev/fuse` and the external
  FUSE/squashfs tools (`squashfuse_ll`, `fuse-overlayfs`,
  `fusermount3`, `mksquashfs`). `t0007-session-recovery.sh` and the
  stale case in `t0009-status.sh` also need `pgrep` to find the daemon
  they SIGKILL. Where they are absent, the case skips rather than
  false-fails.

A case only fails when its real assertion breaks on a properly
equipped host, so a genuine regression is never masked by a skip.

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
- `e2e/t0004-image-incr-cascade.sh` — builds a base full image, mounts
  it, and appends two distinct incremental layers, then confirms the
  chain cascades in `images ls` and in `meta.yaml`. A fresh remount
  serves the full plus both increments, `build --full` merges the live
  view into a new full (replacing `meta.yaml` and clearing the chain),
  and the old full + chain retire into a session-pinned `hold/`. Needs
  the FUSE toolchain; skips cleanly without it.
- `e2e/t0005-images-ls-gc-rm.sh` — exercises the library subcommands
  over a mounted image: `images ls` (all images, per-name chain,
  `--json`), `images gc` promoting an unreferenced hold into an
  archive, `images gc --keep-versions` expiring the older of two
  archives, the two-phase `images rm` (deprecate then remove), and
  `images rm` refused while an implicit `auto-*` session still
  references the image. Needs the FUSE toolchain; skips cleanly
  without it.
- `e2e/t0006-session-lifecycle.sh` — drives the `session` command over
  a named session end to end: `new` (with duplicate and invalid-name
  refusals), grouping views with `mount --session` and
  `TILER_SESSION_ID`, `list`/`show` rendering (text and `--json`),
  `show`/`open` on a missing record, `remove` refused while active,
  `umount` of one view leaving the session active, `close` tearing the
  views down in reverse and keeping the record (idempotent once
  closed), a mount into a closed session refused, and `remove`
  deleting the closed record. Needs the FUSE toolchain; skips cleanly
  without it.
- `e2e/t0007-session-recovery.sh` — simulates a daemon crash by
  SIGKILLing the FUSE process behind a named session's writable view
  (a `kill_view` helper), confirms the write-ahead record survives
  with its pre-crash `mounts=` count, then recovers with
  `session open`: the stale view is replayed, the upper layer survives
  (a file written before the crash is still there), a second `open` is
  a pure no-op, and a closed session revives through `open`. Needs the
  FUSE toolchain and `pgrep`; skips cleanly without them.
- `e2e/t0008-session-implicit.sh` — exercises the `session` command's
  view of the implicit `auto-*` records a plain `mount` creates:
  `list` renders the auto-named record, `show` (text and `--json`)
  matches the named shape, umounting the last view auto-closes the
  session, and an implicit session can still be `close`d and `remove`d
  by name. Needs the FUSE toolchain; skips cleanly without it.
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
  `runtime/m<seq>/` upper data; `mount --rm` marks the implicit
  `auto-*` session so umounting its last view deletes the record and
  purges the runtime; `mount --rm` refuses a session named by
  `--session` or `TILER_SESSION_ID` (exit 2) pointing at `session new
  --rm`; and a plain (non-`--rm`) close keeps the closed record and its
  runtime upper data as the contrast. Needs the FUSE toolchain; skips
  cleanly without it.
- `e2e/t0011-session-remount-different-image.sh` — mounts `alice/one` at
  a workdir, writes `marker.txt`, umounts (closing the first `auto-*`
  session), then mounts `alice/two` at the same workdir. Asserts two
  distinct `auto-*` sessions, that the second view has no `marker.txt`
  (session-scoped upper isolation), and that each session owns its own
  `sessions/<name>/runtime/m1/` tree. Needs the FUSE toolchain; skips
  cleanly without it.

## Adding cases in later phases

Drop a new `tNNNN-<name>.sh` into `e2e/`, sourcing the framework the
same way `t0000-doctor.sh` does (preset `TEST_LIB_DIRECTORY` and
`TEST_DIRECTORY` against the script location, then `. test-lib.sh`).
`make e2e` picks it up with no further wiring. This is where the
`../tiler-e2e` s00-s08 scenario suite will be migrated, one case at a
time.

## Run artifacts

The framework writes `test-results/` and `trash directory.*` into
`test/e2e/` at run time. These are ignored by the root `.gitignore`;
vendored sources and case scripts stay tracked.
