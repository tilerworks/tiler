#!/bin/sh

test_description='source versions are fixed at build time'

. "$(dirname "$0")/lib-tiler.sh"

version_script=$tiler_repo_root/scripts/version.sh

test_lazy_prereq VERSION_BUILD '
	command -v go >/dev/null 2>&1 &&
	command -v make >/dev/null 2>&1
'

copy_build_source () {
	mkdir -p "$1" &&
	for tiler_source in Makefile CHANGELOG.md go.mod go.sum cmd internal scripts
	do
		cp -R "$tiler_repo_root/$tiler_source" "$1/" || return 1
	done
}

build_source () (
	GOCACHE=$(HOME="$USER_HOME" go env GOCACHE) &&
	GOMODCACHE=$(HOME="$USER_HOME" go env GOMODCACHE) &&
	export GOCACHE GOMODCACHE &&
	make -C "$1" build
)

test_expect_success 'a source archive inside a repository uses its own changelog' '
	mkdir -p archive &&
	cat >archive/CHANGELOG.md <<-\EOF &&
	# Changelog
	## [Unreleased]
	## [0.1.0] - Unreleased
	## [0.0.9] - 2026-10-01
	EOF
	printf "0.1.0.change\n" >expect &&
	sh "$version_script" "$HOME/archive" >actual 2>err &&
	test_cmp expect actual &&
	test_must_be_empty err
'

test_expect_success 'archive parsing preserves prerelease and build metadata' '
	mkdir -p prerelease &&
	printf "## [0.2.0-rc.1+build.2] - Unreleased\n" >prerelease/CHANGELOG.md &&
	printf "0.2.0-rc.1+build.2.change\n" >expect &&
	sh "$version_script" "$HOME/prerelease" >actual &&
	test_cmp expect actual
'

for tiler_invalid in missing invalid
do
	test_expect_success "archive version detection rejects $tiler_invalid changelog input" '
		mkdir -p "$tiler_invalid" &&
		if test "$tiler_invalid" = invalid
		then
			printf "# Changelog\n## [Unreleased]\n## [not-a-version]\n" >"$tiler_invalid/CHANGELOG.md"
		fi &&
		test_expect_code 1 sh "$version_script" "$HOME/$tiler_invalid" >actual 2>err &&
		test_must_be_empty actual &&
		test_grep "version:.*CHANGELOG.md" err
	'
done

test_expect_success 'an untagged source repository uses its commit hash' '
	git init --initial-branch=main git-source &&
	printf "initial\n" >git-source/tracked &&
	git -C git-source add tracked &&
	git -C git-source commit -m initial &&
	git -C git-source describe --always --dirty >expect &&
	sh "$version_script" "$HOME/git-source" >actual 2>err &&
	test_cmp expect actual &&
	test_must_be_empty err
'

test_expect_success 'an untagged source repository retains the dirty marker' '
	test_when_finished '\''git -C git-source checkout -- tracked'\'' &&
	printf "pending untagged edit\n" >>git-source/tracked &&
	git -C git-source describe --always --dirty >expect &&
	sh "$version_script" "$HOME/git-source" >actual &&
	test_cmp expect actual &&
	test_grep -E "^[0-9a-f]+-dirty$" actual
'

test_expect_success 'an annotated release tag is preserved verbatim' '
	git -C git-source tag -a -m release v0.1.0 &&
	printf "v0.1.0\n" >expect &&
	sh "$version_script" "$HOME/git-source" >actual &&
	test_cmp expect actual
'

test_expect_success 'commits after the tag retain the describe distance and hash' '
	printf "next\n" >>git-source/tracked &&
	git -C git-source commit -a -m next &&
	git -C git-source describe --dirty >expect &&
	sh "$version_script" "$HOME/git-source" >actual &&
	test_cmp expect actual &&
	test_grep -E "^v0.1.0-1-g[0-9a-f]+$" actual
'

test_expect_success 'tracked changes retain the dirty marker' '
	printf "pending\n" >>git-source/tracked &&
	git -C git-source describe --dirty >expect &&
	sh "$version_script" "$HOME/git-source" >actual &&
	test_cmp expect actual &&
	test_grep -E "^v0.1.0-1-g[0-9a-f]+-dirty$" actual
'

test_expect_success 'a Git worktree metadata file selects the Git version' '
	git -C git-source worktree add --detach "$HOME/linked-source" HEAD &&
	test_when_finished '\''git -C git-source worktree remove --force "$HOME/linked-source"'\'' &&
	test_path_is_file linked-source/.git &&
	git -C linked-source describe --dirty >expect &&
	sh "$version_script" "$HOME/linked-source" >actual &&
	test_cmp expect actual
'

test_expect_success 'inherited Git locations cannot redirect source version discovery' '
	git -C git-source describe --dirty >expect &&
	env GIT_DIR="$HOME/.git" GIT_WORK_TREE="$HOME" sh "$version_script" "$HOME/git-source" >actual &&
	test_cmp expect actual
'

test_expect_success 'a source repository without HEAD fails rather than using the changelog' '
	git init --initial-branch=main empty-source &&
	cp archive/CHANGELOG.md empty-source/CHANGELOG.md &&
	test_expect_code 1 sh "$version_script" "$HOME/empty-source" >actual 2>err &&
	test_must_be_empty actual &&
	test_grep "version: cannot describe" err
'

test_expect_success 'Git sources require git but archives do not' '
	mkdir no-git &&
	awk_path=$(command -v awk) &&
	ln -s "$awk_path" no-git/awk &&
	test_expect_code 1 env PATH="$HOME/no-git" /bin/sh "$version_script" "$HOME/git-source" >actual 2>err &&
	test_must_be_empty actual &&
	test_grep "version: Git is required" err &&
	printf "0.1.0.change\n" >expect &&
	env PATH="$HOME/no-git" /bin/sh "$version_script" "$HOME/archive" >actual 2>err &&
	test_cmp expect actual &&
	test_must_be_empty err
'

test_expect_success TILER 'both version entrypoints work without creating configuration' '
	"$tiler_bin" version >subcommand.out 2>subcommand.err &&
	"$tiler_bin" --version >flag.out 2>flag.err &&
	test_cmp subcommand.out flag.out &&
	test_grep -E "^tiler [^ ]+$" subcommand.out &&
	test_line_count = 1 subcommand.out &&
	test_must_be_empty subcommand.err &&
	test_must_be_empty flag.err &&
	test_path_is_missing "$HOME/.tiler.yaml"
'

# These builds use the working source files, including files not committed yet.
# Reuse the caller's Go caches rather than downloading dependencies per sandbox.
test_expect_success VERSION_BUILD 'an archive build embeds the changelog version and survives relocation' '
	copy_build_source "$HOME/build-archive" &&
	build_source build-archive >archive-build.out 2>archive-build.err &&
	mv build-archive/bin/tiler "$HOME/archive-tiler" &&
	mkdir runtime &&
	printf "invalid: [\n" >runtime/.tiler.yaml &&
	printf "tiler 0.1.0.change\n" >expect &&
	(
		cd runtime &&
		"$HOME/archive-tiler" version >"$HOME/archive-version.out" 2>"$HOME/archive-version.err" &&
		"$HOME/archive-tiler" --version >"$HOME/archive-flag.out" 2>"$HOME/archive-flag.err"
	) &&
	test_cmp expect archive-version.out &&
	test_cmp expect archive-flag.out &&
	test_must_be_empty archive-version.err &&
	test_must_be_empty archive-flag.err &&
	test_path_is_missing "$HOME/.tiler.yaml" &&
	go version -m "$HOME/archive-tiler" >build-info &&
	test_grep "CGO_ENABLED=0" build-info
'

test_expect_success VERSION_BUILD 'a Git build embeds describe output and survives source removal' '
	copy_build_source "$HOME/build-git" &&
	git -C build-git init --initial-branch=main &&
	git -C build-git add Makefile CHANGELOG.md go.mod go.sum cmd internal scripts &&
	git -C build-git commit -m source &&
	git -C build-git tag -a -m release v0.1.0 &&
	build_source build-git >git-build.out 2>git-build.err &&
	mv build-git/bin/tiler "$HOME/git-tiler" &&
	rm -rf build-git &&
	printf "tiler v0.1.0\n" >expect &&
	"$HOME/git-tiler" version >git-version.out &&
	"$HOME/git-tiler" --version >git-flag.out &&
	test_cmp expect git-version.out &&
	test_cmp expect git-flag.out
'

test_expect_success VERSION_BUILD 'version resolution failure stops make before replacing an existing binary' '
	printf "# no release version\n" >build-archive/CHANGELOG.md &&
	cp "$HOME/archive-tiler" build-archive/bin/tiler &&
	test_expect_code 2 build_source build-archive >failed-build.out 2>failed-build.err &&
	test_grep "version: no valid version heading" failed-build.err &&
	test_cmp "$HOME/archive-tiler" build-archive/bin/tiler
'

test_done
