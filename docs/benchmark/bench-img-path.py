#!/usr/bin/env python3
"""Time image create and read on a chosen directory.

macOS dmg packs with hdiutil. Linux squashfs packs with mksquashfs and
erofs with mkfs.erofs, mirroring the argv tiler uses. Read runs attach
the image read-only: squashfuse_ll and erofsfuse on Linux (the FUSE
clients tiler mounts with; a kernel loop mount when running as root
without them), hdiutil on macOS.

Writable volumes: create and the optional device probe write only
under --path/tiler-img-bench/. The attach mountpoint defaults to a
local temporary directory.

Read-only volumes: only --mode read --image FILE is allowed. That
path attaches the existing image read-only, reads it, and writes
nothing on --path (no probe file, no work directory, no image
create). Logs go to a local temporary directory.

--no-probe skips the 4 KiB IOPS probe. --tsv prints only step rows.
"""

import argparse
import os
import random
import shutil
import subprocess
import sys
import tempfile
import time
from pathlib import Path

MIB = 1024 * 1024
PROBE_BLOCK = 4096
WORK_NAME = "tiler-img-bench"
IMAGE_NAMES = {
    "dmg": "image.dmg",
    "tar": "image.tar.gz",
    "squashfs": "image.sqsh",
    "erofs": "image.erofs",
}
# mkfs.erofs records a random superblock UUID without -U. tiler pins
# the nil UUID so the same tree packs to the same bytes.
EROFS_NIL_UUID = "00000000-0000-0000-0000-000000000000"


def detect_os():
    if sys.platform == "darwin":
        return "darwin"
    if sys.platform.startswith("linux"):
        return "linux"
    return sys.platform


def die(message, code=2):
    print(f"bench-img-path: {message}", file=sys.stderr)
    raise SystemExit(code)


def require_tools(names):
    missing = [name for name in names if shutil.which(name) is None]
    if missing:
        die("missing " + ", ".join(missing), 1)


def mib_per_s(seconds, nbytes):
    if seconds <= 0:
        return "inf"
    return f"{nbytes / seconds / MIB:.1f}"


def run_timed(argv, log_path):
    start = time.perf_counter()
    with log_path.open("w") as fh:
        status = subprocess.run(argv, stdout=fh, stderr=subprocess.STDOUT).returncode
    return time.perf_counter() - start, status


def fail_log(message, log_path):
    print(f"bench-img-path: {message} (see {log_path})", file=sys.stderr)
    sys.stderr.write(log_path.read_text())
    raise SystemExit(1)


def write_tree(dest, total, file_size):
    dest.mkdir(parents=True, exist_ok=True)
    remaining = total
    index = 0
    while remaining > 0:
        n = file_size if remaining > file_size else remaining
        path = dest / f"f{index:05d}"
        with path.open("wb") as fh:
            left = n
            while left > 0:
                chunk = min(left, MIB)
                fh.write(os.urandom(chunk))
                left -= chunk
        remaining -= n
        index += 1
    return index


def read_tree(root):
    start = time.perf_counter()
    nbytes = 0
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames.sort()
        filenames.sort()
        for name in filenames:
            if name.startswith("."):
                continue
            path = Path(dirpath) / name
            with path.open("rb") as fh:
                while True:
                    chunk = fh.read(MIB)
                    if not chunk:
                        break
                    nbytes += len(chunk)
    return time.perf_counter() - start, nbytes


def try_purge():
    """Best-effort cache drop; yes only with passwordless sudo."""
    host = detect_os()
    if host == "darwin":
        argv = ["sudo", "-n", "purge"]
    elif host == "linux":
        argv = [
            "sudo", "-n", "sh", "-c",
            "sync; echo 3 > /proc/sys/vm/drop_caches",
        ]
    else:
        return "no"
    result = subprocess.run(argv, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    return "yes" if result.returncode == 0 else "no"


def print_device_info(path):
    df = subprocess.check_output(["df", "-P", str(path)], text=True).splitlines()
    if len(df) < 2:
        die(f"df failed for {path}", 1)
    dev = df[1].split()[0]
    print(f"volume_device: {dev}")
    print(f"df: {df[1]}")
    if detect_os() != "darwin":
        return
    info = subprocess.check_output(["diskutil", "info", dev], text=True)
    fields = {
        "Device Identifier": "device_id",
        "Volume Name": "volume_name",
        "Mount Point": "mount_point",
        "File System Personality": "filesystem",
        "Protocol": "protocol",
        "Solid State": "solid_state",
        "Device Location": "location",
        "Disk Size": "disk_size",
        "Volume Free Space": "free_space",
        "Part of Whole": "whole_disk",
    }
    for line in info.splitlines():
        if ":" not in line:
            continue
        key, value = line.split(":", 1)
        label = fields.get(key.strip())
        if label:
            print(f"{label}: {value.strip()}")


def create_probe_file(root, ops):
    blocks = max(ops * 2, 256)
    path = root / ".tiler-img-bench-probe"
    payload = os.urandom(PROBE_BLOCK)
    with path.open("wb") as fh:
        for _ in range(blocks):
            fh.write(payload)
        fh.flush()
        os.fsync(fh.fileno())
    return path


def probe_device_io(path, ops, purged):
    size = path.stat().st_size
    blocks = size // PROBE_BLOCK
    if blocks < 1:
        die("probe file is empty", 1)
    payload = os.urandom(PROBE_BLOCK)

    def timed_random(mode):
        latencies = []
        start = time.perf_counter()
        with path.open(mode) as fh:
            fd = fh.fileno()
            for _ in range(ops):
                off = random.randrange(blocks) * PROBE_BLOCK
                t0 = time.perf_counter()
                os.lseek(fd, off, os.SEEK_SET)
                if mode == "rb":
                    got = os.read(fd, PROBE_BLOCK)
                    if len(got) != PROBE_BLOCK:
                        die("short read in probe", 1)
                else:
                    if os.write(fd, payload) != PROBE_BLOCK:
                        die("short write in probe", 1)
                    os.fsync(fd)
                latencies.append(time.perf_counter() - t0)
            if mode != "rb":
                os.fsync(fd)
        elapsed = time.perf_counter() - start
        avg_ms = (sum(latencies) / len(latencies)) * 1000.0
        iops = ops / elapsed if elapsed > 0 else float("inf")
        return elapsed, iops, avg_ms

    read_s, read_iops, read_ms = timed_random("rb")
    write_s, write_iops, write_ms = timed_random("r+b")
    path.unlink()
    print(f"probe_ops: {ops}")
    print(f"probe_block: {PROBE_BLOCK}")
    print(f"probe_purge: {purged}")
    print(f"read_iops: {read_iops:.1f}")
    print(f"read_lat_ms: {read_ms:.3f}")
    print(f"read_seconds: {read_s:.3f}")
    print(f"write_iops: {write_iops:.1f}")
    print(f"write_lat_ms: {write_ms:.3f}")
    print(f"write_seconds: {write_s:.3f}")


class Reporter:
    def __init__(self, tsv):
        self.tsv = tsv
        self.rows = []
        if not tsv:
            print(f"{'step':<18} {'seconds':>10} {'bytes':>12} {'MiB/s':>10} {'notes':>12}")
            print(f"{'-' * 18} {'-' * 10} {'-' * 12} {'-' * 10} {'-' * 12}")

    def row(self, step, seconds, nbytes, notes):
        rate = mib_per_s(seconds, nbytes) if isinstance(nbytes, int) else "-"
        sec = f"{seconds:.3f}"
        raw = "-" if nbytes == "-" else str(nbytes)
        self.rows.append((step, sec, raw, rate, notes))
        if self.tsv:
            print(f"{step}\t{sec}\t{raw}\t{rate}\t{notes}")
        else:
            print(f"{step:<18} {sec:>10} {raw:>12} {rate:>10} {notes:>12}")


def check_kind(kind, host):
    if kind == "dmg":
        if host != "darwin":
            die("dmg images require macOS")
        require_tools(["hdiutil", "diskutil"])
        return
    if kind == "tar":
        require_tools(["tar"])
        return
    if kind in ("squashfs", "erofs"):
        if host != "linux":
            die(f"{kind} images require Linux")
        require_tools(["mksquashfs"] if kind == "squashfs" else ["mkfs.erofs"])
        return
    die(f"unknown image type {kind}")


def detach(mount):
    subprocess.run(
        ["diskutil", "eject", str(mount)],
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    subprocess.run(
        ["hdiutil", "detach", str(mount)],
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )


def prepare_mountpoint(requested):
    if requested:
        mount = Path(requested).expanduser().resolve()
        mount.mkdir(parents=True, exist_ok=True)
        if any(mount.iterdir()):
            die(f"--mountpoint is not empty: {mount}")
        return mount, False
    return Path(tempfile.mkdtemp(prefix="tiler-img-mnt.")), True


def create_dmg(work, data, image, compression, filesystem, log_path):
    argv = [
        "hdiutil", "create",
        "-srcfolder", str(data),
        "-format", compression,
        "-fs", filesystem,
        "-volname", "bench",
        "-ov", str(image),
    ]
    elapsed, status = run_timed(argv, log_path)
    if status != 0:
        fail_log("hdiutil create failed", log_path)
    if not image.is_file() and Path(str(image) + ".dmg").is_file():
        image = Path(str(image) + ".dmg")
    return elapsed, image


def create_squashfs(data, image, comp, log_path):
    """Pack data with mksquashfs, the argv shape tiler uses."""
    if image.exists():
        image.unlink()
    argv = ["mksquashfs", str(data), str(image), "-comp", comp, "-noappend"]
    elapsed, status = run_timed(argv, log_path)
    if status != 0:
        fail_log("mksquashfs failed", log_path)
    if not image.is_file():
        die(f"mksquashfs did not write {image}", 1)
    return elapsed, image


def erofs_comp_flag(comp):
    """gzip stays gzip in metadata and becomes -zdeflate on the argv."""
    return "-zdeflate" if comp == "gzip" else "-z" + comp


def default_erofs_comp():
    """Pick the erofs compressor like tiler: zstd, then lz4, then gzip.

    mkfs.erofs --help prints "Available compressors are: ...". Builds
    without libzstd (Ubuntu 24.04) fall back to lz4. lz4hc does not
    satisfy lz4. A help text without the marker predates compressor
    choice, where lz4 is the only sane default.
    """
    require_tools(["mkfs.erofs"])
    result = subprocess.run(
        ["mkfs.erofs", "--help"],
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
    )
    names = set()
    marker = "Available compressors are:"
    for line in (result.stdout or "").splitlines():
        if marker in line:
            names = {n.strip() for n in line.split(marker, 1)[1].split(",")}
            break
    if not names:
        return "lz4"
    for comp in ("zstd", "lz4"):
        if comp in names:
            return comp
    if "deflate" in names or "libdeflate" in names:
        return "gzip"
    die("mkfs.erofs has no usable compressor (zstd, lz4, or gzip)", 1)


def create_erofs(data, image, comp, log_path):
    """Pack data with mkfs.erofs, the argv shape tiler uses."""
    if image.exists():
        image.unlink()
    argv = [
        "mkfs.erofs", "-U", EROFS_NIL_UUID, erofs_comp_flag(comp),
        str(image), str(data),
    ]
    elapsed, status = run_timed(argv, log_path)
    if status != 0:
        fail_log("mkfs.erofs failed", log_path)
    if not image.is_file():
        die(f"mkfs.erofs did not write {image}", 1)
    return elapsed, image


def create_tar(src, image, log_path):
    """Pack src with tar -czf. The archive must not sit inside src."""
    try:
        image.resolve().relative_to(src.resolve())
    except ValueError:
        pass
    else:
        die(f"archive path is inside the tree being packed: {image}")
    if image.exists():
        image.unlink()
    argv = ["tar", "-czf", str(image), "-C", str(src.parent), src.name]
    elapsed, status = run_timed(argv, log_path)
    if status != 0:
        fail_log("tar -czf failed", log_path)
    if not image.is_file():
        die(f"tar -czf did not write {image}", 1)
    return elapsed, image


def read_tar(image):
    """Stream member bytes out of a gzip tar. Writes nothing."""
    import tarfile

    start = time.perf_counter()
    nbytes = 0
    with tarfile.open(image, "r:gz") as archive:
        for member in archive:
            if not member.isfile() or Path(member.name).name.startswith("."):
                continue
            handle = archive.extractfile(member)
            if handle is None:
                continue
            while True:
                chunk = handle.read(MIB)
                if not chunk:
                    break
                nbytes += len(chunk)
    return time.perf_counter() - start, nbytes


def attach_dmg(image, mount, log_path):
    argv = [
        "hdiutil", "attach",
        "-nobrowse", "-noautoopen", "-readonly",
        "-mountpoint", str(mount),
        str(image),
    ]
    elapsed, status = run_timed(argv, log_path)
    if status != 0:
        fail_log("hdiutil attach failed", log_path)
    return elapsed


def fuse_umount_tool():
    for tool in ("fusermount3", "fusermount"):
        if shutil.which(tool) is not None:
            return tool
    return None


def linux_mount_argv(kind, image, mount):
    """Mount argv, unmount argv, and a note for a read-only attach.

    tiler mounts squashfs with squashfuse_ll and erofs with erofsfuse,
    so the benchmark prefers those FUSE clients and measures the same
    read path. A root run without a usable FUSE setup falls back to a
    kernel loop mount.
    """
    tool = "squashfuse_ll" if kind == "squashfs" else "erofsfuse"
    umount = fuse_umount_tool()
    if (
        shutil.which(tool) is not None
        and umount is not None
        and os.path.exists("/dev/fuse")
    ):
        if tool == "squashfuse_ll":
            argv = [tool, "-o", f"fsname={image}", str(image), str(mount)]
        else:
            argv = [tool, "-o", "ro", str(image), str(mount)]
        return argv, [umount, "-u", str(mount)], f"{tool} ro"
    if os.geteuid() == 0:
        argv = ["mount", "-t", kind, "-o", "ro,loop", str(image), str(mount)]
        return argv, ["umount", str(mount)], f"kernel {kind} ro"
    if shutil.which(tool) is None:
        die(f"missing {tool} (or run as root for a kernel {kind} mount)", 1)
    if umount is None:
        die(f"{tool} needs fusermount3 or fusermount to unmount", 1)
    die("/dev/fuse is missing (or run as root for a kernel mount)", 1)


def attach_linux(kind, image, mount, log_path):
    argv, umount_argv, note = linux_mount_argv(kind, image, mount)
    elapsed, status = run_timed(argv, log_path)
    if status != 0:
        fail_log(f"{argv[0]} attach failed", log_path)
    return elapsed, note, umount_argv


def run_umount(argv):
    """Best-effort unmount; a failure leaves the mountpoint busy."""
    subprocess.run(argv, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def parse_args(argv):
    host = detect_os()
    parser = argparse.ArgumentParser(prog="bench-img-path")
    parser.add_argument("--path", required=True, help="directory on the volume under test")
    parser.add_argument("--mountpoint", help="attach mountpoint (default: local temp)")
    parser.add_argument("--mode", choices=("write", "read", "both"), default="both")
    parser.add_argument("--size-mib", type=int, default=128)
    parser.add_argument("--file-mib", type=int, default=4)
    parser.add_argument(
        "--type",
        choices=("dmg", "tar", "squashfs", "erofs"),
        default="dmg" if host == "darwin" else None,
        help="image type (default: dmg on macOS; required on Linux)",
    )
    parser.add_argument(
        "--compression",
        default="ULFO",
        help="dmg compression: ULFO, ULMO, or UDRO",
    )
    parser.add_argument("--fs", default="APFS", help="dmg filesystem: APFS or HFS+")
    parser.add_argument(
        "--comp",
        help="squashfs/erofs compressor (default: zstd for squashfs; "
        "the best mkfs.erofs on this host supports for erofs)",
    )
    parser.add_argument("--image", help="existing image for --mode read")
    parser.add_argument(
        "--src",
        help="pack this existing directory instead of generating a payload",
    )
    parser.add_argument("--probe-ops", type=int, default=2000)
    parser.add_argument("--no-probe", action="store_true")
    parser.add_argument("--tsv", action="store_true", help="print step rows only")
    parser.add_argument("--keep", action="store_true")
    args = parser.parse_args(argv)
    if args.type is None:
        die("pass --type tar, squashfs, or erofs on Linux")
    if args.size_mib < 1 or args.file_mib < 1 or args.probe_ops < 1:
        die("sizes and --probe-ops must be at least 1")
    compression = args.compression.upper()
    if args.type == "dmg" and compression not in ("ULFO", "ULMO", "UDRO"):
        die("--compression must be ULFO, ULMO, or UDRO")
    filesystem = args.fs
    if filesystem.lower() == "apfs":
        filesystem = "APFS"
    elif filesystem.lower() == "hfs+":
        filesystem = "HFS+"
    elif args.type == "dmg":
        die("--fs must be APFS or HFS+")
    args.compression = compression
    args.fs = filesystem
    comp = (args.comp or "").lower() or None
    if args.type == "squashfs":
        if comp is None:
            comp = "zstd"
        if comp not in ("zstd", "gzip", "lz4", "xz"):
            die("--comp must be zstd, gzip, lz4, or xz for squashfs")
    elif args.type == "erofs":
        if comp is None:
            comp = default_erofs_comp()
        if comp not in ("zstd", "lz4", "gzip"):
            die("--comp must be zstd, lz4, or gzip for erofs")
    elif comp is not None:
        die("--comp applies to squashfs and erofs only")
    args.comp = comp
    if args.mode == "read" and args.image and not Path(args.image).is_file():
        die(f"--image is not a file: {args.image}")
    if args.src:
        src = Path(args.src).expanduser()
        if not src.is_dir():
            die(f"--src is not a directory: {args.src}")
        args.src = src.resolve()
    path = Path(args.path).expanduser()
    if not path.is_dir():
        die(f"--path is not a directory: {args.path}")
    args.path = path.resolve()
    args.path_writable = os.access(args.path, os.W_OK)
    return args, host


def same_volume(path_a, path_b):
    """True when both paths resolve on the same device id."""
    try:
        return path_a.resolve().stat().st_dev == path_b.resolve().stat().st_dev
    except OSError:
        return False


def plan_ops(args):
    """Decide create/read/probe and refuse unsafe combinations.

    A read-only --path may only run --mode read --image FILE with
    --no-probe. That combination must not mkdir or write under --path.
    """
    given_image = args.image is not None
    do_write = args.mode in ("write", "both")
    do_read = args.mode in ("read", "both")
    if do_read and not given_image:
        do_write = True
    do_probe = not args.no_probe

    if not args.path_writable:
        if do_write:
            die(
                "--path is not writable; use --mode read --image FILE "
                "(and omit the probe with --no-probe)"
            )
        if not given_image:
            die("--path is not writable; pass --image for a read-only benchmark")
        if do_probe:
            die(
                "--path is not writable; the device probe writes under "
                "--path — pass --no-probe"
            )
        if args.mountpoint:
            mount = Path(args.mountpoint).expanduser()
            if same_volume(args.path, mount) and (
                not mount.exists() or not os.access(mount, os.W_OK)
            ):
                die(
                    "--mountpoint is on the read-only volume and is not a "
                    "writable empty directory; omit --mountpoint to use a "
                    "local temporary directory"
                )
        do_probe = False

    # Writable path, pure read of an existing image: still skip probe
    # unless the user asked for it, so a "read only" run does not
    # surprise-write a probe file. Require --no-probe for zero writes
    # under --path when --image is set and mode is read.
    if (
        args.path_writable
        and args.mode == "read"
        and given_image
        and do_probe
    ):
        die(
            "--mode read --image does not write under --path only when "
            "--no-probe is set; pass --no-probe for a write-free read, "
            "or omit --image to allow create and probe"
        )

    return do_write, do_read, do_probe, given_image


def main(argv):
    args, host = parse_args(argv)
    check_kind(args.type, host)
    do_write, do_read, do_probe, given_image = plan_ops(args)

    # Writes under --path stay inside WORK_NAME. Read-only runs put
    # logs on a local temp dir so --path is never created or written.
    owned_work = False
    if do_write or do_probe:
        work = args.path / WORK_NAME
        work.mkdir(parents=True, exist_ok=True)
    else:
        work = Path(tempfile.mkdtemp(prefix="tiler-img-bench-ro."))
        owned_work = True

    mount = None
    owned_mount = None
    unmounter = None
    try:
        if not args.tsv:
            print(f"os: {host}")
            print(f"type: {args.type}")
            print(f"path: {args.path}")
            print(f"path_writable: {'yes' if args.path_writable else 'no'}")
            print(f"mountpoint: {args.mountpoint or '(local temp)'}")
            print(f"mode: {args.mode}")
            print(f"size_mib: {args.size_mib}")
            print(f"file_mib: {args.file_mib}")
            if args.type == "dmg":
                print(f"compression: {args.compression}")
                print(f"fs: {args.fs}")
            elif args.comp:
                print(f"comp: {args.comp}")
            print(f"probe: {'yes' if do_probe else 'no'}")
            if do_probe:
                print(f"probe_ops: {args.probe_ops}")
            print()
            print("## device for --path")
            print_device_info(args.path)
            print()
        if do_probe:
            probe_out = sys.stderr if args.tsv else sys.stdout
            if not args.tsv:
                print("## device probe (random 4 KiB on --path)")
            probe = create_probe_file(work, args.probe_ops)
            purged = try_purge()
            os.sync()
            _stdout = sys.stdout
            try:
                sys.stdout = probe_out
                probe_device_io(probe, args.probe_ops, purged)
            finally:
                sys.stdout = _stdout
            if not args.tsv:
                print()

        image_file = IMAGE_NAMES[args.type]
        image = Path(args.image).expanduser().resolve() if given_image else work / image_file
        if given_image and not image.is_file():
            die(f"--image is not a file: {image}")
        if not do_write and not given_image:
            die("internal error: read without --image would create an image")
        if not do_write and do_probe:
            die("internal error: probe would write under --path")

        report = Reporter(args.tsv)
        files = 0
        if do_write:
            if args.src:
                data = args.src
                files = sum(
                    1
                    for dirpath, dirnames, filenames in os.walk(data)
                    for name in filenames
                    if not name.startswith(".")
                )
            else:
                data = work / "data"
                files = write_tree(data, args.size_mib * MIB, args.file_mib * MIB)
            if args.type == "tar":
                elapsed, image = create_tar(
                    data, work / IMAGE_NAMES["tar"], work / "tar-create.log"
                )
                note = f"tar.gz {files} files"
            elif args.type == "dmg":
                elapsed, image = create_dmg(
                    work, data, work / IMAGE_NAMES["dmg"], args.compression, args.fs,
                    work / "hdiutil-create.log",
                )
                note = f"dmg {files} files"
            elif args.type == "squashfs":
                elapsed, image = create_squashfs(
                    data, work / IMAGE_NAMES["squashfs"], args.comp,
                    work / "mksquashfs.log",
                )
                note = f"squashfs {args.comp} {files} files"
            else:
                elapsed, image = create_erofs(
                    data, work / IMAGE_NAMES["erofs"], args.comp,
                    work / "mkfs-erofs.log",
                )
                note = f"erofs {args.comp} {files} files"
            if args.mode == "read":
                note += " setup"
            report.row("image_create", elapsed, image.stat().st_size, note)

        if do_read and args.type == "tar":
            purged = try_purge()
            os.sync()
            seconds, nbytes = read_tar(image)
            report.row("read_image_1", seconds, nbytes, f"tar.gz purge={purged}")
            seconds, nbytes = read_tar(image)
            report.row("read_image_2", seconds, nbytes, "tar.gz warm")
        elif do_read:
            purged = try_purge()
            os.sync()
            mount, owned = prepare_mountpoint(args.mountpoint)
            if owned:
                owned_mount = mount
            if args.type == "dmg":
                elapsed = attach_dmg(image, mount, work / "hdiutil-attach.log")
                note = "readonly"
                unmounter = lambda: detach(mount)  # noqa: E731
            else:
                elapsed, note, umount_argv = attach_linux(
                    args.type, image, mount, work / f"{args.type}-attach.log"
                )
                unmounter = lambda: run_umount(umount_argv)  # noqa: E731
            report.row("image_attach", elapsed, "-", note)
            seconds, nbytes = read_tree(mount)
            report.row("read_image_1", seconds, nbytes, f"purge={purged}")
            seconds, nbytes = read_tree(mount)
            report.row("read_image_2", seconds, nbytes, "warm")
            unmounter()
            mount = None
            unmounter = None

        if not args.tsv:
            print()
            print("image_create MiB/s is payload size over wall time.")
            print("read_image MiB/s is bytes read from the attached image over wall time.")
            if do_probe:
                print(
                    "read_iops / write_iops are random 4 KiB ops under "
                    f"--path/{WORK_NAME} (write fsyncs each op)."
                )
            if not args.path_writable:
                print(
                    "path_writable=no: no files were created or modified "
                    "under --path."
                )
            print(
                "Writable runs confine creates to "
                f"--path/{WORK_NAME}. "
                "The attach mountpoint is --mountpoint or a local temp dir."
            )
    finally:
        if unmounter is not None:
            unmounter()
        if owned_mount is not None:
            shutil.rmtree(owned_mount, ignore_errors=True)
        if owned_work:
            shutil.rmtree(work, ignore_errors=True)
        elif not args.keep and not given_image and (do_write or do_probe):
            shutil.rmtree(args.path / WORK_NAME, ignore_errors=True)


if __name__ == "__main__":
    main(sys.argv[1:])
