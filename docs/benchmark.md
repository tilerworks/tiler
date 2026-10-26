# Image pack benchmarks

`tiler benchmark <path>` measures one image size on a chosen volume
(external disk or NAS). The tables below keep the same sizes and
columns as before: 32, 128, 256, and 512 MiB, each split into 4 MiB
files, with pack time, output size, cold and warm reads, and (where
the format mounts) attach time. Repeat the procedure once per size.
Build with `make build` and call `bin/tiler`, or any `tiler` on
`PATH`.

## Procedure

Use a scratch directory as `<path>`. Put `--runtime-dir` next to it,
not inside it: the two paths must not contain each other. Each type
needs its own empty `--bench-dir`. The first type writes `data/`;
later types pack that same tree with `--src`. `--no-probe` skips the
4 KiB IOPS probe so the rows match these tables. `--format tsv`
prints only step rows (`image_create`, `image_attach` when the type
mounts, `read_image_1`, `read_image_2`). `--keep` leaves the bench
directory so the image and `data/` stay for the read passes.

Payload bytes are urandom. Pack MiB/s is input size over wall time.
Read MiB/s is bytes read over wall time. Attach time is its own row
and is not included in the read rate. `read_image_2` is a second pass
on a warm cache. tar reads stream member bytes and do not extract
onto `<path>`.

Cold image reads drop the cache inside `tiler benchmark` when
passwordless sudo works (`sudo -n purge` on macOS,
`drop_caches` on Linux). The `purge=` note says whether it worked.

The **expanded** columns are not a `tiler benchmark` step. After the
packs, and after the same passwordless purge, walk `data/` twice in
1 MiB reads, skipping names that start with `.`. The first walk is
"expanded, first"; the second is "expanded, warm".

### macOS (dmg and tar)

Needs `hdiutil`. For each `SIZE` in `32`, `128`, `256`, `512`:

```sh
SIZE=32
work=$(mktemp -d "${TMPDIR:-/tmp}/tiler-bench.XXXXXX")
runtime=$(mktemp -d "${TMPDIR:-/tmp}/tiler-bench-rt.XXXXXX")

bin/tiler benchmark "$work" \
  --bench-dir "$work/bench-dmg" \
  --runtime-dir "$runtime" \
  --type dmg --mode write \
  --size "${SIZE}M" --file-size 4M \
  --no-probe --keep --format tsv

bin/tiler benchmark "$work" \
  --bench-dir "$work/bench-tar" \
  --runtime-dir "$runtime" \
  --type tar --mode write \
  --src "$work/bench-dmg/data" \
  --no-probe --keep --format tsv

# expanded, first then warm (after sudo -n purge when it is allowed)
# walk "$work/bench-dmg/data" twice

bin/tiler benchmark "$work" \
  --runtime-dir "$runtime" \
  --type dmg --mode read \
  --image "$work/bench-dmg/image.dmg" \
  --no-probe --format tsv

bin/tiler benchmark "$work" \
  --runtime-dir "$runtime" \
  --type tar --mode read \
  --image "$work/bench-tar/image.tar.gz" \
  --no-probe --format tsv
```

`hdiutil create` uses `-format ULFO -fs APFS`. tar uses `tar -czf`.
Take `image_create` seconds, MiB/s, and the bytes column for the pack
table. Take `image_attach` seconds for the attach column. Take
`read_image_1` / `read_image_2` for the DMG and tar.gz read columns.

### Linux (squashfs, erofs, and tar)

Needs `mksquashfs`, `mkfs.erofs`, and — for a rootless mount —
`squashfuse_ll`, `erofsfuse`, and `fusermount3`. Same sizes and
`--file-size 4M`. Generate the tree with squashfs, then pack erofs
and tar from that `data/`:

```sh
SIZE=32
work=$(mktemp -d /tmp/tiler-bench.XXXXXX)
runtime=$(mktemp -d /tmp/tiler-bench-rt.XXXXXX)

bin/tiler benchmark "$work" \
  --bench-dir "$work/bench-squashfs" \
  --runtime-dir "$runtime" \
  --type squashfs --mode write \
  --size "${SIZE}M" --file-size 4M \
  --no-probe --keep --format tsv

bin/tiler benchmark "$work" \
  --bench-dir "$work/bench-erofs" \
  --runtime-dir "$runtime" \
  --type erofs --mode write \
  --src "$work/bench-squashfs/data" \
  --no-probe --keep --format tsv

bin/tiler benchmark "$work" \
  --bench-dir "$work/bench-tar" \
  --runtime-dir "$runtime" \
  --type tar --mode write \
  --src "$work/bench-squashfs/data" \
  --no-probe --keep --format tsv

# walk "$work/bench-squashfs/data" twice for the expanded columns
# (after drop_caches when passwordless sudo allows it)

bin/tiler benchmark "$work" \
  --runtime-dir "$runtime" \
  --type squashfs --mode read \
  --image "$work/bench-squashfs/image.sqsh" \
  --no-probe --format tsv

bin/tiler benchmark "$work" \
  --runtime-dir "$runtime" \
  --type erofs --mode read \
  --image "$work/bench-erofs/image.erofs" \
  --no-probe --format tsv

bin/tiler benchmark "$work" \
  --runtime-dir "$runtime" \
  --type tar --mode read \
  --image "$work/bench-tar/image.tar.gz" \
  --no-probe --format tsv
```

squashfs packs with `mksquashfs -comp zstd -noappend`. erofs uses
`mkfs.erofs` with a nil UUID and the best compressor that
`mkfs.erofs` lists (zstd, then lz4, then gzip). Ubuntu 24.04's
erofs-utils has no zstd, so the table below packed with lz4. Reads
attach read-only with `squashfuse_ll` / `erofsfuse` and walk the
mount twice.

---

## Linux: squashfs and erofs vs tar+gzip

### Result

Host: Lima VM (Linux on macOS, Apple Virtualization framework) on an
Apple silicon host, aarch64, 6 vCPU, 11 GiB RAM, Ubuntu 24.04.5 LTS,
kernel 6.8.0-147-generic, ext4 scratch on virtio. squashfs-tools
4.6.1, squashfuse 0.5.0, erofs-utils 1.7.1, GNU tar 1.35, gzip 1.12.
Date: 2026-10-22. `purge=yes` for every size.

#### Pack

| MiB | mksquashfs zstd | mkfs.erofs lz4 | tar+gzip | sqsh out | erofs out | tar.gz out |
|-----|-----------------|----------------|----------|----------|-----------|------------|
| 32 | 0.247 s (129.7 MiB/s) | 0.284 s (112.8 MiB/s) | 0.665 s (48.1 MiB/s) | 33558528 | 33558528 | 33560812 |
| 128 | 1.014 s (126.3 MiB/s) | 1.114 s (114.9 MiB/s) | 2.679 s (47.8 MiB/s) | 134221824 | 134221824 | 134244190 |
| 256 | 1.882 s (136.1 MiB/s) | 2.260 s (113.3 MiB/s) | 5.266 s (48.6 MiB/s) | 268439552 | 268443648 | 268487106 |
| 512 | 3.943 s (129.8 MiB/s) | 4.764 s (107.5 MiB/s) | 10.621 s (48.2 MiB/s) | 536875008 | 536883200 | 536974754 |

Both image packers thread their compressors and stay 2.2–2.8x ahead of
single-threaded gzip at every size; mksquashfs zstd leads mkfs.erofs
lz4 by roughly 10–20%. All three outputs stay near the input size
because the payload is incompressible.

#### Read

| MiB | expanded, first | squashfs, first | erofs, first | tar.gz, first | expanded, warm | squashfs, warm | erofs, warm | tar.gz, warm |
|-----|-----------------|-----------------|--------------|---------------|----------------|----------------|-------------|--------------|
| 32 | 0.012 s (2666.7 MiB/s) | 0.024 s (1309.6 MiB/s) | 0.019 s (1670.0 MiB/s) | 0.028 s (1125.3 MiB/s) | 0.009 s | 0.010 s | 0.018 s | 0.027 s |
| 128 | 0.048 s (2666.7 MiB/s) | 0.107 s (1200.7 MiB/s) | 0.090 s (1419.7 MiB/s) | 0.110 s (1166.4 MiB/s) | 0.036 s | 0.042 s | 0.084 s | 0.098 s |
| 256 | 0.105 s (2438.1 MiB/s) | 0.198 s (1295.3 MiB/s) | 0.173 s (1475.6 MiB/s) | 0.236 s (1084.0 MiB/s) | 0.067 s | 0.082 s | 0.140 s | 0.204 s |
| 512 | 0.267 s (1917.6 MiB/s) | 0.423 s (1210.8 MiB/s) | 0.366 s (1398.3 MiB/s) | 0.476 s (1076.3 MiB/s) | 0.153 s | 0.157 s | 0.266 s | 0.432 s |

The cold read through either FUSE mount lands at 1200–1670 MiB/s,
roughly half to two thirds of the cold walk of the expanded tree and
still ahead of the tar.gz stream, which inflates every byte on one
thread. Warm squashfs nearly matches the expanded tree at
3020–3270 MiB/s: after the first pass the kernel page cache serves the
mount, so zstd decompression happens once. Warm erofsfuse shows no
comparable lift and stays at 1529–1922 MiB/s, and warm tar.gz stays at
1177–1303 MiB/s; both redo their decompression on every pass. Unlike
the ~3 s dmg attach on macOS, both FUSE attaches finish in
single-digit milliseconds.

---

## macOS: dmg vs tar+gzip

Follow the macOS procedure above for 32, 128, 256, and 512 MiB.

### Result

Host: Mac15,6, Apple M3 Pro, 36 GiB RAM, macOS 27.0.1, arm64.
Date: 2026-10-26. `purge=yes` for every size. Measured with
`tiler benchmark` using the procedure above.

#### Pack

| MiB | hdiutil create | tar+gzip | hdiutil out | tar.gz out |
|-----|----------------|----------|-------------|------------|
| 32 | 7.888 s (4.1 MiB/s) | 0.654 s (48.9 MiB/s) | 34264095 | 33568140 |
| 128 | 6.835 s (18.7 MiB/s) | 2.504 s (51.1 MiB/s) | 137604398 | 134272096 |
| 256 | 8.056 s (31.8 MiB/s) | 4.995 s (51.3 MiB/s) | 273171290 | 268544069 |
| 512 | 10.019 s (51.1 MiB/s) | 9.967 s (51.4 MiB/s) | 544298150 | 537088012 |

`hdiutil create` spends several seconds before the payload size
matters, so it is slower than tar on the smaller trees. At 512 MiB
that fixed cost is amortized and ULFO is roughly even with gzip
(~51 MiB/s). Both outputs stay near the input size because the
payload is incompressible.

#### Read

| MiB | expanded, first | DMG, first | tar.gz, first | expanded, warm | DMG, warm | tar.gz, warm | attach |
|-----|-----------------|------------|---------------|----------------|-----------|--------------|--------|
| 32 | 0.018 s (1777.8 MiB/s) | 0.094 s (341.6 MiB/s) | 0.033 s (965.5 MiB/s) | 0.003 s | 0.004 s | 0.014 s | 3.448 s |
| 128 | 0.055 s (2327.3 MiB/s) | 0.347 s (369.4 MiB/s) | 0.072 s (1768.9 MiB/s) | 0.012 s | 0.013 s | 0.050 s | 2.842 s |
| 256 | 0.112 s (2285.7 MiB/s) | 0.664 s (385.3 MiB/s) | 0.127 s (2010.8 MiB/s) | 0.021 s | 0.027 s | 0.093 s | 3.609 s |
| 512 | 0.249 s (2056.2 MiB/s) | 1.351 s (379.0 MiB/s) | 0.232 s (2206.4 MiB/s) | 0.046 s | 0.055 s | 0.182 s | 2.643 s |

The first read through the mounted dmg stays near 340–385 MiB/s,
about one sixth to one seventh of the cold read of the expanded
directory. Streaming the tar.gz after the same purge is slower than
the expanded tree at 32 MiB and roughly matches it at 512 MiB,
because it is one sequential inflate rather than a walk of many
files. The second pass of the expanded tree and the mounted dmg is
limited by the page cache and lands at the same rate. The warm
tar.gz pass stays slower: the archive is cached, but gzip still
inflates every byte. Attach itself is about 2.6–3.6 seconds and does
not scale cleanly with size.

---

## External disk and NAS with `tiler benchmark`

Use `tiler benchmark <path>` when the question is how create and read
operations behave on a specific volume — an external USB disk, a
Thunderbolt volume, or a NAS share mounted under `/Volumes` (macOS) or
`/mnt` (Linux). The command does not read `.tiler.yaml` or accept
root persistent flags such as `--images-dir`.

### Safety

- Writable runs create files only under `--bench-dir`. The default is
  a fresh `<path>/tiler-bench-<random>/`. They do not delete or rename
  anything outside that directory. Pass an explicit empty directory
  under `<path>` when you want a fixed location; an existing non-empty
  `--bench-dir` is refused.
- Mount and logs live under `--runtime-dir` on the **local** machine
  (default: a private directory in the system temp area, removed at
  the end). Attach never creates a directory on the NAS unless you
  point `--runtime-dir` there. A user-supplied `--runtime-dir` is
  kept; only the command's `mount/` and `logs/` children are removed.
- The device probe writes `.tiler-bench-probe` under `--bench-dir` and
  removes it. Skip it with `--no-probe`.
- If `<path>` is **not writable** (read-only mount), only this form
  is accepted:

  ```sh
  tiler benchmark /Volumes/ro-share \
    --readonly \
    --mode read \
    --image /Volumes/ro-share/existing/image.dmg \
    --no-probe
  ```

  That run attaches the image read-only, walks it, and writes
  **nothing** under `<path>` (mount and logs stay under
  `--runtime-dir`). Pass `--mode read`: the default mode is `both`,
  and `--readonly` with `write` or `both` is refused. `--bench-dir`
  and a probe are also refused. Prefer omitting `--runtime-dir` (or
  pass a local directory); `--runtime-dir` and `<path>` must not
  contain each other, so a runtime directory on the read-only volume
  under test is refused.
- On a writable volume, `--mode read --image FILE` also requires
  `--no-probe` so the read benchmark cannot leave a probe file behind.

### Writable share examples

```sh
# macOS: NAS share already mounted under /Volumes.
# Default --bench-dir is <path>/tiler-bench-<random>/; omit --runtime-dir
# for a private local temp parent (mount/ and logs/ underneath).
tiler benchmark /Volumes/share/bench \
  --type dmg \
  --mode both \
  --size 512M \
  --file-size 4M \
  --probe-ops 2000

# Create the image on the share, then re-read it. Keep a fixed
# --bench-dir so the dmg path is known. Attach mounts onto the local
# disk via --runtime-dir; the read walk uses that local mount, not a
# directory on the share.
tiler benchmark /Volumes/share/bench \
  --bench-dir /Volumes/share/bench/tiler-bench-run \
  --mode write \
  --type dmg \
  --size 256M \
  --file-size 4M \
  --keep
mkdir -p /tmp/tiler-bench-runtime
tiler benchmark /Volumes/share/bench \
  --runtime-dir /tmp/tiler-bench-runtime \
  --type dmg \
  --mode read \
  --image /Volumes/share/bench/tiler-bench-run/image.dmg \
  --no-probe
```

`--mode write` times only image create. `--mode read` without
`--image` still creates an image first (setup) and needs a writable
`<path>`. `--mode both` times create and both read passes. `--type tar`
packs with `tar -czf` and reads by streaming member bytes; it does not
mount. `--src DIR` packs an existing directory instead of generating a
new one. `--size` and `--file-size` use the same byte-size syntax as the
rest of tiler (`128M`, `4M`, bare integers are bytes). For dmg,
`--compress` is `ULFO` (default), `ULMO`, or `UDRO`. For squashfs it
is `zstd` (default), `gzip`, `lz4`, or `xz`; for erofs it is `zstd`,
`lz4`, or `gzip`, defaulting to the best compressor the host's
`mkfs.erofs` lists. tar rejects `--compress`. `--probe-ops` defaults
to 2000. `--keep` leaves `--bench-dir` in place. `--format` selects
the report shape: `table` (default) is the human layout, `tsv`
prints only step rows for scripts that build a larger table, and
`json` writes one object with metadata, probe, and steps after a
successful run. Payload bytes are random and incompressible.

On Linux the same `<path>` pattern works with `--type squashfs` and
`--type erofs`:

```sh
# squashfs on a NAS share, create and both read passes
tiler benchmark /mnt/share/bench \
  --type squashfs \
  --mode both \
  --size 512M \
  --file-size 4M

# read an existing erofs image back without writing under <path>
tiler benchmark /mnt/share/bench \
  --readonly \
  --mode read \
  --type erofs \
  --image /mnt/share/bench/existing/image.erofs \
  --no-probe
```

Cold passes try a passwordless cache drop first (`sudo -n purge` on
macOS, `drop_caches` on Linux); the `purge=` note says whether it
worked.
