//go:build !linux

package workspace

func cleanRebaseTransits(Request, runFunc, hooks) error { return nil }

func (w *Workspace) rebaseUpper(Request, []string, hooks) error {
	return ErrUnavailable
}
