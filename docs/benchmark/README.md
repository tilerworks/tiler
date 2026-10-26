# Image pack benchmarks

Prefer `tiler benchmark <path>` for a single image size on a chosen
volume (external disk or NAS). The scripts below stay as standalone
helpers: they are not part of `make test`, and
`bench-img-vs-tar.sh` still drives the multi-size image-vs-tar tables
in this document.

| Tool | Role |
|------|------|
| `tiler benchmark <path>` | Create and read one image on a chosen path; probe the volume for IOPS and latency |
| [bench-img-path.py](bench-img-path.py) | Standalone Python helper with the same path-level workflow (kept for comparison and scripting outside the binary) |
| [bench-img-vs-tar.sh](bench-img-vs-tar.sh) | Standalone multi-size helper: image pack vs `tar+gzip`, then expanded-tree read vs image mount read |

`bench-img-path.py` detects the host OS. macOS defaults to `--type dmg`.
`--type tar` (gzip tar) runs on macOS and Linux. `--type squashfs` and
`--type erofs` run on Linux: they pack with `mksquashfs` and
`mkfs.erofs` (the same argv tiler uses) and attach read-only with
`squashfuse_ll` and `erofsfuse`, the FUSE clients tiler mounts with. A
root run without the FUSE clients falls back to kernel loop mounts.
Image create and read always go through that program, including tar.gz.
`bench-img-vs-tar.sh` calls it for the image and tar pack/read steps —
dmg on macOS, squashfs and erofs on Linux — and times the expanded-tree
reads itself.

---

## Linux: squashfs and erofs vs tar+gzip

```sh
docs/benchmark/bench-img-vs-tar.sh
```

Same defaults and overrides as the macOS section (`BENCH_SIZES_MIB`,
`BENCH_FILE_MIB`, `BENCH_ROOT`). Needs `python3`, `tar`, `mksquashfs`,
`mkfs.erofs`, and — for a rootless mount — `squashfuse_ll`,
`erofsfuse`, and `fusermount3`. Every size packs one payload tree as
squashfs, as erofs, and as tar.gz, then reads each artifact back.

### Method

- Payload bytes are `os.urandom`, so no packer can collapse the tree
  into a tiny archive of zeros.
- Packing goes through `bench-img-path.py` with the argv tiler uses:
  `mksquashfs -comp zstd -noappend` and `mkfs.erofs -U <nil-uuid>
  -zlz4`. The erofs compressor defaults to the best one the host's
  `mkfs.erofs` offers (zstd, then lz4, then gzip/deflate); Ubuntu
  24.04's build has no zstd, so the run below packed with lz4. tar
  packs the same tree with `tar -czf`.
- Reads attach the image read-only with `squashfuse_ll` / `erofsfuse`
  and walk the mount twice. Attach took 3–8 ms for both formats and is
  reported separately from the read rates.
- Before each cold pass the benchmark drops caches when passwordless
  sudo is available (`sync; echo 3 > /proc/sys/vm/drop_caches`;
  `purge=yes` in the notes). The `*_2` passes repeat the walk with no
  purge in between and hit the page cache.
- MiB/s for a pack step is input size over wall time; for a read step,
  bytes read over wall time. Reading the tar.gz streams member bytes
  and does not extract onto the volume under test.

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

```sh
docs/benchmark/bench-img-vs-tar.sh
```

Defaults are totals of 32, 128, 256, and 512 MiB, split into 4 MiB
files. Override with `BENCH_SIZES_MIB`, `BENCH_FILE_MIB`, and
`BENCH_ROOT`. Needs `python3`, `tar`, and `hdiutil`.

### Method

- Payload bytes are `os.urandom`, so neither packer can collapse the
  tree into a tiny archive of zeros.
- Packing uses `hdiutil create -format ULFO -fs APFS` (the same format
  tiler uses for a new dmg) and `tar -czf`, both through
  `bench-img-path.py` (`--type dmg` and `--type tar`). The tar archive
  is built from the same payload tree as the dmg (`--src`).
- Reading the tar streams member bytes with `tarfile` (`tar/read_image_*`).
  That does not extract onto the volume under test. `read_expanded_*`
  is still a direct walk of the unpacked tree in this shell script.
- Before the expanded-tree read the script runs `sudo -n purge` when
  passwordless sudo is available. `read_expanded_1` is a cold read
  only when the notes say `purge=yes`.
- `read_image_1` is the first walk of the attached volume. It includes
  UDIF decompression. `read_image_2` and `read_expanded_2` repeat that
  walk with no purge in between, so they hit the page cache.
- MiB/s for a pack step is input size over wall time. MiB/s for a read
  step is bytes read over wall time. Attach time is reported separately
  and is not included in the read rates.

### Result

Host: Mac15,6, Apple M3 Pro, 36 GiB RAM, macOS 27.0.1, arm64.
Date: 2026-10-22. `purge=yes` for every size.

#### Pack

| MiB | hdiutil create | tar+gzip | hdiutil out | tar.gz out |
|-----|----------------|----------|-------------|------------|
| 32 | 5.071 s (6.4 MiB/s) | 0.661 s (48.5 MiB/s) | 34264363 | 33569792 |
| 128 | 5.085 s (25.8 MiB/s) | 2.486 s (51.5 MiB/s) | 137603685 | 134272119 |
| 256 | 7.078 s (36.9 MiB/s) | 4.892 s (52.4 MiB/s) | 274177330 | 268544148 |
| 512 | 8.085 s (64.2 MiB/s) | 9.707 s (52.8 MiB/s) | 544298255 | 537088118 |

`hdiutil create` spends about five seconds before the payload size
matters, so it is slower than tar on the smaller trees. At 512 MiB
that fixed cost is amortized and ULFO finishes ahead of gzip. Both
outputs stay near the input size because the payload is incompressible.

#### Read

| MiB | expanded, first | DMG, first | tar.gz, first | expanded, warm | DMG, warm | tar.gz, warm | attach |
|-----|-----------------|------------|---------------|----------------|-----------|--------------|--------|
| 32 | 0.016 s (2000.0 MiB/s) | 0.083 s (384.3 MiB/s) | 0.016 s (2022.8 MiB/s) | 0.003 s | 0.003 s | 0.007 s | 3.413 s |
| 128 | 0.067 s (1910.4 MiB/s) | 0.341 s (375.5 MiB/s) | 0.042 s (3029.4 MiB/s) | 0.011 s | 0.010 s | 0.034 s | 3.322 s |
| 256 | 0.147 s (1741.5 MiB/s) | 0.668 s (383.5 MiB/s) | 0.073 s (3510.8 MiB/s) | 0.023 s | 0.020 s | 0.056 s | 2.982 s |
| 512 | 0.234 s (2188.0 MiB/s) | 1.319 s (388.2 MiB/s) | 0.149 s (3429.6 MiB/s) | 0.042 s | 0.043 s | 0.121 s | 3.197 s |

The first read through the mounted dmg stays near 380 MiB/s, about
one fifth of the cold read of the expanded directory. Streaming the
tar.gz after the same purge is as fast as the expanded tree at 32 MiB
and faster at the larger sizes, because it is one sequential inflate
rather than a walk of many files. The second pass of the expanded
tree and the mounted dmg is limited by the page cache and lands at
the same rate. The warm tar.gz pass stays slower: the archive is
cached, but gzip still inflates every byte. Attach itself is about
3 seconds and does not scale cleanly with size.

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
mount. `--src DIR` packs an existing tree instead of generating a new
one. `--size` and `--file-size` use the same byte-size syntax as the
rest of tiler (`128M`, `4M`, bare integers are bytes). For dmg,
`--compress` is `ULFO` (default), `ULMO`, or `UDRO`. For squashfs it
is `zstd` (default), `gzip`, `lz4`, or `xz`; for erofs it is `zstd`,
`lz4`, or `gzip`, defaulting to the best compressor the host's
`mkfs.erofs` lists. tar rejects `--compress`. `--probe-ops` defaults
to 2000. `--keep` leaves `--bench-dir` in place. `--tsv` prints only
step rows for scripts that build a larger table. Payload bytes are
random and incompressible.

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
