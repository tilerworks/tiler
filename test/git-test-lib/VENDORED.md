# Vendored: git-test-lib

This directory holds a flattened, pinned snapshot of the `git-test-lib`
shell test framework. It is vendored into tiler (ordinary tracked
files, not a git submodule) so in-repo end-to-end tests under
`../e2e/` can source it directly and contributors can reproduce the
exact same snapshot.

## Provenance

- Upstream: https://github.com/jiangxin/git-test-lib
- Clone URL: `git@github.com:jiangxin/git-test-lib.git`
- Branch: `master`
- Pinned commit: `7b81101bd0d9e9f6c1841281d6fa02e3c1ee0691`
- Commit date: `2025-02-05`
- Commit subject: `fixup! test-lib: load test extensions from extension dir`
- Snapshot taken: `2026-10-18`

git-test-lib is Git's own `t/` test framework (test-lib.sh and
friends), filtered out of the Git project and repackaged by Jiang Xin
so it can be reused outside Git without a C build (the `test-tool`
helper is reimplemented in Python under `helper/`).

## License

GNU General Public License version 2 (GPLv2), inherited from the Git
project. The full license text is provided verbatim in `COPYING`.

Upstream ships no file named `LICENSE`; the `LICENSE` in this
directory is a tiler-added pointer notice that refers to `COPYING`.
`VENDORED.md` and `LICENSE` are the only tiler-added files here —
everything else is the unmodified upstream snapshot. Preserve both
tiler-added files when refreshing the snapshot.

## What was changed from upstream

- The upstream `.git/` directory is excluded (the snapshot is exported
  with `git archive`, so only tracked files are copied).
- No upstream CI was stripped: this pinned commit ships no
  `.github/workflows/`.
- No other content is modified. Files, modes, and the
  `test-example/lib -> ..` symlink are preserved verbatim.

## Layout

- `test/git-test-lib/` — this framework snapshot.
- `test/e2e/` — tiler's own end-to-end cases (phase 1 ships a single
  `tiler doctor` case). A case sources the framework by presetting
  `TEST_LIB_DIRECTORY` to this directory before `. test-lib.sh`, which
  works under both bash and dash.

## How to update this snapshot

Reproducible refresh procedure:

1. Clone upstream outside the repo and check out the target commit:

   ```sh
   git clone git@github.com:jiangxin/git-test-lib.git /tmp/git-test-lib
   git -C /tmp/git-test-lib checkout <target-commit>
   ```

2. Replace the tracked snapshot, then restore the tiler-added files
   from git. `VENDORED.md` and `LICENSE` are not part of upstream, but
   they are tracked, so git restores them after the `rm -rf`:

   ```sh
   # from the tiler repo root
   rm -rf test/git-test-lib
   mkdir -p test/git-test-lib
   git -C /tmp/git-test-lib archive <target-commit> |
     tar -x -C test/git-test-lib
   git checkout HEAD -- test/git-test-lib/VENDORED.md test/git-test-lib/LICENSE
   ```

3. Update the restored `VENDORED.md` and `LICENSE`: the pinned commit
   SHA, commit date, and snapshot date recorded above.

4. Verify: `make build && make e2e`, then read `../README.md` for the
   e2e layout and skip semantics. The `test/e2e/` cases, the `make e2e`
   target, and `test/README.md` are added alongside this snapshot by
   the tiler e2e wiring; if they are not present yet, fall back to
   `make lint test`.
