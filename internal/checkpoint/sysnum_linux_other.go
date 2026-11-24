//go:build linux && !arm64 && !amd64 && !386

package checkpoint

const renameat2Number = ^uintptr(0)
