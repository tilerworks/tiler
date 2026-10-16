package doctor

import "github.com/jiangxin/tiler/internal/host"

// DefaultSeams returns the real LookPath, Stat, and Run
// implementations. Construction fails on platforms that have no
// host probe implementation. A missing tool is not a construction
// failure.
func DefaultSeams() (Seams, error) {
	if err := host.Available(); err != nil {
		return Seams{}, err
	}
	return Seams{
		LookPath: host.LookPath,
		Stat:     host.Stat,
		Run:      host.Run,
	}, nil
}
