# e2e assertions

Shell cases under `e2e/` run on the vendored git-test-lib in
`git-test-lib/`. New assertions use the helpers already defined in
`git-test-lib/test-lib-functions.sh`. Do not add a `tiler_*` wrapper
in `e2e/sharness.d/` for an assertion that helper already covers.

`test_grep` takes the file as its last argument. Capture command
output first (`>out 2>err`), then match the file. A usage error is
exit 2: use `test_expect_code 2`, not `test_must_fail`.

## Prefer these helpers

| Instead of | Write |
|------------|-------|
| `test -f PATH` | `test_path_is_file PATH` |
| `test -d PATH` | `test_path_is_dir PATH` |
| `test -e PATH` | `test_path_exists PATH` |
| `test ! -e PATH` or `test ! -d PATH` | `test_path_is_missing PATH` |
| `cmp -s EXPECT ACTUAL` | `test_cmp EXPECT ACTUAL` |
| `grep -Eq PAT FILE` | `test_grep -E PAT FILE` |
| `grep -Fq PAT FILE` or `grep -q PAT FILE` | `test_grep -F PAT FILE` or `test_grep PAT FILE` |
| `! grep -F PAT FILE` | `test_grep ! -F PAT FILE` |
| `test ! -s FILE` | `test_must_be_empty FILE` |
| capture status, then `test "$status" -ne 0` | `test_must_fail CMD >out 2>err` |
| capture status, then `test "$status" -eq N` | `test_expect_code N CMD >out 2>err` |

A prefix assignment on the command (`VAR=value tiler ...`) becomes
`env VAR=value` under `test_must_fail` or `test_expect_code`, because
those helpers run `"$@"` and do not parse shell assignments.

## Leave as shell

These have no matching helper:

- `cut` of a hex id from a build or mount line, and `test "$a" = "$b"`.
- inode checks with `stat`.
- `test_atexit` cleanup that uses `|| true`.
- `grep -c` inside `$(...)` when the assertion is a count.
- pipelines (`grep ... | grep ...`) and `grep -A`, where the match is
  not a single file argument.
- `if grep` used as a branch, not as the assertion.
- a command that may exit 0 or 1 (`tiler doctor` on a partial
  toolchain). `test_might_fail` accepts every non-signal failure, so
  it is wider than "0 or 1".

Stdout that contains a fresh image id stays `test_grep -E`. Do not
build an expected file just to call `test_cmp` on that line.

## Do not use

`test_stdout_line_count` needs a Git worktree trash directory.
`test_cmp_config`, `test_oid*`, and `test_commit*` are Git helpers.
They do not apply to tiler cases.
