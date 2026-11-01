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

# Read the block records emitted by tiler's object metadata serializer.
workflow_block_manifest () {
	awk -v min_blocks="${2:-2}" '
	function emit() {
		if (name != sprintf("blk.%03d", count) || sizes != 1 || sums != 1 ||
		    size !~ /^[0-9]+$/ || length(sum) != 64 || sum ~ /[^0-9a-f]/) {
			bad = 1
			return
		}
		print name, size, sum
		count++
	}
	$1 == "blocks:" { sections++; blocks = 1; next }
	blocks && $1 == "-" && $2 == "name:" {
		if (seen++) emit()
		name = $3; size = sum = ""; sizes = sums = 0
		next
	}
	blocks && $1 == "size:" { size = $2; sizes++; next }
	blocks && $1 == "sha256:" { sum = $2; sums++; next }
	END {
		if (seen) emit()
		exit (bad || sections != 1 || count < min_blocks)
	}
	' "$1"
}
