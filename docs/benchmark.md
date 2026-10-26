# Image pack benchmarks

`tiler benchmark <path>` measures one image size on a chosen volume
(external disk or NAS). The tables below use these sizes and
columns: 32, 128, 256, and 512 MiB, each split into 4 MiB files,
with pack time, output size, cold and warm reads, and (where the
format mounts) attach time. Repeat the procedure once per size.
Build with `make build` and call `bin/tiler`, or any `tiler` on
`PATH`.

## Procedure

`<path>` is the path to evaluate. It is typically the mount point of
an external USB or Thunderbolt disk, or of a NAS share already
mounted under `/Volumes` (macOS) or `/mnt` (Linux). It must already
exist and be a directory; tiler neither creates nor removes it. A run
measures the volume that holds `<path>`, not the directory or its
depth, so a scratch directory measures only the local disk. Point
`<path>` at the disk or share you care about; the format tables below
instead used the host's local ext4 volume, because those rows compare
formats rather than volumes.

Each type runs a single command: `--mode both` generates the payload,
packs the image, attaches it for the types that mount (`dmg`,
`squashfs`, `erofs`), and measures both read passes in one run; `tar`
is streamed instead of mounted. `--bench-dir run-<type>` is a
relative name that tiler joins under `<path>`; the joined directory
must not exist or must be empty, and it is removed when the run ends.
`--no-probe` skips the 4 KiB IOPS probe so the rows match these
tables; that probe and its results are in the [Random 4 KiB IOPS
probe](#random-4-kib-iops-probe) section. `--format tsv` prints only
step rows (`image_create`, `image_attach` for a mounted type,
`read_image_1`, `read_image_2`).

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
# after a --keep run left /mnt/bench/run-tar/image.tar.gz
bin/tiler benchmark /mnt/bench \
  --type tar --mode read \
  --image /mnt/bench/run-tar/image.tar.gz \
  --no-probe --format tsv
```

On macOS the same paths start under `/Volumes/<volume>`.

Cold image reads drop the cache inside `tiler benchmark` when
passwordless sudo works (`sudo -n purge` on macOS,
`drop_caches` on Linux). The `purge=` note says whether it worked.

The **expanded** columns are not a `tiler benchmark` step. Rerun
with `--keep` and an explicit local `--runtime-dir`, then — after
the same passwordless purge — walk the kept payload tree
(`<runtime-dir>/data`) twice in 1 MiB reads, skipping names that
start with `.`. The first walk is "expanded, first"; the second is
"expanded, warm". That tree lives on the local disk, so these
columns measure local reads.

### macOS (dmg and tar)

Needs `hdiutil`. For each `SIZE` in `32`, `128`, `256`, `512` MiB,
with `voldir` set to the mount point of the volume under test:

```sh
SIZE=32
voldir=/Volumes/bench

bin/tiler benchmark "$voldir" \
  --type dmg --mode both \
  --bench-dir run-dmg \
  --size "${SIZE}M" --file-size 4M \
  --no-probe --format tsv

bin/tiler benchmark "$voldir" \
  --type tar --mode both \
  --bench-dir run-tar \
  --size "${SIZE}M" --file-size 4M \
  --no-probe --format tsv
```

Each run writes only its image under `<path>` and removes
`run-dmg` / `run-tar` plus its private runtime dir at the end. For
the expanded columns, add `--keep --runtime-dir <dir>` (an absent or
empty local dir) and walk `<dir>/data` twice after the purge
(`sudo -n purge` when it is allowed).

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
voldir=/mnt/bench

bin/tiler benchmark "$voldir" \
  --type squashfs --mode both \
  --bench-dir run-squashfs \
  --size "${SIZE}M" --file-size 4M \
  --no-probe --format tsv

bin/tiler benchmark "$voldir" \
  --type erofs --mode both \
  --bench-dir run-erofs \
  --size "${SIZE}M" --file-size 4M \
  --no-probe --format tsv

bin/tiler benchmark "$voldir" \
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
kernel 6.8.0-147-generic, ext4 scratch volume as `<path>` on virtio.
squashfs-tools 4.6.1, squashfuse 0.5.0, erofs-utils 1.7.1, GNU tar
1.35, gzip 1.12.
Date: 2026-10-26. `purge=yes` for every read. Pack and image read rows
are the mean of two runs, with each rate derived from that mean so a
row stays internally consistent; the expanded rows come from one walk
of a kept payload tree after the same purge, then one warm repeat.
tar+gzip is the baseline; the multiples below are against it.

#### Pack

| MiB | mksquashfs zstd | mkfs.erofs lz4 | tar+gzip | sqsh out | erofs out | tar.gz out |
|-----|-----------------|----------------|----------|----------|-----------|------------|
| 32 | 0.368 s (87.0 MiB/s) | 0.318 s (100.6 MiB/s) | 0.689 s (46.5 MiB/s) | 33558528 | 33558528 | 33560799 |
| 128 | 1.345 s (95.1 MiB/s) | 1.358 s (94.2 MiB/s) | 2.764 s (46.3 MiB/s) | 134221824 | 134221824 | 134243705 |
| 256 | 2.127 s (120.4 MiB/s) | 2.428 s (105.4 MiB/s) | 5.498 s (46.6 MiB/s) | 268439552 | 268443648 | 268487553 |
| 512 | 5.069 s (101.0 MiB/s) | 4.928 s (103.9 MiB/s) | 11.056 s (46.3 MiB/s) | 536875008 | 536883200 | 536975761 |

Both image packers thread their compressors and beat single-threaded
gzip at every size: squashfs by 1.9–2.6x and erofs by 2.0–2.3x, with
no clear trend as the tree grows. Measured as wall time, the two are
within 3% of each other at 128 and 512 MiB, erofs needs 14% less at
32 MiB, and squashfs needs 12% less at 256 MiB. All three outputs stay
within 0.02% of the input because the payload is incompressible; the
`tar.gz out` column is the run-1 value and drifts by up to a few
hundred bytes between runs, while both image outputs are byte-identical
across the two runs.

This host's `mkfs.erofs` lists lz4, lz4hc, lzma, deflate, and
libdeflate but no zstd, so the packer selected lz4 rather than the
zstd default. A zstd-capable erofs-utils would move the erofs columns.

#### Read

| MiB | expanded, first | squashfs, first | erofs, first | tar.gz, first | expanded, warm | squashfs, warm | erofs, warm | tar.gz, warm |
|-----|-----------------|-----------------|--------------|---------------|----------------|----------------|-------------|--------------|
| 32 | 0.012 s (2651.9 MiB/s) | 0.042 s (761.9 MiB/s) | 0.026 s (1254.9 MiB/s) | 0.020 s (1561.0 MiB/s) | 0.007 s | 0.009 s | 0.018 s | 0.015 s |
| 128 | 0.049 s (2628.9 MiB/s) | 0.152 s (842.1 MiB/s) | 0.126 s (1015.9 MiB/s) | 0.078 s (1641.0 MiB/s) | 0.026 s | 0.036 s | 0.078 s | 0.064 s |
| 256 | 0.116 s (2209.8 MiB/s) | 0.212 s (1207.5 MiB/s) | 0.227 s (1127.8 MiB/s) | 0.163 s (1575.4 MiB/s) | 0.057 s | 0.063 s | 0.138 s | 0.130 s |
| 512 | 0.251 s (2042.2 MiB/s) | 0.494 s (1037.5 MiB/s) | 0.519 s (986.5 MiB/s) | 0.449 s (1141.6 MiB/s) | 0.107 s | 0.119 s | 0.265 s | 0.263 s |

Warm rates, derived from the warm seconds: expanded 4390–4982 MiB/s,
squashfs 3368–4285 MiB/s, erofs 1641–1936 MiB/s, tar.gz 1947–2065
MiB/s. Both mounts attach read-only in 6–38 ms, and all but one
sample attached within 17 ms, so attach is not a factor here; tar
never mounts, it streams member bytes.

Cold, tar.gz is the fastest of the four at every size, and both FUSE
mounts trail it: squashfs at 0.49–0.91x of tar and erofs at
0.62–0.86x. That ranking is a property of this payload, not of the
formats. Payload bytes are urandom, so gzip cannot compress them and
emits stored blocks — `image.tar.gz` is larger than the tree it packs.
Streaming it is a sequential read of one file plus a block copy, with
no inflate arithmetic, no FUSE round trip, and no per-file open. On a
compressible tree the single-threaded inflate would dominate instead
and tar would fall behind both mounts.

Warm reverses the ranking for squashfs, which reaches 1.63–2.20x of
tar while staying 0.71–0.90x of the expanded warm tree, so it is the
fastest mount at every size and the expanded tree keeps an 11–40%
lead over it. After the first pass the page cache holds the
decompressed blocks, so zstd runs once. erofs shows no comparable
lift, staying at 0.82–0.99x of tar, and tar itself stays near 2000
MiB/s, because erofsfuse re-decompresses lz4 and gzip re-reads its
stored blocks on every pass. The expanded warm tree lands at 2.1–2.5x
of tar.

#### Variance

The short read cells carry the widest relative spread, because a few
milliseconds of host jitter is a large fraction of them. The least
stable cell is squashfs cold at 128 MiB, where the two runs took
0.183 s and 0.121 s — a 1.51x spread that leaves its 842.1 MiB/s mean
indicative. erofs cold at 256 MiB follows at 1.45x (0.185 s and
0.269 s), and no other read row exceeded 1.32x. Pack times agreed
within 1.24x (squashfs at 128 MiB). Read the cold cells under a
quarter of a second as indicative rather than precise.

---

## macOS: dmg vs tar+gzip

Follow the macOS procedure above for 32, 128, 256, and 512 MiB.

### Result

Host: Mac15,6, Apple M3 Pro, 36 GiB RAM, macOS 27.0.1, arm64, APFS
data volume `disk3s1`. Date: 2026-10-26. `purge=yes` for every size.
Measured with `tiler benchmark` using the procedure above, with a
local directory as `<path>` rather than an external disk or NAS.
Pack, attach, and image read rows are the mean of two runs, with
each rate derived from that mean so a row stays internally
consistent; the expanded rows come from one walk of a kept payload
tree after the same purge, then one warm repeat.

#### Pack

| MiB | hdiutil create | tar+gzip | hdiutil out | tar.gz out |
|-----|----------------|----------|-------------|------------|
| 32 | 6.844 s (4.7 MiB/s) | 0.665 s (48.2 MiB/s) | 34264142 | 33567044 |
| 128 | 7.299 s (17.5 MiB/s) | 2.529 s (50.6 MiB/s) | 137604087 | 134270432 |
| 256 | 8.826 s (29.0 MiB/s) | 5.016 s (51.0 MiB/s) | 273171085 | 268540894 |
| 512 | 11.283 s (45.4 MiB/s) | 9.872 s (51.9 MiB/s) | 545289681 | 537085536 |

`hdiutil create` spends about 6.8 s on fixed work before the payload
size matters: the 32 MiB image takes 6.844 s, and the remaining
480 MiB up to 512 MiB adds 4.4 s, so the reported rate climbs from
4.7 to 45.4 MiB/s. tar+gzip holds near 51 MiB/s at every size: it is
10x faster on the smallest tree and still 14% faster at 512 MiB,
where that fixed cost is amortized. Both outputs stay near the input
size because the payload is incompressible, the dmg carrying about
2% of overhead and the tar.gz about 0.04%.

#### Read

| MiB | expanded, first | DMG, first | tar.gz, first | expanded, warm | DMG, warm | tar.gz, warm | attach |
|-----|-----------------|------------|---------------|----------------|-----------|--------------|--------|
| 32 | 0.020 s (1600.0 MiB/s) | 0.098 s (326.5 MiB/s) | 0.034 s (941.2 MiB/s) | 0.004 s | 0.005 s | 0.014 s | 2.672 s |
| 128 | 0.056 s (2285.7 MiB/s) | 0.356 s (359.6 MiB/s) | 0.072 s (1790.2 MiB/s) | 0.014 s | 0.015 s | 0.052 s | 2.956 s |
| 256 | 0.149 s (1718.1 MiB/s) | 0.692 s (369.9 MiB/s) | 0.135 s (1903.3 MiB/s) | 0.032 s | 0.027 s | 0.113 s | 3.048 s |
| 512 | 0.225 s (2275.6 MiB/s) | 1.334 s (383.8 MiB/s) | 0.243 s (2102.7 MiB/s) | 0.056 s | 0.065 s | 0.194 s | 2.694 s |

The first read through the mounted dmg stays near 327–384 MiB/s,
which is 0.16–0.22x of the cold read of the expanded directory.
Streaming the tar.gz after the same purge runs at 941–2103 MiB/s:
59% of the expanded tree at 32 MiB, 78% at 128 MiB, 92% at
512 MiB, and 11% ahead at 256 MiB, because it is one sequential
read of an archive rather than a walk of many files. Warm, the page
cache serves both the expanded tree and the mounted dmg, at
8000–9143 MiB/s and 7111–9482 MiB/s, so once its blocks are resident
the mount adds little. The warm tar.gz pass stays slower at
2207–2639 MiB/s: the archive is cached, but gzip still inflates every
byte. Attach itself is about 2.7–3.0 seconds and does not scale
cleanly with size.

The two runs behind each pack and image read cell agreed within
1.14x on pack time and 1.16x on the reads, while attach spread by up
to 1.47x (2.186 s and 3.203 s at 512 MiB). The expanded column is
the least stable row here: four cold walks of the same kept tree at
128 MiB spanned 0.054 s to 0.133 s, so treat the short cells as
indicative rather than precise.

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

---

## Random 4 KiB IOPS probe

Pack, attach, and read rows are large-block numbers: they say how
fast the volume streams an image, not how quickly it answers the
small random I/O that git and metadata-heavy work issues. Every run
that writes under `<path>` samples that separately, before it creates
the image.

The probe writes `.tiler-bench-probe` inside `<path>/<bench-dir>` as
`2 × --probe-ops` blocks of 4 KiB (15.6 MiB at the default 2000),
fsyncs it, drops the cache, then issues `--probe-ops` random 4 KiB
reads followed by the same number of random 4 KiB writes, each write
fsynced. The file is removed after sampling. `probe_purge` records
whether the cache drop worked, because the sample is cold only when
it did.

A probe needs a writable `<path>` and a bench dir, so `--readonly` and
`--mode read --image` refuse it. `--no-probe` skips it, which the pack
and read tables above do. To probe a local disk, point `<path>` at a
directory on that disk and keep the image cheap:

```sh
mkdir -p /path/on/local/disk

tiler benchmark /path/on/local/disk \
  --type tar --mode write \
  --size 4M --file-size 4M \
  --probe-ops 2000 --format tsv
```

The probe rows are `probe_ops`, `probe_block`, `probe_purge`,
`read_iops`, `read_lat_ms`, `read_seconds`, `write_iops`,
`write_lat_ms`, and `write_seconds`. `--format tsv` sends them to
stderr so the step rows stay parseable; `--format json` puts the same
numbers under `probe`. The write pass fsyncs every operation, so the
default 2000 writes can take seconds on a slow volume.

### Result

Host: Mac15,6, Apple M3 Pro, 36 GiB RAM, macOS 27.0.1, arm64, APFS
data volume `disk3s1` mounted at `/System/Volumes/Data`. Also a Lima
VM (Linux on macOS, Apple Virtualization framework) on an Apple
silicon host, aarch64, 6 vCPU, 11 GiB RAM, Ubuntu 24.04.5 LTS, kernel
6.8.0-147-generic, with ext4 `vda1` on virtio as `/`. Date:
2026-10-26. 2000 ops of 4 KiB in every run, `probe_purge=yes`
throughout, so every sample is cold. Each row is the mean of three
runs, with its rate derived from the mean seconds.

| Volume | Read IOPS | Read ms | Write IOPS | Write ms |
|--------|-----------|---------|------------|----------|
| macOS APFS `disk3s1` | 9537.4 | 0.105 | 238.4 | 4.195 |
| Lima ext4 `vda1` on virtio | 33900.3 | 0.029 | 5740.8 | 0.174 |

Reads are quick on both volumes and neither read pass dominates its
run: 0.21 s on APFS, 0.06 s on ext4. ext4 reads 3.6x the IOPS at
about 28% of the latency, yet both stay under 0.11 ms. Writes split
them. APFS answers 238 fsynced writes per second at 4.20 ms each, so
the 2000 writes take 8.39 s; ext4 answers 5740.8 at 0.174 ms and needs
0.35 s. That is a 24x gap in write IOPS and a 24x gap in write
latency, with the same ops and the same purge in effect.

The write column is the one that predicts how a working volume feels.
A checkout, a commit, or an index refresh issues many small writes
and fsyncs, and none of the pack, attach, or read rows above show
that cost: the tar.gz pack of a 4 MiB payload in the same runs still
reached 33–37 MiB/s while the probe managed only 238 write IOPS. Read
the absolute values as indicative: the macOS write sample is tight
(236–240 IOPS over three runs) but its read sample spans 9244–9918
IOPS, and the three Lima runs span 30301–37587 read IOPS and
5063–6416 write IOPS. The ordering is not close.
