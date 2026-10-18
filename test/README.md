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
- **No FUSE toolchain** — `tiler doctor` and the mount case
  `t0003-image-mount.sh` need `/dev/fuse` and the external
  FUSE/squashfs tools (`squashfuse_ll`, `fuse-overlayfs`,
  `fusermount3`, `mksquashfs`). Where they are absent, the case skips
  rather than false-fails.

A case only fails when its real assertion breaks on a properly
equipped host, so a genuine regression is never masked by a skip.

## Cases

- `e2e/t0000-doctor.sh` — `tiler doctor` exits 0 on an equipped host.
  Proves the vendored framework runs inside the repository.
- `e2e/t0001-config-bootstrap.sh` — tiler creates `$HOME/.tiler.yaml`
  from a template on first run, never rewrites an edited one, and
  `doctor` reports the config location and core dirs (text and `--json`).
- `e2e/t0002-config-ceiling.sh` — `TILER_CEILING_DIR` bounds the upward
  project-config walk so it stays inside the sandbox.
- `e2e/t0003-image-mount.sh` — builds a full image from a source tree,
  mounts it read-write (writing into the upper layer), confirms a repeat
  mount of the same view is idempotent (`already mounted`), builds a
  second image with a distinct id and mounts it at its own target, and
  checks that mounting a different image onto a live view is refused as
  a conflict rather than silently restacked. Needs the FUSE toolchain;
  skips cleanly without it.

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
