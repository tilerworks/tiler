package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"unicode"

	"github.com/jiangxin/tiler/internal/image"
	"github.com/spf13/cobra"
)

var descriptionTerminal = func(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
var runDescriptionEditor = func(ctx context.Context, name string, args []string,
	in io.Reader, out, stderr io.Writer) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdin, command.Stdout, command.Stderr = in, out, stderr
	return command.Run()
}

func editDescription(cmd *cobra.Command, current string) (string, error) {
	if !descriptionTerminal(cmd.InOrStdin()) {
		return "", usageError{errors.New("images describe: no message input on non-interactive terminal; use --message or --file")}
	}
	editor := os.Getenv("EDITOR")
	if strings.TrimSpace(editor) == "" {
		var err error
		if editor, err = exec.LookPath("vi"); err != nil {
			return "", errors.New("images describe: set EDITOR or use --message or --file")
		}
	}
	argv, err := splitEditor(editor)
	if err != nil {
		return "", fmt.Errorf("images describe: EDITOR: %w", err)
	}
	f, err := os.CreateTemp("", "tiler-description-*")
	if err != nil {
		return "", fmt.Errorf("images describe: create editor file: %w", err)
	}
	path := f.Name()
	defer os.Remove(path) //nolint:errcheck // Best-effort cleanup of a private temporary file.
	if _, err = f.WriteString(current); err == nil {
		err = f.Close()
	}
	if err != nil {
		return "", fmt.Errorf("images describe: write editor file: %w", err)
	}
	if err = runDescriptionEditor(cmd.Context(), argv[0], append(argv[1:], path),
		cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr()); err != nil {
		return "", fmt.Errorf("images describe: editor: %w", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("images describe: read editor file: %w", err)
	}
	text, err := image.NormalizeDescription(string(body))
	if err != nil {
		return "", usageError{fmt.Errorf("images describe: description: %w", err)}
	}
	return text, nil
}
func splitEditor(value string) ([]string, error) {
	var words []string
	var word strings.Builder
	var quote rune
	escaped, started := false, false
	for _, r := range value {
		switch {
		case escaped:
			word.WriteRune(r)
			escaped, started = false, true
		case r == '\\' && quote != '\'':
			escaped, started = true, true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				word.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, started = r, true
		case unicode.IsSpace(r):
			if started {
				words = append(words, word.String())
				word.Reset()
				started = false
			}
		default:
			word.WriteRune(r)
			started = true
		}
	}
	if escaped || quote != 0 {
		return nil, errors.New("unfinished quote or escape")
	}
	if started {
		words = append(words, word.String())
	}
	if len(words) == 0 {
		return nil, errors.New("empty command")
	}
	return words, nil
}
