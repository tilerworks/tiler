//go:build !unix

package benchmark

func dirWritable(string) bool {
	return false
}
