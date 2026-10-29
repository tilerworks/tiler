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

## Runtime and product surface

Builds use Go 1.25 or newer with `CGO_ENABLED=0` static binaries.
Call git and FUSE tools only through `exec`. Do not add go-git, and do
not add CGO FUSE bindings. Keep Linux-only code in one
`//go:build linux` package so the rest of the module can be tested on
any platform. Integration tests that need root or mounts call `t.Skip`
when that permission is missing.

The default runtime layout is under the user home directory:
`$HOME/.tiler/images`, `$HOME/.tiler/sessions`, and
`$HOME/.tiler/cache`. Session records and combination trees share
`sessions_dir` (`sessions/<name>/` with `runtime/m<seq>/` under it).
tiler does not gain a registry, a pull command, or certificate
signing. Integrity checks, when added later, use sha256 only.

## Dependencies

The dependency whitelist is cobra, `gopkg.in/yaml.v3`, and the standard
library. `moby/sys/mountinfo` waits until mount-table parsing exists.
Prefer the standard library for everything else.

## Commits

Use Conventional Commits. Add a `Signed-off-by` trailer with
`git commit -s`. Wrap subject and body at 72 characters.

## Completion gates

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
