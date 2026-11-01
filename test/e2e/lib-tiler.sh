# Shared bootstrap for tiler shell tests. Source before using test helpers.
# Resolve paths before test-lib.sh changes cwd and HOME. Keep artifacts in
# e2e/ even when a script is invoked from another working directory.
TEST_DIRECTORY=$(cd "$(dirname "$0")" && pwd) || exit 1
TEST_LIB_DIRECTORY=$(cd "$TEST_DIRECTORY/../git-test-lib" && pwd) || exit 1
export TEST_DIRECTORY TEST_LIB_DIRECTORY

# Keep the harness-created Git repository at the sandbox root. The trash
# directory lives under tiler's source checkout, so Git commands must stop
# here instead of discovering and modifying the project's own repository.
# Clear inherited opt-outs too; scenarios may still create nested repos.
unset TEST_NO_CREATE_REPO
. "$TEST_LIB_DIRECTORY/test-lib.sh"

# Git's harness sanitizes Git variables; isolate tiler's inputs as well.
# Use a physical ceiling, including for cases that enter paths with cd -P.
sane_unset TILER_SESSION_ID SOURCE_DATE_EPOCH
TILER_CEILING_DIR=$(pwd -P) || exit 1
export TILER_CEILING_DIR
