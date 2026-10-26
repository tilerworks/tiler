//go:build !darwin && !linux

package benchmark

func platformPurge() string {
	return "no"
}
