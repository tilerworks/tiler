# contrib — optional helper tool sources

This directory vendors the sources of third-party helper tools that
tiler can use in specific scenarios. They are not part of the tiler
Go module: each tool keeps its own upstream license and build, and
the main module's builds, vet, lint, and tests skip this tree
(slowfs is a nested Go module with its own go.mod; concatfs is plain
C).

tiler never installs these tools automatically. When a scenario needs
one and it is missing, the command says so and points here, and
`tiler doctor` reports the absence as a warning with the build
guidance from the tool's own README.

| Tool | Role in tiler | Upstream | License | Build |
|------|---------------|----------|---------|-------|
| [concatfs](concatfs/README.md) | mounts a byte-block payload directory as one virtual concatenated image file, so a blocks-form image can be mounted without physical reassembly | https://github.com/schlaile/concatfs | GPL-2.0 | gcc + pkg-config fuse3 |
| [slowfs](slowfs/README.md) | mirrors a backing directory while injecting latency, used to model slow NAS storage in performance comparisons | https://github.com/google/slowfs | Apache-2.0 | go build |
