//go:build !linux

package mountinfo

// Load reports that the host mount table cannot be read here. Other
// platforms only decide against injected tables and never open a
// host mount table.
func Load() ([]Mount, error) {
	return nil, ErrUnavailable
}
