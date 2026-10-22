# Image pack benchmarks

Scripts under this directory compare packing a directory as an image
with `tar+gzip`, and compare sequential reads of the expanded tree
with reads through a mounted image. They are not part of `make test`.

| Script | Role |
|--------|------|
| [bench-img-path.py](bench-img-path.py) | Create and read one image on a chosen path; probe the volume for IOPS and latency |
| [bench-img-vs-tar.sh](bench-img-vs-tar.sh) | For several sizes: image pack vs `tar+gzip`, then expanded-tree read vs image mount read |

`bench-img-path.py` detects the host OS. macOS defaults to `--type dmg`.
`--type tar` (gzip tar) runs on macOS and Linux. `--type squashfs` and
`--type erofs` are accepted on Linux and are not implemented yet.
Image create and read always go through that program, including tar.gz.
`bench-img-vs-tar.sh` calls it for the dmg and tar pack/read steps, and
times the expanded-tree reads itself.

---

## Linux: squashfs vs tar+gzip

Placeholder. When `bench-img-path.py --type squashfs` is implemented,
this section will record pack and read comparisons against `tar+gzip`
the same way as the dmg section below.

Expected tools once wired:

- Pack: `mksquashfs` (tiler's squashfs default)
- Mount: `squashfuse_ll`
- Driver: `docs/benchmark/bench-img-vs-tar.sh` with a Linux image type,
  or a dedicated invocation of `bench-img-path.py`

### Method

_TBD_

### Result

_TBD — no Linux squashfs numbers checked in yet._

---

## Linux: erofs vs tar+gzip

Placeholder. When `bench-img-path.py --type erofs` is implemented,
this section will record pack and read comparisons against `tar+gzip`.

Expected tools once wired:

- Pack: `mkfs.erofs`
- Mount: `erofsfuse`
- Driver: same comparison script shape as squashfs

### Method

_TBD_

### Result

_TBD — no Linux erofs numbers checked in yet._

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

## External disk and NAS with `bench-img-path.py`

Use `bench-img-path.py` when the question is how create and read
behave on a specific volume — an external USB disk, a Thunderbolt
volume, or a NAS share mounted under `/Volumes` (macOS) or
`/mnt` (Linux).

### Safety

- Writable runs create files only under `--path/tiler-img-bench/`.
  They do not delete or rename anything outside that directory.
- The default attach mountpoint is a **local** temporary directory, so
  hdiutil/FUSE attach does not mkdir on the NAS unless you pass
  `--mountpoint`.
- The device probe writes a temporary 4 KiB-random file under
  `tiler-img-bench/` and removes it. Skip it with `--no-probe`.
- If `--path` is **not writable** (read-only mount), only this form
  is accepted:

  ```sh
  docs/benchmark/bench-img-path.py \
    --path /Volumes/ro-share \
    --mode read \
    --image /Volumes/ro-share/existing/image.dmg \
    --no-probe
  ```

  That run attaches the image read-only, walks it, and writes
  **nothing** under `--path` (logs go to a local temp dir). `write`,
  `both`, create-without-`--image`, and a probe are refused. Prefer a
  local `--mountpoint` (or omit it); a mountpoint on the read-only
  volume must already exist and be writable/empty.
- On a writable volume, `--mode read --image FILE` also requires
  `--no-probe` so the read benchmark cannot leave a probe file behind.

### Writable share examples

```sh
# macOS: NAS share already mounted under /Volumes
docs/benchmark/bench-img-path.py \
  --path /Volumes/share/bench \
  --type dmg \
  --mode both \
  --size-mib 512 \
  --file-mib 4 \
  --probe-ops 2000

# Create the image on the share, then re-read it. The dmg file stays
# on the share. Attach mounts that image onto the local physical disk
# (omit --mountpoint for a local temp dir, or pass an empty directory
# on the internal volume). The read walk uses that local mount, not a
# directory on the share.
docs/benchmark/bench-img-path.py \
  --path /Volumes/share/bench \
  --mode write \
  --type dmg \
  --size-mib 256 \
  --keep
mkdir -p /tmp/tiler-bench-mnt
docs/benchmark/bench-img-path.py \
  --path /Volumes/share/bench \
  --mountpoint /tmp/tiler-bench-mnt \
  --type dmg \
  --mode read \
  --image /Volumes/share/bench/tiler-img-bench/image.dmg \
  --no-probe
```

`--mode write` times only image create. `--mode read` without
`--image` still creates an image first (setup) and needs a writable
`--path`. `--mode both` times create and both read passes. `--type tar`
packs with `tar -czf` and reads by streaming member bytes; it does not
mount. `--src DIR` packs an existing tree instead of generating a new
one. For dmg, `--compression` is `ULFO`, `ULMO`, or `UDRO`, and `--fs`
is `APFS` or `HFS+`. `--probe-ops` defaults to 2000. `--keep` leaves
`tiler-img-bench/` under `--path`. `--tsv` prints only step rows for
scripts that build a larger table. Payload bytes are `os.urandom`.

On Linux the same `--path` pattern will apply once squashfs and erofs
are implemented; until then those `--type` values exit with "not
implemented yet".
