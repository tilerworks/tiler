//go:build !linux

package mountinfo

import "errors"

// Load reports that the host mount table cannot be read here. Other
// platforms only decide against injected tables and never open a
// host mount table.
func Load() ([]Mount, error) {
	return nil, errors.New("mountinfo: reading the host mount table requires linux")
}
