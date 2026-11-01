#!/bin/sh

# Resolve the source version before compilation, never from the installed
# binary's working directory. An archive inside another repository is still
# an archive: only Git metadata at the source root makes this a Git build.
set -eu

if test "$#" -gt 1
then
	printf '%s\n' 'usage: version.sh [source-directory]' >&2
	exit 1
fi
source_dir=$(cd "${1:-$(dirname "$0")/..}" && pwd -P)

if test -d "$source_dir/.git" || test -f "$source_dir/.git"
then
	# Inherited Git locations must not redirect version discovery elsewhere.
	unset GIT_DIR GIT_WORK_TREE GIT_COMMON_DIR GIT_INDEX_FILE
	unset GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES
	if ! command -v git >/dev/null 2>&1
	then
		printf '%s\n' 'version: Git is required for a Git source tree' >&2
		exit 1
	fi
	git_root=$(git -C "$source_dir" rev-parse --show-toplevel) || exit 1
	git_root=$(cd "$git_root" && pwd -P)
	if test "$git_root" != "$source_dir"
	then
		printf '%s\n' 'version: Git worktree does not match the source root' >&2
		exit 1
	fi
	if version=$(git -C "$source_dir" describe --dirty 2>/dev/null)
	then
		:
	elif version=$(git -C "$source_dir" describe --always --dirty)
	then
		:
	else
		printf '%s\n' 'version: cannot describe the Git source tree' >&2
		exit 1
	fi
else
	changelog=$source_dir/CHANGELOG.md
	if ! test -f "$changelog"
	then
		printf 'version: missing %s\n' "$changelog" >&2
		exit 1
	fi
	version=$(awk '
	/^## \[[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?\]([[:space:]]|$)/ {
		version = $2
		sub(/^\[/, "", version)
		sub(/\]$/, "", version)
		print version ".change"
		found = 1
		exit
	}
	END { if (!found) exit 1 }
	' "$changelog") || {
		printf 'version: no valid version heading in %s\n' "$changelog" >&2
		exit 1
	}
fi

printf '%s\n' "$version"
