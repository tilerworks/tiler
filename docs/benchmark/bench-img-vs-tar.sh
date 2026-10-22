#!/bin/sh
# Compare image packing with tar+gzip, then compare sequential reads of
# the expanded tree with reads of the same bytes through the image mount.
#
# Image create and read, including tar.gz, go through bench-img-path.py.
# On macOS the image type is dmg. squashfs and erofs are not wired in yet.
#
# Payload bytes are os.urandom so the result measures compressor and
# reader cost, not a trivially compressible run of zeros.
#
#   BENCH_SIZES_MIB   space-separated totals (default: 32 128 256 512)
#   BENCH_FILE_MIB    size of each file (default: 4)
#   BENCH_ROOT        scratch directory (default: mktemp under $TMPDIR)
#
# macOS only until a Linux image type is implemented. Tries `sudo -n
# purge` before the expanded-tree read. The image read purges again
# inside bench-img-path.py.

set -eu

here=$(CDPATH= cd "$(dirname "$0")" && pwd)
img_py=$here/bench-img-path.py

if test "$(uname -s)" != Darwin
then
	echo "bench-img-vs-tar: dmg comparison requires macOS" >&2
	exit 1
fi

for cmd in tar python3
do
	if ! command -v "$cmd" >/dev/null 2>&1
	then
		echo "bench-img-vs-tar: missing $cmd" >&2
		exit 1
	fi
done

SIZES=${BENCH_SIZES_MIB:-32 128 256 512}
FILE_MIB=${BENCH_FILE_MIB:-4}
KEEP_ROOT=0
if test -n "${BENCH_ROOT:-}"
then
	ROOT=$BENCH_ROOT
	mkdir -p "$ROOT"
	KEEP_ROOT=1
else
	ROOT=$(mktemp -d "${TMPDIR:-/tmp}/tiler-bench.XXXXXX")
fi

cleanup() {
	if test "$KEEP_ROOT" -eq 0
	then
		rm -rf "$ROOT"
	fi
}
trap cleanup EXIT INT TERM

# read_tree prints "<seconds> <bytes>".
read_tree() {
	python3 - "$1" <<'PY'
import os, sys, time
root = sys.argv[1]
start = time.perf_counter()
nbytes = 0
for dirpath, dirnames, filenames in os.walk(root):
    dirnames.sort()
    filenames.sort()
    for name in filenames:
        if name.startswith("."):
            continue
        path = os.path.join(dirpath, name)
        with open(path, "rb") as fh:
            while True:
                chunk = fh.read(1024 * 1024)
                if not chunk:
                    break
                nbytes += len(chunk)
elapsed = time.perf_counter() - start
sys.stdout.write(f"{elapsed:.3f} {nbytes}\n")
PY
}

mib_s() {
	python3 - "$1" "$2" <<'PY'
import sys
seconds = float(sys.argv[1])
nbytes = float(sys.argv[2])
if seconds <= 0:
    print("inf")
else:
    print(f"{nbytes / seconds / (1024 * 1024):.1f}")
PY
}

try_purge() {
	if sudo -n purge >/dev/null 2>&1
	then
		echo yes
	else
		echo no
	fi
}

print_row() {
	printf "%-8s %-22s %10s %12s %10s %12s\n" "$1" "$2" "$3" "$4" "$5" "$6"
}

print_tsv() {
	prefix=$1
	file=$2
	while IFS=$(printf '\t') read -r step seconds bytes rate notes
	do
		print_row "$size" "${prefix}${step}" "$seconds" "$bytes" "$rate" "$notes"
	done <"$file"
}

echo "scratch: $ROOT"
echo "sizes_mib: $SIZES"
echo "file_mib: $FILE_MIB"
echo "payload: urandom"
echo "image: dmg and tar.gz via bench-img-path.py"
echo

print_row "MiB" "step" "seconds" "out_bytes" "MiB/s" "notes"
print_row "--------" "----------------------" "----------" "------------" "----------" "------------"

for size in $SIZES
do
	work=$ROOT/s$size
	rm -rf "$work"
	mkdir -p "$work/mnt"
	python3 "$img_py" \
		--path "$work" \
		--type dmg \
		--mode write \
		--size-mib "$size" \
		--file-mib "$FILE_MIB" \
		--no-probe \
		--keep \
		--tsv >"$work/create.tsv"
	bench=$work/tiler-img-bench
	image=$bench/image.dmg
	if test ! -f "$image" && test -f "$image.dmg"
	then
		image=$image.dmg
	fi
	print_tsv "dmg/" "$work/create.tsv"

	python3 "$img_py" \
		--path "$work" \
		--type tar \
		--mode write \
		--src "$bench/data" \
		--no-probe \
		--keep \
		--tsv >"$work/tar-create.tsv"
	print_tsv "tar/" "$work/tar-create.tsv"
	archive=$bench/image.tar.gz

	purged=$(try_purge)
	sync
	# shellcheck disable=SC2046
	set -- $(read_tree "$bench/data")
	print_row "$size" "read_expanded_1" "$1" "$2" "$(mib_s "$1" "$2")" "purge=$purged"
	# shellcheck disable=SC2046
	set -- $(read_tree "$bench/data")
	print_row "$size" "read_expanded_2" "$1" "$2" "$(mib_s "$1" "$2")" "warm"

	python3 "$img_py" \
		--path "$work" \
		--type dmg \
		--mode read \
		--image "$image" \
		--mountpoint "$work/mnt" \
		--no-probe \
		--keep \
		--tsv >"$work/read.tsv"
	print_tsv "dmg/" "$work/read.tsv"

	python3 "$img_py" \
		--path "$work" \
		--type tar \
		--mode read \
		--image "$archive" \
		--no-probe \
		--keep \
		--tsv >"$work/tar-read.tsv"
	print_tsv "tar/" "$work/tar-read.tsv"

	rm -rf "$work"
	echo
done

echo "MiB/s is input size over wall time for pack steps, and bytes read over wall time for read steps."
echo "dmg/* and tar/* rows come from bench-img-path.py. read_expanded_* reads the unpacked tree."
echo "read_expanded_1 is cold only when purge=yes. read_*_2 is a second pass on a warm cache."
echo "tar/read_image_* streams member bytes out of image.tar.gz and does not extract onto --path."
