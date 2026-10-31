#!/bin/sh

test_description='the sandbox repository contains Git discovery'

. "$(dirname "$0")/lib-tiler.sh"

test_expect_success 'bootstrap creates a repository at the physical sandbox root' '
	test_path_is_dir "$HOME/.git" &&
	sandbox_root=$(pwd -P) &&
	printf "%s\n" "$sandbox_root" >expect-root &&
	git rev-parse --show-toplevel >actual-root &&
	test_cmp expect-root actual-root
'

test_expect_success 'Git discovery stays in the sandbox without a ceiling' '
	mkdir -p "$HOME/nested/deep" &&
	(
		cd "$HOME/nested/deep" &&
		GIT_CEILING_DIRECTORIES= git rev-parse --show-toplevel
	) >nested-root &&
	test_cmp expect-root nested-root
'

test_expect_success 'bootstrap isolates tiler inputs and uses the physical config ceiling' '
	test -z "${TILER_SESSION_ID+x}" &&
	test -z "${SOURCE_DATE_EPOCH+x}" &&
	test "$TILER_CEILING_DIR" = "$sandbox_root"
'

test_done
