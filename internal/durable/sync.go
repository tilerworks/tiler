// Package durable provides the file synchronization boundary.
package durable

// File is a file or a test double that supports synchronization.
type File interface {
	Sync() error
}

// Sync flushes a file using its full durability guarantee. Tests may
// replace this before starting any goroutines; production always uses
// File.Sync, including the hardware cache flush on macOS.
var Sync = func(file File) error { return file.Sync() }
