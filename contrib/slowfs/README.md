# slowfs — latency-injecting FUSE mirror

Upstream: https://github.com/google/slowfs
License: Apache-2.0 (see LICENSE)

slowfs mirrors a backing directory through FUSE and injects
configured latency: seek time for non-sequential access inside a
seek window, byte-rate caps for sequential throughput, and a per
operation metadata time. tiler uses it as a test aid to model a
high-latency NAS holding image files, so mount and read behavior can
be compared against a local-disk baseline (for example beside the
doctor storage performance probe).

## Upstream state and local adaptation

The upstream tree predates Go modules; it is vendored unchanged
except for two locally added module files: `go.mod` (declares module
`slowfs`, matching the in-tree import paths, and pins
github.com/hanwen/go-fuse v1.0.0) and `go.sum`. Being a nested
module, the main tiler module's `./...` builds, vet, lint, and tests
skip this tree.

## Build

Needs a Go toolchain:

    go build -o slowfs-bin .

Put the result anywhere on PATH when a latency-injection scenario is
wanted. tiler never builds, installs, or runs it automatically;
`tiler doctor` reports a missing slowfs as a warning only.

## Usage sketch

    slowfs-bin --backing-dir=<dir holding image.sqsh> \
        --mount-dir=<view> --seek-time=8ms --seek-window=4KiB \
        --metadata-op-time=0

squashfuse_ll then mounts `<view>/image.sqsh`, so every squashfs
read crosses the latency model.
