package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/image"
	"github.com/spf13/cobra"
)

// importBlockedFlags are pack and description options. An import keeps
// the source payload and metadata, so these flags are usage errors.
var importBlockedFlags = []string{
	"full", "incremental", "split", "format", "compression", "message", "file",
}

// runBuildImport handles --link and --copy before description input,
// toolchain gates, and source-directory inference. The bool reports
// that this invocation is an import, including a usage failure.
func runBuildImport(cmd *cobra.Command, args []string, cfg config.Config, imageName, link, copySrc, hash string) (bool, error) {
	mode, source, handled, err := importSelection(cmd, args, imageName, link, copySrc)
	if err != nil || !handled {
		return handled, err
	}
	if err := rejectHashedFile(cmd, mode, imageName, source); err != nil {
		return true, err
	}
	req := image.ImportRequest{
		Mode: mode, ImagesDir: cfg.ImagesDir, Name: imageName, Source: source, Hash: hash,
		Warn: func(msg string) {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", msg)
		},
	}
	name, items, err := publishImport(req)
	if err != nil {
		return true, classifyImportError(err)
	}
	return true, writeImportLines(cmd.OutOrStdout(), mode, name, items)
}

func importSelection(cmd *cobra.Command, args []string, imageName, link, copySrc string) (image.ImportMode, string, bool, error) {
	linkSet := cmd.Flags().Changed("link")
	copySet := cmd.Flags().Changed("copy")
	if !linkSet && !copySet {
		if cmd.Flags().Changed("hash") {
			return "", "", true, usageError{errors.New("build: --hash requires --link or --copy")}
		}
		return "", "", false, nil
	}
	if linkSet && copySet {
		return "", "", true, usageError{fmt.Errorf(
			"build: --link %q and --copy %q are mutually exclusive", link, copySrc)}
	}
	mode, source, flag := image.ImportLink, link, "--link"
	if copySet {
		mode, source, flag = image.ImportCopy, copySrc, "--copy"
	}
	if err := rejectImportShape(cmd, args, imageName, mode, source, flag); err != nil {
		return "", "", true, err
	}
	return mode, source, true, nil
}

func rejectImportShape(cmd *cobra.Command, args []string, imageName string, mode image.ImportMode, source, flag string) error {
	if strings.TrimSpace(source) == "" {
		return usageError{fmt.Errorf("build: %s: image %s: %s requires a path", mode, imageName, flag)}
	}
	if !cmd.Flags().Changed("image") || strings.TrimSpace(imageName) == "" {
		return usageError{fmt.Errorf("build: %s: source %s: --image is required", mode, source)}
	}
	if len(args) > 0 {
		return usageError{fmt.Errorf(
			"build: %s: image %s: source %s: import does not accept a positional argument",
			mode, imageName, source)}
	}
	for _, name := range importBlockedFlags {
		if cmd.Flags().Changed(name) {
			return usageError{fmt.Errorf(
				"build: %s: image %s: source %s: --%s cannot be combined with import",
				mode, imageName, source, name)}
		}
	}
	return nil
}

// rejectHashedFile reports a usage error when --hash is present for a
// regular file. An explicit empty --hash still counts as present.
// A missing path stays a runtime error from the importer.
func rejectHashedFile(cmd *cobra.Command, mode image.ImportMode, name, source string) error {
	if !cmd.Flags().Changed("hash") {
		return nil
	}
	info, err := os.Stat(source)
	if err != nil || info.IsDir() || !info.Mode().IsRegular() {
		return nil
	}
	return usageError{fmt.Errorf(
		"build: %s: image %s: source %s: --hash applies only to an image directory",
		mode, name, source)}
}

func publishImport(req image.ImportRequest) (string, []image.ImportMergeItem, error) {
	switch req.Mode {
	case image.ImportLink:
		got, err := image.PublishLinkedImport(req)
		return got.Name, got.Items, err
	case image.ImportCopy:
		got, err := image.PublishCopiedImport(req)
		return got.Name, got.Items, err
	default:
		return "", nil, fmt.Errorf("build: unknown import mode %q", req.Mode)
	}
}

func classifyImportError(err error) error {
	if errors.Is(err, image.ErrInvalidName) || errors.Is(err, image.ErrInvalidHashPrefix) {
		return usageError{err}
	}
	if strings.Contains(err.Error(), "hash selector requires an image directory") {
		return usageError{err}
	}
	return err
}

func writeImportLines(w io.Writer, mode image.ImportMode, name string, items []image.ImportMergeItem) error {
	for _, item := range items {
		word, err := importResultWord(mode, item.Action)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "%s %s %s\n", word, name, item.Node.ID); err != nil {
			return err
		}
	}
	return nil
}

func importResultWord(mode image.ImportMode, action image.MergeAction) (string, error) {
	switch action {
	case image.MergeNoop, image.MergeRetain:
		return "no-op", nil
	case image.MergePromote:
		return "converted", nil
	case image.MergeAdd:
		return string(mode), nil
	default:
		return "", fmt.Errorf("build: unknown import result %q", action)
	}
}
