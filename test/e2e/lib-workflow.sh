# Fixture setup shared by the Linux workflow cases. Source after lib-tiler.sh.

test_lazy_prereq WORKFLOW_GIT '
	command -v git >/dev/null 2>&1
'

# The CLI checks the format client even when packing without a mount.
test_lazy_prereq WORKFLOW_SQUASHFS_CLIENT '
	command -v squashfuse_ll >/dev/null 2>&1
'

workflow_config () {
	mkdir -p "$1" &&
	cat >"$1/.tiler.yaml" <<-\EOF
	version: 1
	images_dir: .tiler/images
	sessions_dir: .tiler/sessions
	cache_dir: .tiler/cache
	logs_dir: .tiler/logs
	EOF
}

workflow_git_init () {
	git -C "$1" init --initial-branch=main &&
	git -C "$1" config user.name "Workflow Test" &&
	git -C "$1" config user.email "workflow@example.com" &&
	git -C "$1" config commit.gpgsign false &&
	git -C "$1" config tag.gpgsign false
}
