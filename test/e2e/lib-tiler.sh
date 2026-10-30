# Shared bootstrap for tiler shell tests. Source before using test helpers.
# Resolve paths before test-lib.sh changes cwd and HOME. Keep artifacts in
# e2e/ even when a script is invoked from another working directory.
TEST_DIRECTORY=$(cd "$(dirname "$0")" && pwd) || exit 1
TEST_LIB_DIRECTORY=$(cd "$TEST_DIRECTORY/../git-test-lib" && pwd) || exit 1
export TEST_DIRECTORY TEST_LIB_DIRECTORY

# These are CLI tests, not Git worktree tests. A case needing a repository
# must create its own explicitly instead of inheriting an unrelated one.
TEST_NO_CREATE_REPO=1
. "$TEST_LIB_DIRECTORY/test-lib.sh"

# Git's harness sanitizes Git variables; isolate tiler's inputs as well.
# Use a physical ceiling, including for cases that enter paths with cd -P.
sane_unset TILER_SESSION_ID SOURCE_DATE_EPOCH
TILER_CEILING_DIR=$(pwd -P) || exit 1
export TILER_CEILING_DIR
