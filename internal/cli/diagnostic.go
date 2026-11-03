package cli

import (
	"fmt"
	"io"

	"github.com/jiangxin/tiler/internal/diagnostic"
)

// writeWarning keeps warnings off stdout and prevents dynamic values from
// introducing terminal controls or extra diagnostic lines. Newlines in the
// format itself are intentional layout, such as a separate recovery hint.
func writeWarning(w io.Writer, command, format string, args ...any) (int, error) {
	values := make([]any, len(args))
	for i, arg := range args {
		switch value := arg.(type) {
		case string:
			values[i] = terminalText(value)
		case error:
			values[i] = diagnostic.ErrorText(value)
		default:
			values[i] = arg
		}
	}
	return fmt.Fprintf(w, "Warning: "+command+": "+format, values...)
}
