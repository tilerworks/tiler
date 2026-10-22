//go:build darwin

package container

import "os/exec"

// HdiutilPath is the system disk-image tool. Constructors stay free
// of a build tag. Tests record argument vectors and do not exec
// this path.
const HdiutilPath = "/usr/bin/hdiutil"

// NewHdiutilCommand returns an unstarted command for a constructor
// vector. consErr is returned without building a command, so a
// failed constructor is not executed. The caller starts the process.
func NewHdiutilCommand(argv []string, consErr error) (*exec.Cmd, error) {
	args, err := hdiutilArgs(argv, consErr)
	if err != nil {
		return nil, err
	}
	return exec.Command(HdiutilPath, args...), nil
}
