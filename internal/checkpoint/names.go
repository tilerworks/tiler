package checkpoint

import (
	"errors"
	"path/filepath"
)

func safeName(name string) error {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return errors.New("invalid relative name")
	}
	return nil
}
