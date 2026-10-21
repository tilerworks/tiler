package container

import (
	"errors"
	"strings"
)

// ErrNoToolchain means no selectable format has every tool available.
var ErrNoToolchain = errors.New("no complete image toolchain")

// Stack is one registered format and the tools look could not resolve.
type Stack struct {
	Format  Format
	Missing []string
}

// Complete reports whether every tool of the format was found.
func (s Stack) Complete() bool { return len(s.Missing) == 0 }

// Stacks reports each selectable image format in registration order.
// dmg stays out of this list. Darwin doctor passes image_toolchain
// from /usr/bin/hdiutil itself, and InferType keeps the FUSE stacks
// so Linux does not start requiring hdiutil. look resolves a binary
// name the way exec.LookPath does. A nil look treats every tool as
// missing.
func Stacks(look func(string) (string, error)) []Stack {
	formats := All()
	out := make([]Stack, 0, len(formats))
	for _, format := range formats {
		if !selectableStack(format) {
			continue
		}
		out = append(out, Stack{
			Format:  format,
			Missing: MissingTools(format, look),
		})
	}
	return out
}

// selectableStack reports whether format may be inferred or treated
// as a Linux image toolchain. dmg is omitted so a working hdiutil
// is not a FUSE stack. Darwin doctor probes that binary on its own.
func selectableStack(format Format) bool {
	return format.Type() != dmgType
}

// MissingTools returns the Tools of format that look cannot resolve,
// in Tools order. A nil look reports every tool missing.
func MissingTools(format Format, look func(string) (string, error)) []string {
	var missing []string
	for _, tool := range format.Tools() {
		if look == nil {
			missing = append(missing, tool)
			continue
		}
		if _, err := look(tool); err != nil {
			missing = append(missing, tool)
		}
	}
	return missing
}

// AnyComplete reports whether at least one selectable format has
// every tool available.
func AnyComplete(look func(string) (string, error)) bool {
	for _, stack := range Stacks(look) {
		if stack.Complete() {
			return true
		}
	}
	return false
}

// InferType chooses the container type from installed tools.
// A complete squashfs stack wins, including when another stack is
// also complete, so a dual-stack host stays on squashfs. Otherwise
// the first complete format in registration order is used. No
// complete stack returns ErrNoToolchain.
func InferType(look func(string) (string, error)) (string, error) {
	var fallback string
	for _, stack := range Stacks(look) {
		if !stack.Complete() {
			continue
		}
		if stack.Format.Type() == squashfsType {
			return squashfsType, nil
		}
		if fallback == "" {
			fallback = stack.Format.Type()
		}
	}
	if fallback == "" {
		return "", ErrNoToolchain
	}
	return fallback, nil
}

// IncompleteDetail joins the stacks that are not complete, for
// doctor and command errors. An empty result means every stack is
// complete, or none are registered.
func IncompleteDetail(stacks []Stack) string {
	var gaps []string
	for _, stack := range stacks {
		if stack.Complete() {
			continue
		}
		gaps = append(gaps, stack.Format.Type()+" missing "+strings.Join(stack.Missing, ", "))
	}
	return strings.Join(gaps, "; ")
}
