# concatfs — concat view of a byte-block directory

Upstream: https://github.com/schlaile/concatfs (cloned 2026-10-16)
Copyright: 2015 Peter Schlaile (peter at schlaile dot de)
License: GPL-2.0 (see LICENSE)

concatfs mounts a directory and presents every file whose name
contains `-concat-` as the concatenation of the files listed inside
it. tiler uses it for the optional direct-mount route of the
byte-block split form: a mount writes a small runtime
`image.sqsh-concat-view` description listing resolved block paths in order,
and concatfs turns that description into one
virtual image file that squashfuse_ll mounts directly — no physical
reassembly and no extra cache space.

## Local adaptation

`concatfs.c` is the upstream source adapted for libfuse3; the
adaptation is recorded in full in `concatfs-fuse3.patch`:
FUSE_USE_VERSION 26 -> 31, fuse_file_info parameters added to
getattr/truncate/chmod/chown, rename gains the flags argument,
readdir gains fuse_readdir_flags, the fuse3-removed utime/access ops
are dropped, filler calls take five arguments, and <utime.h> is
included explicitly.

## Build

Needs gcc and the fuse3 development package (pkg-config fuse3):

    gcc -O2 -Wno-unused-function concatfs.c \
        $(pkg-config fuse3 --cflags --libs) -o concatfs

Put the result anywhere on PATH. Linux tiler mounts automatically use it
for split full images and incremental layers, including linked imports
and session reopen. Each layer gets a read-only concatfs transit mount
and a small runtime descriptor with resolved absolute block paths. No
complete cache image is created by mounting. Without concatfs, tiler
reassembles verified blocks in the cache. Build quality checks can still
reassemble layers. There is no route configuration key or mount flag;
tiler never compiles or installs this helper automatically.
