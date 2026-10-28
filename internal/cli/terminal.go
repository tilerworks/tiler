package cli

import (
	"io"
	"os"
)

// inputTerminal reports whether r is a terminal a user can answer on. Only an
// open character device qualifies: a byte buffer, a pipe wrapper, a pipe, and
// a regular file never are, so a redirected standard input is never treated as
// interactive. The null device is a character device that never answers, so it
// is refused as well.
//
// It is a variable, so tests replace the check instead of allocating a
// terminal. The description editor and the gc confirmation share it, so both
// decide interactivity the same way.
var inputTerminal = func(r io.Reader) bool {
	file, ok := r.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	null, err := os.Stat(os.DevNull)
	return err != nil || !os.SameFile(info, null)
}
