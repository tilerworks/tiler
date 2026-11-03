// Package diagnostic formats actionable tool failures and terminal-safe text.
package diagnostic

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode"
)

// Text escapes control characters in dynamic terminal output.
func Text(text string) string {
	var out strings.Builder
	for _, r := range text {
		if unicode.IsControl(r) {
			fmt.Fprintf(&out, "\\u%04x", r)
		} else {
			out.WriteRune(r)
		}
	}
	return out.String()
}

// MountDeniedError retains the original mount failure for error matching.
type MountDeniedError struct {
	context   string
	cause     error
	confirmed bool
}

func (e *MountDeniedError) Unwrap() error { return e.cause }

// TerminalDiagnostic returns a trusted layout with escaped dynamic content.
func (e *MountDeniedError) TerminalDiagnostic() string { return e.Error() }

func (e *MountDeniedError) Error() string {
	detail := e.cause.Error()
	if start := strings.Index(detail, "fusermount3:"); start >= 0 {
		detail = detail[start:]
	}
	lines := strings.Split(strings.TrimSpace(detail), "\n")
	for i := range lines {
		lines[i] = Text(strings.TrimSpace(lines[i]))
	}
	context := e.context
	if !e.confirmed {
		context = strings.ReplaceAll(context, "was denied.", "could not complete.")
	}
	return context + "\n  " + strings.Join(lines, "\n  ") + `
  Hint: check that the source image and its parent directories are readable.
        Check /dev/fuse access, fusermount3 permissions, and security policy.
        In a container, also check FUSE device access and CAP_SYS_ADMIN.
        For Docker, start the container with:
          --device /dev/fuse --cap-add SYS_ADMIN
        If permissions are already configured, check seccomp/AppArmor/SELinux
        restrictions on FUSE mounts.`
}

// DeniedMount recognizes typed permission errors first, then tool output.
// Call it only for failed FUSE mount commands.
func DeniedMount(context string, err error) *MountDeniedError {
	message := strings.ToLower(err.Error())
	if !errors.Is(err, os.ErrPermission) && !strings.Contains(message, "operation not permitted") && !strings.Contains(message, "permission denied") {
		return nil
	}
	confirmed := false
	for _, line := range strings.Split(message, "\n") {
		if strings.Contains(line, "mount failed:") &&
			(strings.Contains(line, "operation not permitted") || strings.Contains(line, "permission denied")) {
			confirmed = true
		}
	}
	return &MountDeniedError{context: context, cause: err, confirmed: confirmed}
}

// ErrorText preserves explicitly trusted diagnostic layouts, even when wrapped.
// Other error text is treated as dynamic data and escaped in full.
func ErrorText(err error) string {
	var trusted interface{ TerminalDiagnostic() string }
	if errors.As(err, &trusted) {
		if before, after, ok := strings.Cut(err.Error(), trusted.TerminalDiagnostic()); ok {
			return Text(before) + trusted.TerminalDiagnostic() + Text(after)
		}
	}
	return Text(err.Error())
}
