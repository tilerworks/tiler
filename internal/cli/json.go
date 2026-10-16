package cli

import (
	"encoding/json"
	"io"
)

// writeJSON renders one machine-readable document: two-space
// indented JSON with a trailing newline. HTML escaping is disabled
// so details and paths keep & and < characters byte-stable for the
// golden-file contract tests.
func writeJSON(w io.Writer, v any) error {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(v)
}
