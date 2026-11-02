# Engineering constraints

This file sits beside the Makefile quality gates. It records language,
runtime, dependency, and completion rules for tiler.

## Language

Code comments, in-repo documents, and Git commit messages are English.
Design documents live outside this repository and are not vendored, so
they may use another language. Describe design points in words. Do not
cite chapter numbers, page numbers, or requirement numbers from those
external documents. Test names are descriptive English and carry no
external document numbers.

`README.md` and `README.zh.md` are synchronized English and Chinese
versions of the same document. Keep their content, section order,
examples, commands, links, diagrams, and data equivalent; update both
files together whenever either changes. Language-switch links point
to the other version. `README.zh.md` is an exception to the English-only
rule for in-repo documents.

Hard-wrap English prose in `README.md` at about 80 columns. Keep Markdown
links and inline code intact; headings, tables, and fenced code blocks
may exceed that width. Do not hard-wrap Chinese paragraphs in
`README.zh.md`: joining wrapped lines can insert unwanted spaces between
Chinese characters. These line-wrapping differences are intentional and
do not change the requirement to keep both versions in sync.

## Runtime and product surface

Builds use Go 1.25 or newer with `CGO_ENABLED=0` static binaries.
Call git and FUSE tools only through `exec`. Do not add go-git, and do
not add CGO FUSE bindings. Platform-specific code lives in
`//go:build` files (`linux`, `darwin`, and `!linux && !darwin`), not
in a single Linux package. Integration tests that need root or mounts
call `t.Skip` when that permission is missing.

The default runtime layout is under the user home directory:
`$HOME/.tiler/images`, `$HOME/.tiler/sessions`, `$HOME/.tiler/cache`,
and `$HOME/.tiler/logs`. Session records and combination trees share
`sessions_dir` (`sessions/<name>/` with `runtime/m<seq>/` under it).
Activity logs live under `logs_dir`, not in the image object or
session directory: `logs_dir/images/<name>/<id>.log` and
`logs_dir/sessions/<name>.log`. The lock file is the log name plus
`.lock`.
tiler does not gain a registry, a pull command, or certificate
signing. Integrity checks, when added later, use sha256 only.

## Platforms

The library, session record, and import graph are the same on every
host. The container and the writable upper are not.

Linux packs and mounts squashfs and erofs through FUSE. A writable
view is an overlay upper. Mounting needs `/dev/fuse` and the pack
tools. `dmg` is refused. macOS packs and mounts UDIF disk images
through `hdiutil`, also reached only by `exec`. A writable view is
the session shadow `upper.shadow`. The host default format is `dmg`.
`squashfs` and `erofs` are refused. A new image uses APFS, or HFS+
when this OS cannot `attach -shadow` an APFS image. That choice is
not a configuration key.

`dmg` metadata stays readable on a host that did not register the
format, so a library copied to Linux can still be shown. Format
policy applies when linking, building, or mounting, not when decoding
`meta.yaml`.

Shell end-to-end cases name the host they need. `LINUX` and
`FUSE_TOOLCHAIN` cover the FUSE stack. `MACOS` covers `hdiutil`.
`make e2e` skips a case whose prerequisite is missing. A green Linux
run does not execute the macOS disk-image scripts.

## Keeping both hosts green

`make darwin-check` and `make linux-check` only compile tests. They
do not run them, and they do not see symlink resolution, timers, or
`hdiutil`. A behavior that differs by host needs a seam or an
explicit case on that host.

Replace `runtime.GOOS` at the policy boundary and test both sides on
one machine. `hostDefaultType` and `linkDarwinHost` are the format
seams. Tests install `withLinuxLinkPolicy` for the Linux link rules,
and a Darwin-shaped seam when the case is the macOS refusal. Inject
the `hdiutil` runner the same way. Do not exec `hdiutil` from a unit
test. Real attach and convert stay in the `MACOS` end-to-end scripts.

Apply the more specific check before the host format policy. Split
copy requires a directory; a regular file fails that check before
macOS rejects `squashfs`. Both hosts then report the directory
requirement.

The reserved digest of sixty-four zeros means the content hash is
unknown. Mount and bake share one probe for it: size and mtime, no
content hash. A known digest still requires the full hash. A file
import records that marker only when `<stem>.yaml` or the `.sha256`
sidecar says so. When both files are missing, `--link` and `--copy`
warn and hash the payload; they do not invent the marker. Hashing
the marker itself is still a failure.

Compare paths only after the same canonicalization the product uses.
`canonicalPath` resolves symlinks. On macOS, `/var` and `/tmp` point
at `/private/var` and `/private/tmp`, and `t.TempDir()` is usually
the unresolved spelling. Linux `/tmp` usually is not a symlink, so an
equality check against `t.TempDir()` passes there and fails on macOS.

Do not assert a formatted MiB/s that comes from the wall clock. Assert
the byte numerator, or inject the elapsed time. A slower temporary
directory turns a non-zero rate into `0.0` and fails only that host.

A change to disk images, shadows, `hdiutil`, or link-and-bake still
needs the `MACOS` end-to-end scripts on Darwin. A change to FUSE
mounts, overlay uppers, squashfs, or erofs still needs the `LINUX`
scripts on Linux.

## Dependencies

The dependency whitelist is cobra, `gopkg.in/yaml.v3`, and the standard
library. `moby/sys/mountinfo` waits until mount-table parsing exists.
Prefer the standard library for everything else.

## Commits

Use Conventional Commits. Add a `Signed-off-by` trailer with
`git commit -s`. Wrap subject and body at 72 characters.

## Completion gates

On macOS, filesystem tests import `internal/testutil` to use ordinary
`fsync` for temporary fixtures instead of flushing hardware caches on
every metadata write. Production keeps `os.File.Sync` and its full
durability guarantee. Sync failures and injected file implementations
remain observable in tests. Set `TILER_TEST_FULL_SYNC=1` with `-count=1`
when explicitly checking the full hardware synchronization path.

A change is complete when these commands are green:

- `make fmt`
- `make lint`
- `make test`

Also run `make build` when `cmd/` changes. `make build` produces a
`CGO_ENABLED=0` static binary under `bin/` for the host OS. It does
not cross-compile.

Linux development also runs `make darwin-check`. macOS development
also runs `make linux-check`. Each target compiles tests for arm64
and amd64 and discards the binaries. `GOOS` and `GOARCH` are set
only on those command lines, so neither target rewrites `bin/tiler`.
They do not run the tests. Real hdiutil checks stay on macOS, and
mount checks stay on Linux.

## CI

The unit CI job runs on push and pull request. It checks out the tree,
installs Go 1.25, runs `go vet ./...`, runs golangci-lint, checks
formatting without rewriting files, and runs `go test ./... -race
-cover`. The pipeline does not reformat the tree.

The privileged integration-smoke job is added only after mount code
exists, on a self-hosted runner with the squashfs and FUSE toolchain.
Do not add that job before mount code exists.

## Shell end-to-end tests

Follow [test/AGENTS.md](test/AGENTS.md) for the Git test framework,
assertions, prerequisites, fixture isolation, cleanup, and e2e validation.
