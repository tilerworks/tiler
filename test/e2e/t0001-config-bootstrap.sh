#!/bin/sh
#
# tiler end-to-end test: config bootstrap and the doctor config report.
#
# Companion to t0000-doctor.sh. It covers two behaviors that need no
# FUSE toolchain, so they run wherever a tiler binary exists:
#
#   1. tiler creates $HOME/.tiler.yaml from a template on first run and
#      never rewrites an existing one.
#   2. `tiler doctor` reports the config file location and the core
#      runtime directories, in both the text and the --json form.
#
# The framework repoints HOME at a fresh per-run trash directory and
# exports it, so the auto-created file lands inside the sandbox and the
# real user home is never touched.

test_description='tiler config bootstrap and doctor config report'

# Resolve paths against this script's location BEFORE sourcing the
# framework: test-lib.sh moves the working directory into a per-run
# trash directory and repoints HOME there.
tiler_case_dir=$(cd "$(dirname "$0")" && pwd)
TEST_LIB_DIRECTORY=$(cd "$tiler_case_dir/../git-test-lib" && pwd)
export TEST_LIB_DIRECTORY
TEST_DIRECTORY=$tiler_case_dir
export TEST_DIRECTORY

. "$TEST_LIB_DIRECTORY/test-lib.sh"

# Prefer the repository build; fall back to a tiler found on PATH.
tiler_repo_root=$(cd "$tiler_case_dir/../.." && pwd)
tiler_bin=$tiler_repo_root/bin/tiler
if ! test -x "$tiler_bin"
then
	tiler_bin=$(command -v tiler) || tiler_bin=
fi
if test -n "$tiler_bin"
then
	test_set_prereq TILER
fi

test_expect_success TILER 'no user config exists before the first run' '
	test ! -e "$HOME/.tiler.yaml"
'

test_expect_success TILER 'tiler creates $HOME/.tiler.yaml from the template' '
	"$tiler_bin" version &&
	test -f "$HOME/.tiler.yaml" &&
	grep -q "^images_dir: ~/.tiler/images$" "$HOME/.tiler.yaml" &&
	grep -q "^sessions_dir: ~/.tiler/sessions" "$HOME/.tiler.yaml" &&
	grep -q "^cache_dir: ~/.tiler/cache$" "$HOME/.tiler.yaml" &&
	grep -q "^log_level: info$" "$HOME/.tiler.yaml" &&
	! grep -q "runtime_dir" "$HOME/.tiler.yaml"
'

test_expect_success TILER 'a later run leaves an edited config untouched' '
	echo "# user edit" >>"$HOME/.tiler.yaml" &&
	"$tiler_bin" version &&
	grep -q "^# user edit$" "$HOME/.tiler.yaml"
'

# `tiler doctor` exits 1 when a FUSE probe fails on an unequipped host,
# but it renders the config section and the probe report first. Capture
# the output and tolerate either verdict; the assertions are on content.
test_expect_success TILER 'doctor prints the config section as text' '
	tab=$(printf "\t") &&
	status=0 &&
	{ "$tiler_bin" doctor >doctor.txt 2>doctor.txt.err || status=$?; } &&
	{ test "$status" -eq 0 || test "$status" -eq 1; } &&
	grep -qF "config${tab}path${tab}$HOME/.tiler.yaml" doctor.txt &&
	grep -qF "config${tab}images_dir${tab}$HOME/.tiler/images" doctor.txt
'

test_expect_success TILER 'doctor --json reports the config location and dirs' '
	status=0 &&
	{ "$tiler_bin" doctor --json >doctor.json 2>doctor.json.err || status=$?; } &&
	{ test "$status" -eq 0 || test "$status" -eq 1; } &&
	grep -qF "\"path\": \"$HOME/.tiler.yaml\"" doctor.json &&
	grep -qF "\"images_dir\": \"$HOME/.tiler/images\"" doctor.json &&
	grep -qF "\"sessions_dir\": \"$HOME/.tiler/sessions\"" doctor.json &&
	grep -qF "\"log_level\": \"info\"" doctor.json &&
	! grep -qF "\"runtime_dir\"" doctor.json
'

test_done
