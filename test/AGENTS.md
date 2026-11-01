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
| `test "$(grep -c PAT FILE)" = N` | `grep PAT FILE >matches && test_line_count = N matches` |
| capture status, then `test "$status" -ne 0` | `test_must_fail CMD >out 2>err` |
| capture status, then `test "$status" -eq N` | `test_expect_code N CMD >out 2>err` |

A prefix assignment on the command (`VAR=value tiler ...`) becomes
`env VAR=value` under `test_must_fail` or `test_expect_code`, because
those helpers run `"$@"` and do not parse shell assignments.

## Leave as shell

These have no matching helper:

- `cut` of a hex id from a build or mount line, and `test "$a" = "$b"`.
- inode comparisons after a checked `inode=$(stat ...)` assignment.
- `test_atexit` cleanup that uses `|| true`.
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

## Structure and isolation

Use Git's `t/README` and recent `t/tXXXX-*.sh` cases as the style
reference, adapting Git-specific assumptions to this CLI. Useful examples
include `t2082-parallel-checkout-attributes.sh` (isolated subshells and
expected files) and `t5004-archive-corner-cases.sh` (explicit prerequisites).
The vendored helper implementation is the authority on availability;
do not modify `git-test-lib/` to refactor tiler's tests.

Set `test_description`, then source `e2e/lib-tiler.sh` relative to `$0`.
The bootstrap resolves the script directory before the framework changes
cwd and HOME, pins artifacts under `e2e/`, and clears
`TEST_NO_CREATE_REPO` so the framework initializes a Git repository at
the sandbox root. Keep that repository: it stops Git discovery from
reaching and modifying tiler's enclosing source checkout, even without
a Git ceiling. The bootstrap clears inherited `TILER_SESSION_ID` and
`SOURCE_DATE_EPOCH`, and exports a `TILER_CEILING_DIR` at the physical
trash root. Create nested Git repositories explicitly for scenarios
that need their own source history. Keep shared tiler capabilities in
`e2e/sharness.d/tiler.sh`;
keep scenario-specific helpers in the case or a focused `lib-*.sh`.

Use single-quoted test bodies, tab indentation, and `&&` between steps.
Every loop iteration must propagate failure (`|| return 1`), and the
loop itself must be chained. Put directory changes in subshells so a
failed assertion cannot change later tests' working directory.

Keep setup commands and capability probes inside test blocks or lazy
prerequisites, where failures are reported or skipped explicitly. Use
`test_expect_success PREREQ,...` for platform and tool requirements;
macOS disk-image cases require `MACOS,HDIUTIL`. Each dependent test must
include the prerequisites needed to create its shared fixture. Independent
packing assertions should not require a mount toolchain. Whole-file
`skip_all` is reserved for requirements shared by every test in that file.

Cases may share an expensive mounted fixture across a lifecycle sequence;
state that dependency clearly. Register `test_atexit` teardown before
mounting so the view is detached before trash removal, even on failure.
Use `test_when_finished` for resources owned by one test; do not tear down
a fixture still needed by subsequent tests. Preserve the platform unmount
fallbacks and keep mounts inside the framework's sandbox.

## Failure visibility and precise assertions

Never mask a tiler failure with `|| true`, `! tiler`, a pipeline, or a
command substitution passed directly to another command. Capture output
and check the producing command first, then inspect the file. For doctor,
when both a healthy and an incomplete toolchain are valid, capture the
status and explicitly accept only 0 or 1. `test_might_fail` is too broad
for this contract. Use `!` for an expected failure of an ordinary system
command, such as a write to a read-only mount; reserve `test_must_fail`
for tiler failures whose exact code is not part of the assertion.

Use `test_cmp` for complete deterministic output and exact file contents;
use `test_grep` for selected fields or output containing generated IDs.
For negative assertions, establish the file exists and was produced
successfully: an empty pipeline or failed producer must not count as
proof of absence. For directory emptiness, capture `find` output and use
`test_must_be_empty`. Redirect the tested command's stderr directly when
checking it, rather than capturing a whole subshell's trace under `-x`.

Use bounded polling of an observable condition for asynchronous daemon
state changes. Do not assume a fixed sleep proves completion, and do not
retry an entire test or mutation to hide a failure. Keep generated data
as small as the scenario permits; use random payloads only when the test
needs incompressible bytes.

## Validation

Run `make e2e` after changing cases or shared helpers. The framework runs
its chain lint by default; keep it enabled. Check changed cases with
`-v -x` as well, and verify invocation from outside `test/e2e/` when
changing bootstrap paths. Skips are capability gaps, not executed
coverage: report macOS scripts as unverified on Linux and vice versa.
Run the root quality gates too. Do not add Git worktree assumptions to
mount-free cases merely to reuse a Git-only helper.

## Fixtures, diagnostics, and capability coverage

`test_line_count` is usable without a Git worktree, unlike
`test_stdout_line_count`. Follow `t7700-repack.sh`: capture selected
records, check the selector succeeds, then count with `test_line_count`
so failures show the actual records. If zero matches are valid, use a
selector such as `sed -n` that succeeds on no matches; do not hide a read
error with `|| true`.

Follow the readable fixture style in `t0610-reftable-basics.sh` and
`t1300-config.sh`: use `cat >file <<-\EOF &&` with tab-indented literal
configuration lines. Use an unquoted delimiter only when expansion is
intentional. A claim that an existing file is unchanged or backed up
requires a before snapshot and `test_cmp`; finding one preserved marker
cannot prove that the rest of the file survived.

Git's `t/README` explicitly warns against redirecting a compound
command's stderr when examining command diagnostics. Put `2>"$HOME/err"`
on the tiler invocation inside the subshell, and capture the subshell's
stdout outside it. The absolute error path still refers to the trash
root after `cd`; `-x` output then stays in the harness trace.

Use `test_grep -F -e "$path" -e "$physical_path" file` for accepted
literal path alternatives, rather than raw `grep ... || grep ...`.
Capture `stat` output in a checked assignment before comparing inodes;
a substitution inside `test` discards the producer's exit status.

For several analogous assertions, follow the parameterized tests in
Git's `t/README`: loop over values outside `test_expect_success`, expand
the value in the title, and keep the body single-quoted. Each probe then
has its own TAP result and useful failure diagnostics. Select structured
report fields into a file before asserting; do not use a negative
pipeline that could silently pass if a selector fails. If a field is
allowed to be absent, document that explicitly.

Declare the minimum capabilities that each assertion actually needs.
Packing without mounting needs `SQUASHFS_PACK`, not `FUSE_TOOLCHAIN`.
An erofs full followed by a default squashfs increment also needs the
squashfs packer, including in tests that consume that increment later.
Include capability checks the CLI performs before reaching the behavior
under test: even an invalid compression case needs a complete toolchain
when build checks tools first.
Validate both the equipped path and the missing-tool skip path when
changing prerequisites; an equipped host alone cannot reveal a missing
prerequisite or an unnecessarily broad skip.

## Environment isolation and behavioral evidence

Git's `t/test-lib.sh` resets product environment variables and exports
`GIT_CEILING_DIRECTORIES`; its isolation does not extend to tiler's own
inputs. Keep tiler environment defaults in `e2e/lib-tiler.sh` after the
framework establishes HOME. A caller's session or reproducible-build
settings must not change an unrelated case. Bound configuration discovery
to the physical trash root so a project file above it cannot redirect
test storage. Test a specific environment setting with a command-scoped
assignment or a subshell, leaving the bootstrap defaults for later tests.
For symlinked test roots, enter configuration fixtures with `cd -P` and
construct their ceiling with `pwd -P`: Go's `os.Getwd` may otherwise
honor the shell's logical PWD while the ceiling uses a physical path.

Follow `t0610-reftable-basics.sh` when a rejected operation promises to
preserve existing state: take a snapshot and compare it after the error.
Check the expected exit code and empty stdout when failure must not
announce success. Assert absence of the actual session record, not its
containing directory: locking a missing session can legitimately create
a directory and a lock file without creating `session.json`.

Follow `t2082-parallel-checkout-attributes.sh` for content evidence:
compare known source files with their mounted counterparts. Repeating a
mount must preserve upper contents as well as report success. Isolation
requires checking that one session's data survives while another cannot
see it; comparing two strings constructed from distinct session names
only repeats what the test already knows. An unmount test must verify
that the mounted files disappear, not merely that the command exits 0.

Keep independent validation and record-only tests runnable with `TILER`
even when neighboring lifecycle tests need FUSE. Use a separate fixture
and `test_when_finished` for a duplicate-record rejection test so it does
not inherit mounted state or leave records for the lifecycle to consume.
Use per-operation TAP cases for missing-record errors. Prefix loop
variables that cross framework calls with `tiler_`: the vendored harness
uses globals such as `operation` while matching `--run`, so a generic
loop variable can be overwritten before its single-quoted body runs.
Verify independent cases with `--run` to catch this class of collision.
For an expensive
shared lifecycle, keep its dependencies explicit instead of pretending
all blocks can run alone.

Comments should explain fixture ownership, host requirements, or a
non-obvious assertion. Let descriptive test titles explain the steps;
remove obsolete scenario inventories and external harness references
when refactoring them. A title must describe the behavior actually
asserted, such as a cumulative tip rather than a chain of sibling layers.
Validate shared bootstrap changes with inherited tiler variables and an
invalid project file above a relocated trash root, as well as the normal
suite. Keep real host mount validation separate from these isolation
checks.
