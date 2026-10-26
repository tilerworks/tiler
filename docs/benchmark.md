# Image pack benchmarks

`tiler benchmark <path>` measures one image size on a chosen volume
(external disk or NAS). The tables below keep the same sizes and
columns as before: 32, 128, 256, and 512 MiB, each split into 4 MiB
files, with pack time, output size, cold and warm reads, and (where
the format mounts) attach time. Repeat the procedure once per size.
Build with `make build` and call `bin/tiler`, or any `tiler` on
`PATH`.

## Procedure

Use a scratch directory as `<path>`. Each type runs a single
command: `--mode both` generates the payload, packs the image,
attaches it (when the type mounts), and measures both read passes in
one run. `--bench-dir run-<type>` is a relative name that tiler
joins under `<path>`; the joined directory must not exist or must be
empty, and it is removed when the run ends. `--no-probe` skips the
4 KiB IOPS probe so the rows match these tables. `--format tsv`
prints only step rows (`image_create`, `image_attach` when the type
mounts, `read_image_1`, `read_image_2`).

Each run generates its own payload under `--src` (default `data`)
inside the runtime directory: a private local temp dir by default,
or the local directory passed to `--runtime-dir`. The measured
volume only ever holds the bench dir with the image inside it, so
pack reads the payload from local disk while writing the image to
`<path>`. Payload bytes are urandom and incompressible, so pack
rates stay comparable across types without sharing a tree. Pack
MiB/s is input size over wall time. Read MiB/s is bytes read over
wall time. Attach time is its own row and is not included in the
read rate. `read_image_2` is a second pass on a warm cache. tar
reads stream member bytes and do not extract onto `<path>`.

A later run can re-read an image that an earlier run kept with
`--keep`: rerun one of the tar commands below with `--keep` added,
then read the image it left behind. The read needs `--no-probe`, and
an explicit `--bench-dir` or `--src` is refused because it creates
nothing:

```sh
# after a --keep run left /tmp/tiler-scratch/run-tar/image.tar.gz
bin/tiler benchmark /tmp/tiler-scratch \
  --type tar --mode read \
  --image /tmp/tiler-scratch/run-tar/image.tar.gz \
  --no-probe --format tsv
```

Cold image reads drop the cache inside `tiler benchmark` when
passwordless sudo works (`sudo -n purge` on macOS,
`drop_caches` on Linux). The `purge=` note says whether it worked.

The **expanded** columns are not a `tiler benchmark` step. Rerun
with `--keep` and an explicit local `--runtime-dir`, then — after
the same passwordless purge — walk the kept payload tree
(`<runtime-dir>/data`) twice in 1 MiB reads, skipping names that
start with `.`. The first walk is "expanded, first"; the second is
"expanded, warm". That tree lives on the local disk, so these
columns measure local reads. The tables below were produced with the
older procedure, where the payload tree was generated under the
bench directory on the measured volume and later types packed the
first type's shared `data/`.

### macOS (dmg and tar)

Needs `hdiutil`. For each `SIZE` in `32`, `128`, `256`, `512`:

```sh
SIZE=32
mkdir -p /tmp/tiler-scratch

bin/tiler benchmark /tmp/tiler-scratch \
  --type dmg --mode both \
  --bench-dir run-dmg \
  --size "${SIZE}M" --file-size 4M \
  --no-probe --format tsv

bin/tiler benchmark /tmp/tiler-scratch \
  --type tar --mode both \
  --bench-dir run-tar \
  --size "${SIZE}M" --file-size 4M \
  --no-probe --format tsv
```

Each run writes only its image under `/tmp/tiler-scratch` and
removes `run-dmg` / `run-tar` plus its private runtime dir at the
end. For the expanded columns, add `--keep --runtime-dir <dir>` (an
absent or empty local dir) and walk `<dir>/data` twice after the
purge (`sudo -n purge` when it is allowed).

`hdiutil create` uses `-format ULFO -fs APFS`. tar uses `tar -czf`.
Take `image_create` seconds, MiB/s, and the bytes column for the pack
table. Take `image_attach` seconds for the attach column. Take
`read_image_1` / `read_image_2` for the DMG and tar.gz read columns.

### Linux (squashfs, erofs, and tar)

Needs `mksquashfs`, `mkfs.erofs`, and — for a rootless mount —
`squashfuse_ll`, `erofsfuse`, and `fusermount3`. Same sizes and
`--file-size 4M`. Every type packs its own freshly generated
payload:

```sh
SIZE=32
mkdir -p /tmp/tiler-scratch

bin/tiler benchmark /tmp/tiler-scratch \
  --type squashfs --mode both \
  --bench-dir run-squashfs \
  --size "${SIZE}M" --file-size 4M \
  --no-probe --format tsv

bin/tiler benchmark /tmp/tiler-scratch \
  --type erofs --mode both \
  --bench-dir run-erofs \
  --size "${SIZE}M" --file-size 4M \
  --no-probe --format tsv

bin/tiler benchmark /tmp/tiler-scratch \
  --type tar --mode both \
  --bench-dir run-tar \
  --size "${SIZE}M" --file-size 4M \
  --no-probe --format tsv
```

For the expanded columns, add `--keep --runtime-dir <dir>` to one
run and walk `<dir>/data` twice (after `drop_caches` when
passwordless sudo allows it).

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
Date: 2026-10-26. `purge=yes` for every read. The procedure ran twice
and every row is the mean of the two run times, with its rate derived
from that mean so a row stays internally consistent. tar+gzip is the
baseline; the multiples below are against it.

#### Pack

| MiB | mksquashfs zstd | mkfs.erofs lz4 | tar+gzip | sqsh out | erofs out | tar.gz out |
|-----|-----------------|----------------|----------|----------|-----------|------------|
| 32 | 0.305 s (104.9 MiB/s) | 0.312 s (102.6 MiB/s) | 0.680 s (47.0 MiB/s) | 33558528 | 33558528 | 33560803 |
| 128 | 1.068 s (119.9 MiB/s) | 1.168 s (109.5 MiB/s) | 2.789 s (45.9 MiB/s) | 134221824 | 134221824 | 134243430 |
| 256 | 1.981 s (129.2 MiB/s) | 2.308 s (110.9 MiB/s) | 5.280 s (48.5 MiB/s) | 268439552 | 268443648 | 268487100 |
| 512 | 3.780 s (135.4 MiB/s) | 4.565 s (112.2 MiB/s) | 11.810 s (43.4 MiB/s) | 536875008 | 536883200 | 536974194 |

Both image packers thread their compressors and beat single-threaded
gzip at every size: squashfs by 2.2–3.1x and erofs by 2.2–2.6x, with
the advantage widening as the tree grows. Measured as wall time,
squashfs zstd needs 2% less than erofs lz4 at 32 MiB and 17% less at
512 MiB. All three outputs stay within 0.01% of the input because the
payload is incompressible; the `tar.gz out` column is the run-1 value
and drifts by a few hundred bytes between runs, while both image
outputs are byte-identical across the two runs.

This host's `mkfs.erofs` lists lz4, lz4hc, lzma, deflate, and
libdeflate but no zstd, so the packer selected lz4 rather than the
zstd default. A zstd-capable erofs-utils would move the erofs columns.

#### Read

| MiB | expanded, first | squashfs, first | erofs, first | tar.gz, first | expanded, warm | squashfs, warm | erofs, warm | tar.gz, warm |
|-----|-----------------|-----------------|--------------|---------------|----------------|----------------|-------------|--------------|
| 32 | 0.025 s (1280.0 MiB/s) | 0.029 s (1103.4 MiB/s) | 0.022 s (1454.5 MiB/s) | 0.017 s (1882.4 MiB/s) | 0.012 s | 0.008 s | 0.018 s | 0.015 s |
| 128 | 0.060 s (2115.7 MiB/s) | 0.139 s (920.9 MiB/s) | 0.186 s (688.2 MiB/s) | 0.072 s (1777.8 MiB/s) | 0.037 s | 0.038 s | 0.081 s | 0.060 s |
| 256 | 0.111 s (2316.7 MiB/s) | 0.179 s (1426.2 MiB/s) | 0.155 s (1646.3 MiB/s) | 0.140 s (1835.1 MiB/s) | 0.067 s | 0.059 s | 0.121 s | 0.115 s |
| 512 | 0.311 s (1646.3 MiB/s) | 0.406 s (1261.1 MiB/s) | 0.320 s (1602.5 MiB/s) | 0.292 s (1750.4 MiB/s) | 0.138 s | 0.116 s | 0.242 s | 0.237 s |

Warm rates, derived from the warm seconds: expanded 2667–3821 MiB/s,
squashfs 3413–4395 MiB/s, erofs 1580–2116 MiB/s, tar.gz 2116–2226
MiB/s. Both mounts attach read-only in 7–13 ms, so attach is not a
factor here; tar never mounts, it streams member bytes.

Cold, tar.gz is the fastest of the four at every size, and both FUSE
mounts trail it: squashfs at 0.52–0.78x of tar and erofs at
0.39–0.92x. That ranking is a property of this payload, not of the
formats. Payload bytes are urandom, so gzip cannot compress them and
emits stored blocks — `image.tar.gz` is larger than the tree it packs.
Streaming it is a sequential read of one file plus a block copy, with
no inflate arithmetic, no FUSE round trip, and no per-file open. On a
compressible tree the single-threaded inflate would dominate instead
and tar would fall behind both mounts.

Warm reverses the ranking for squashfs, which reaches 1.61–2.04x of
tar and 0.99–1.50x of the expanded warm tree, so it is the fastest
read path in this run at three of four sizes. After the first pass the
page cache holds the decompressed blocks, so zstd runs once. erofs
shows no comparable lift, staying at 0.75–0.98x of tar, and tar itself
stays near 2150 MiB/s, because erofsfuse re-decompresses lz4 and gzip
re-reads its stored blocks on every pass. The expanded warm tree lands
at 1.25–1.73x of tar.

#### Variance and disagreement with the earlier run

Read rows at 32 MiB are tens of milliseconds long, so their relative
spread is the largest. The outlier is erofs cold at 128 MiB, where the
two runs took 0.117 s and 0.255 s — a 2.18x spread that leaves its
688.2 MiB/s mean, and the 0.39x it implies against tar, weakly
supported. Every other read row agreed within 1.31x, and pack times
within 1.16x (the widest was squashfs at 256 MiB). Read the 128 MiB
erofs cold cell as indicative rather than precise.

The earlier run below records tar.gz cold at 1076–1166 MiB/s, behind
both mounts, which is the opposite ordering from this one at
1750–1882 MiB/s. Comparing the two row by row does not isolate a
single cause. tar is the only cold row that rose consistently at every
size, by +53% to +69% cold and +62% to +82% warm. The expanded first
pass fell consistently, by −5% to −52%, and the expanded warm pass
scattered from −25% to +11%. squashfs warm rose +12% to +38% and erofs
warm +3% to +15%, while the two cold mount rows scatter in both
directions, −52% to +14%. The driver also changed between the runs,
from a Python `tarfile` reader in the now-deleted `bench-img-path.py`
to Go `archive/tar` in `internal/benchmark`. Re-running both readers
back to back on one 512 MiB archive gave 1467 MiB/s for Python and
1670 MiB/s for Go, so that change is worth about 14%, and Python still
measures well above the 1076 MiB/s the earlier table records for that
same reader. The driver change is therefore not the main cause, and
the remainder is unexplained; the Lima VM shares its Apple host, so
host-side load during the earlier run is a plausible contributor.
Treat the earlier table as stale rather than contradicted.

### Earlier run (2026-10-22)

Kept for reference. Measured on the same host class with the
`bench-img-vs-tar.sh` and `bench-img-path.py` drivers, both since
removed in favour of `tiler benchmark`. Its tar rows disagree with the
2026-10-26 run above; see the variance note.

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

- Writable runs create files only inside `<path>/<bench-dir>`.
  `--bench-dir` is a relative name that tiler joins under `<path>`
  (default `tiler-bench`); absolute paths, empty values, names
  starting with `./` or `../`, and values containing a `..` element
  are refused. The joined directory must not exist or must be empty,
  and the whole directory is removed when the run ends; `--keep`
  preserves it. `<path>` itself must already exist, is never removed,
  and nothing else under it is deleted or renamed.
- The runtime dir holds `mount/`, `logs/`, and the payload tree
  (`--src`, default `data`) on the **local** machine — by default a
  private directory in the system temp area. A `--runtime-dir` you
  pass must not exist or must be an empty directory, and it is
  removed entirely at the end (`--keep` preserves it together with
  the bench dir). `--runtime-dir` and `<path>` must not contain each
  other, so attach never creates a directory on the measured volume.
- The device probe writes `.tiler-bench-probe` inside the bench dir
  and removes it after sampling. Skip it with `--no-probe`.
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
  **nothing** under `<path>` (mount and logs stay under the local
  runtime dir). Pass `--mode read`: the default mode is `both`,
  and `--readonly` with `write` or `both` is refused. An explicit
  `--bench-dir` or `--src` is refused too — the defaults are
  silently ignored — and so is a probe. Prefer omitting
  `--runtime-dir` (or pass a local directory); `--runtime-dir` and
  `<path>` must not contain each other, so a runtime directory on
  the read-only volume under test is refused.
- On a writable volume, `--mode read --image FILE` also requires
  `--no-probe` so the read benchmark cannot leave a probe file
  behind, and it refuses an explicit `--bench-dir` or `--src`
  because it creates no image.

### Writable share examples

```sh
# macOS: NAS share already mounted under /Volumes.
# The default --bench-dir is the relative name tiler-bench, joined
# under <path>; omit --runtime-dir for a private local temp dir
# (mount/, logs/, and the payload tree underneath). Both directories
# are removed when the run ends.
tiler benchmark /Volumes/share/bench \
  --type dmg \
  --mode both \
  --size 512M \
  --file-size 4M \
  --probe-ops 2000

# Create the image on the share and keep it, then re-read it in a
# later run. --bench-dir is relative, so the image lands at
# <path>/run-dmg/image.dmg. Attach mounts onto the local disk via
# the runtime dir; the read walk uses that local mount, not a
# directory on the share.
tiler benchmark /Volumes/share/bench \
  --type dmg \
  --mode write \
  --bench-dir run-dmg \
  --size 256M \
  --file-size 4M \
  --keep

tiler benchmark /Volumes/share/bench \
  --type dmg \
  --mode read \
  --image /Volumes/share/bench/run-dmg/image.dmg \
  --no-probe
```

`--mode write` times only image create. `--mode read` without
`--image` still creates an image first (setup) and needs a writable
`<path>`. `--mode both` times create and both read passes. `--type tar`
packs with `tar -czf` and reads by streaming member bytes; it does not
mount. `--src DIR` names the payload directory: a relative path,
joined under the runtime dir (default `data`), where tiler generates
the random tree it packs; it is only valid when creating an image.
`--size` and `--file-size` use the same byte-size syntax as the
rest of tiler (`128M`, `4M`, bare integers are bytes). For dmg,
`--compress` is `ULFO` (default), `ULMO`, or `UDRO`. For squashfs it
is `zstd` (default), `gzip`, `lz4`, or `xz`; for erofs it is `zstd`,
`lz4`, or `gzip`, defaulting to the best compressor the host's
`mkfs.erofs` lists. tar rejects `--compress`. `--probe-ops` defaults
to 2000. `--bench-dir` and `--runtime-dir` must not exist or must be
empty, and both are removed when the run ends; `--keep` preserves
both directories, the payload tree included. Images land at
`<path>/<bench-dir>/image.dmg`, `image.sqsh`, `image.erofs`, or
`image.tar.gz`. `--format` selects the report shape: `table`
(default) is the human layout, `tsv` prints only step rows for
scripts that build a larger table, and `json` writes one object with
metadata, probe, and steps after a successful run. Payload bytes are
random and incompressible.

On Linux the same `<path>` pattern works with `--type squashfs` and
`--type erofs`:

```sh
# squashfs on a NAS share, create and both read passes; the image
# is written to /mnt/share/bench/run-squashfs/ and the bench dir is
# removed at the end
tiler benchmark /mnt/share/bench \
  --type squashfs \
  --mode both \
  --bench-dir run-squashfs \
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
