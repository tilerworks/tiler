#!/bin/sh

test_description='directory checkpoints preserve and restore context'

# Reuse the caller's Go caches only when compiling the test driver.
tiler_build_home=$HOME

. "$(dirname "$0")/lib-tiler.sh"

checkpoint_prereqs=TILER,LINUX
if test_have_prereq MACOS
then
	checkpoint_prereqs=TILER,MACOS
fi

# These cases share the two-agent lifecycle fixture in their declared order.
test_expect_success "$checkpoint_prereqs" 'create checkpoints for two agent contexts' '
	for tiler_agent in agent-x agent-y
	do
		for tiler_part in skills rules plugins sessions
		do
			mkdir -p "$HOME/$tiler_agent/$tiler_part" &&
			printf "%s %s original\n" "$tiler_agent" "$tiler_part" >"$HOME/$tiler_agent/$tiler_part/example" || return 1
		done || return 1
	done &&
	cp -R "$HOME/agent-x" "$HOME/expected-x" &&
	cp -R "$HOME/agent-y" "$HOME/expected-y" &&
	(
		cd "$HOME" &&
		"$tiler_bin" checkpoint create agent-x agent-y >"$HOME/agents-create.out" 2>"$HOME/agents-create.err"
	) &&
	for tiler_agent in agent-x agent-y
	do
		printf "modified\n" >"$HOME/$tiler_agent/skills/example" &&
		rm "$HOME/$tiler_agent/rules/example" &&
		printf "added\n" >"$HOME/$tiler_agent/plugins/added" &&
		test_path_is_file "$HOME/$tiler_agent.checkpoint/.tiler-checkpoint" &&
		test_path_is_missing "$HOME/$tiler_agent/.tiler-checkpoint" &&
		for tiler_part in skills rules plugins sessions
		do
			test_cmp "$HOME/expected-${tiler_agent#agent-}/$tiler_part/example" "$HOME/$tiler_agent.checkpoint/$tiler_part/example" || return 1
		done &&
		test_path_is_missing "$HOME/$tiler_agent/rules/example" &&
		test_path_is_missing "$HOME/$tiler_agent.checkpoint/plugins/added" || return 1
	done
'

test_expect_success "$checkpoint_prereqs" 'repeated create preserves both changed contexts and checkpoints' '
	(
		cd "$HOME" &&
		"$tiler_bin" checkpoint create agent-x agent-y >"$HOME/agents-repeat.out" 2>"$HOME/agents-repeat.err"
	) &&
	test_grep -F "checkpoint already exists" "$HOME/agents-repeat.out" &&
	for tiler_agent in agent-x agent-y
	do
		printf "modified\n" >"$HOME/modified.expect" &&
		test_cmp "$HOME/modified.expect" "$HOME/$tiler_agent/skills/example" &&
		test_path_is_missing "$HOME/$tiler_agent/rules/example" &&
		test_path_is_file "$HOME/$tiler_agent/plugins/added" &&
		for tiler_part in skills rules plugins sessions
		do
			test_cmp "$HOME/expected-${tiler_agent#agent-}/$tiler_part/example" "$HOME/$tiler_agent.checkpoint/$tiler_part/example" || return 1
		done || return 1
	done
'

test_expect_success "$checkpoint_prereqs" 'restore both contexts and archive their changes at restore time' '
	date +%Y%m%d-%H%M%S >"$HOME/restore-before" &&
	(
		cd "$HOME" &&
		"$tiler_bin" checkpoint restore agent-x agent-y >"$HOME/agents-restore.out" 2>"$HOME/agents-restore.err"
	) &&
	date +%Y%m%d-%H%M%S >"$HOME/restore-after" &&
	for tiler_agent in agent-x agent-y
	do
		find "$HOME" -maxdepth 1 -name "$tiler_agent-????????-??????" >"$HOME/archive-list" &&
		test_line_count = 1 "$HOME/archive-list" &&
		tiler_archive=$(cat "$HOME/archive-list") &&
		tiler_stamp=${tiler_archive##*/$tiler_agent-} &&
		cat "$HOME/restore-before" >"$HOME/time-values" &&
		printf "%s\n" "$tiler_stamp" >>"$HOME/time-values" &&
		cat "$HOME/restore-after" >>"$HOME/time-values" &&
		LC_ALL=C sort "$HOME/time-values" >"$HOME/time-sorted" &&
		test_cmp "$HOME/time-values" "$HOME/time-sorted" &&
		for tiler_part in skills rules plugins sessions
		do
			test_cmp "$HOME/expected-${tiler_agent#agent-}/$tiler_part/example" "$HOME/$tiler_agent/$tiler_part/example" || return 1
		done &&
		printf "modified\n" >"$HOME/modified.expect" &&
		test_cmp "$HOME/modified.expect" "$tiler_archive/skills/example" &&
		test_path_is_missing "$tiler_archive/rules/example" &&
		printf "added\n" >"$HOME/added.expect" &&
		test_cmp "$HOME/added.expect" "$tiler_archive/plugins/added" &&
		test_cmp "$HOME/expected-${tiler_agent#agent-}/plugins/example" "$tiler_archive/plugins/example" &&
		test_cmp "$HOME/expected-${tiler_agent#agent-}/sessions/example" "$tiler_archive/sessions/example" &&
		test_path_is_missing "$HOME/$tiler_agent.checkpoint" &&
		test_path_is_missing "$HOME/$tiler_agent.checkpoint.lock" &&
		test_path_is_missing "$HOME/$tiler_agent/.tiler-checkpoint" || return 1
	done
'

test_expect_success "$checkpoint_prereqs" 'repeated restore warns for both missing checkpoints' '
	(
		cd "$HOME" &&
		"$tiler_bin" checkpoint restore agent-x agent-y >"$HOME/agents-repeat-restore.out" 2>"$HOME/agents-repeat-restore.err"
	) &&
	test_grep -F "warning: checkpoint missing for $HOME/agent-x" "$HOME/agents-repeat-restore.err" &&
	test_grep -F "warning: checkpoint missing for $HOME/agent-y" "$HOME/agents-repeat-restore.err" &&
	test_must_be_empty "$HOME/agents-repeat-restore.out"
'

test_expect_success "$checkpoint_prereqs" 'exchange and restore a checkpoint with an archive' '
	mkdir "$HOME/agents" &&
	printf "original\n" >"$HOME/agents/skill" &&
	ln "$HOME/agents/skill" "$HOME/held-skill" &&
	(
		cd "$HOME/agents" &&
		"$tiler_bin" checkpoint create "$HOME/agents" >../create.out 2>../create.err &&
		printf "through old directory\n" >held-directory
	) &&
	test_path_is_file "$HOME/agents.checkpoint/.tiler-checkpoint" &&
	test_path_is_missing "$HOME/agents/.tiler-checkpoint" &&
	test "$HOME/held-skill" -ef "$HOME/agents.checkpoint/skill" &&
	test_path_is_file "$HOME/agents.checkpoint/held-directory" &&
	test_path_is_missing "$HOME/agents/held-directory" &&
	printf "changed\n" >"$HOME/agents/skill" &&
	"$tiler_bin" checkpoint restore "$HOME/agents" >restore.out 2>restore.err &&
	printf "original\n" >expect &&
	test_cmp expect "$HOME/agents/skill" &&
	test "$HOME/held-skill" -ef "$HOME/agents/skill" &&
	test_path_is_missing "$HOME/agents.checkpoint" &&
	test_path_is_missing "$HOME/agents/.tiler-checkpoint" &&
	find "$HOME" -maxdepth 1 -name "agents-????????-??????" >agent-archives &&
	test_line_count = 1 agent-archives &&
	archive=$(cat agent-archives) &&
	printf "changed\n" >changed &&
	test_cmp changed "$archive/skill" &&
	test_grep -F "$HOME/agents" restore.out
'

test_expect_success "$checkpoint_prereqs" 'restore --rm removes the changed directory' '
	mkdir "$HOME/context" &&
	printf "before\n" >"$HOME/context/config" &&
	"$tiler_bin" checkpoint create "$HOME/context" >create-rm.out &&
	printf "after\n" >"$HOME/context/config" &&
	"$tiler_bin" checkpoint restore --rm "$HOME/context" >restore-rm.out &&
	printf "before\n" >expect-rm &&
	test_cmp expect-rm "$HOME/context/config" &&
	find "$HOME" -maxdepth 1 -name "context-????????-??????" >archives &&
	test_must_be_empty archives
'

test_expect_success TILER,LINUX 'multiple checkpoints correct suffixes and restore both directories' '
	mkdir "$HOME/multi-one" "$HOME/multi-two" &&
	printf "first\n" >"$HOME/multi-one/data" &&
	printf "second\n" >"$HOME/multi-two/data" &&
	"$tiler_bin" checkpoint create "$HOME/multi-one" "$HOME/multi-two" >multi-create.out 2>multi-create.err &&
	printf "changed\n" >"$HOME/multi-one/data" &&
	printf "changed\n" >"$HOME/multi-two/data" &&
	"$tiler_bin" checkpoint restore --rm "$HOME/multi-one.checkpoint" "$HOME/multi-two" >multi-restore.out 2>multi-restore.err &&
	test_grep -F warning multi-restore.err &&
	printf "first\n" >multi-first &&
	printf "second\n" >multi-second &&
	test_cmp multi-first "$HOME/multi-one/data" &&
	test_cmp multi-second "$HOME/multi-two/data" &&
	test_path_is_missing "$HOME/multi-one.checkpoint" &&
	test_path_is_missing "$HOME/multi-one.checkpoint.lock" &&
	test_path_is_missing "$HOME/multi-two.checkpoint" &&
	test_path_is_missing "$HOME/multi-two.checkpoint.lock" &&
	test_path_is_missing "$HOME/multi-one/.tiler-checkpoint" &&
	test_path_is_missing "$HOME/multi-two/.tiler-checkpoint"
'

# Compile a test-only driver; the production CLI has no fault injection switch.
test_lazy_prereq GO '
	command -v go >/dev/null 2>&1
'

test_expect_success "$checkpoint_prereqs,GO" 'build the interruption test driver' '
	(cd "$TEST_DIRECTORY/../.." && env HOME="$tiler_build_home" go test -c -o "$HOME/checkpoint-driver" ./internal/checkpoint)
'

test_expect_success "$checkpoint_prereqs,GO" 'recover create after a process stops after exchange' '
	mkdir "$HOME/interrupted-create" &&
	printf "original\n" >"$HOME/interrupted-create/data" &&
	test_expect_code 77 env TILER_CHECKPOINT_TEST_DRIVER=1 TILER_CHECKPOINT_TEST_OPERATION=create \
		TILER_CHECKPOINT_TEST_POINT=after:exchange TILER_CHECKPOINT_TEST_TARGET="$HOME/interrupted-create" \
		"$HOME/checkpoint-driver" -test.run=^TestCheckpointProcessInterruption$ >driver.out 2>driver.err &&
	"$tiler_bin" checkpoint create "$HOME/interrupted-create" >retry-create.out 2>retry-create.err &&
	printf "original\n" >original.expect &&
	test_cmp original.expect "$HOME/interrupted-create/data" &&
	test_cmp original.expect "$HOME/interrupted-create.checkpoint/data" &&
	test_path_is_file "$HOME/interrupted-create.checkpoint/.tiler-checkpoint"
'

for tiler_phase in reserve install archive cleanup marker
do
	test_expect_success "$checkpoint_prereqs,GO" "recover restore after process stops at $tiler_phase" '
		tiler_target="$HOME/interrupted-$tiler_phase" &&
		mkdir "$tiler_target" &&
		printf "original\n" >"$tiler_target/data" &&
		"$tiler_bin" checkpoint create "$tiler_target" >driver-create.out 2>driver-create.err &&
		printf "changed\n" >"$tiler_target/data" &&
		test_expect_code 77 env TILER_CHECKPOINT_TEST_DRIVER=1 TILER_CHECKPOINT_TEST_OPERATION=restore \
			TILER_CHECKPOINT_TEST_POINT="after:record:$tiler_phase" TILER_CHECKPOINT_TEST_TARGET="$tiler_target" \
			"$HOME/checkpoint-driver" -test.run=^TestCheckpointProcessInterruption$ >driver.out 2>driver.err &&
		if test "$tiler_phase" = install
		then
			find "$tiler_target.checkpoint" -mindepth 1 -print >placeholder-contents &&
			test_must_be_empty placeholder-contents &&
			test_path_is_file "$tiler_target.checkpoint.lock/.tiler-checkpoint" &&
			printf "original\n" >original.expect &&
			test_cmp original.expect "$tiler_target.checkpoint.lock/data"
		elif test "$tiler_phase" = archive
		then
			test_path_is_file "$tiler_target/.tiler-checkpoint" &&
			printf "original\n" >original.expect &&
			printf "changed\n" >changed.expect &&
			test_cmp original.expect "$tiler_target/data" &&
			test_cmp changed.expect "$tiler_target.checkpoint.lock/data"
		fi &&
		"$tiler_bin" checkpoint restore "$tiler_target" >driver-restore.out 2>driver-restore.err &&
		printf "original\n" >original.expect &&
		printf "changed\n" >changed.expect &&
		test_cmp original.expect "$tiler_target/data" &&
		find "$HOME" -maxdepth 1 -name "interrupted-$tiler_phase-????????-??????" >driver-archive &&
		test_line_count = 1 driver-archive &&
		tiler_archive=$(cat driver-archive) &&
		test_cmp changed.expect "$tiler_archive/data" &&
		test_path_is_missing "$tiler_target.checkpoint" &&
		test_path_is_missing "$tiler_target.checkpoint.lock" &&
		test_path_is_missing "$tiler_target/.tiler-checkpoint"
	'
done

test_expect_success "$checkpoint_prereqs,GO" 'resume a batch after the second restore was interrupted' '
	mkdir "$HOME/batch-first" "$HOME/batch-second" &&
	printf "first original\n" >"$HOME/batch-first/data" &&
	printf "second original\n" >"$HOME/batch-second/data" &&
	"$tiler_bin" checkpoint create "$HOME/batch-first" "$HOME/batch-second" >batch-create.out 2>batch-create.err &&
	printf "first changed\n" >"$HOME/batch-first/data" &&
	printf "second changed\n" >"$HOME/batch-second/data" &&
	"$tiler_bin" checkpoint restore "$HOME/batch-first" >batch-first.out 2>batch-first.err &&
	test_expect_code 77 env TILER_CHECKPOINT_TEST_DRIVER=1 TILER_CHECKPOINT_TEST_OPERATION=restore \
		TILER_CHECKPOINT_TEST_POINT=after:record:install TILER_CHECKPOINT_TEST_TARGET="$HOME/batch-second" \
		"$HOME/checkpoint-driver" -test.run=^TestCheckpointProcessInterruption$ >batch-driver.out 2>batch-driver.err &&
	"$tiler_bin" checkpoint restore "$HOME/batch-first" "$HOME/batch-second" >batch-retry.out 2>batch-retry.err &&
	test_grep -F "warning: checkpoint missing for $HOME/batch-first" batch-retry.err &&
	test_grep -F "restored $HOME/batch-second" batch-retry.out &&
	printf "first original\n" >first.expect &&
	printf "second original\n" >second.expect &&
	test_cmp first.expect "$HOME/batch-first/data" &&
	test_cmp second.expect "$HOME/batch-second/data"
'

test_done
