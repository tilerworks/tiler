package doctor

import (
	"fmt"
	"io"
	"strings"
)

// Write renders one tab-separated line per probe. The detail is
// flattened so a host error cannot break the line into extra rows.
func Write(w io.Writer, results []Result) error {
	for _, result := range results {
		_, err := fmt.Fprintf(w, "%s\t%s\t%s\n", result.Name, result.Status, result.Detail)
		if err != nil {
			return err
		}
	}
	return nil
}

func cleanDetail(detail string) string {
	detail = strings.ReplaceAll(detail, "\t", " ")
	detail = strings.ReplaceAll(detail, "\r", " ")
	detail = strings.ReplaceAll(detail, "\n", " ")
	return strings.TrimSpace(detail)
}
