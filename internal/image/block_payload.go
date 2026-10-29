package image

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jiangxin/tiler/internal/container"
)

const (
	// logicalHeaderLen is the erofs superblock window. Squashfs magic
	// is the first four bytes of the same prefix.
	logicalHeaderLen = 1024 + 4
	// logicalTrailerLen is the UDIF trailer window.
	logicalTrailerLen = 512
)

// ProbeLogicalFormat reports the container type of an ordered block
// list. It reads a header window and a trailer window from the
// resolved payloads and does not assemble the logical image.
// Position-file text is not probed. A broken location names the block.
func ProbeLogicalFormat(blocks []BlockFile) (string, error) {
	sample, err := logicalSample(blocks)
	if err != nil {
		return "", err
	}
	return detectLogicalSample(sample)
}

func logicalSample(blocks []BlockFile) ([]byte, error) {
	if len(blocks) == 0 {
		return nil, errors.New("image: no blocks to probe")
	}
	header, err := readForward(blocks, logicalHeaderLen)
	if err != nil {
		return nil, err
	}
	if len(header) < logicalHeaderLen {
		return header, nil
	}
	trailer, err := readBackward(blocks, logicalTrailerLen)
	if err != nil {
		return nil, err
	}
	return append(header, trailer...), nil
}

func readForward(blocks []BlockFile, n int) ([]byte, error) {
	buf := make([]byte, 0, n)
	for _, block := range blocks {
		if len(buf) >= n {
			break
		}
		chunk, err := readBlockHead(block, n-len(buf))
		if err != nil {
			return nil, err
		}
		buf = append(buf, chunk...)
	}
	return buf, nil
}

func readBlockHead(block BlockFile, n int) ([]byte, error) {
	path, err := resolveBlockBytes(block)
	if err != nil {
		return nil, err
	}
	chunk, err := readFileWindow(path, 0, n)
	if err != nil {
		return nil, fmt.Errorf("block %s: %w", block.Name, err)
	}
	return chunk, nil
}

func readBackward(blocks []BlockFile, n int) ([]byte, error) {
	parts := make([][]byte, 0, 2)
	need := n
	for i := len(blocks) - 1; i >= 0 && need > 0; i-- {
		chunk, err := readBlockTail(blocks[i], need)
		if err != nil {
			return nil, err
		}
		if len(chunk) == 0 {
			continue
		}
		parts = append(parts, chunk)
		need -= len(chunk)
	}
	return joinReversed(parts), nil
}

func readBlockTail(block BlockFile, n int) ([]byte, error) {
	path, err := resolveBlockBytes(block)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("block %s: %w", block.Name, err)
	}
	take := n
	if int64(take) > info.Size() {
		take = int(info.Size())
	}
	if take == 0 {
		return nil, nil
	}
	chunk, err := readFileWindow(path, info.Size()-int64(take), take)
	if err != nil {
		return nil, fmt.Errorf("block %s: %w", block.Name, err)
	}
	return chunk, nil
}

func joinReversed(parts [][]byte) []byte {
	n := 0
	for _, part := range parts {
		n += len(part)
	}
	out := make([]byte, 0, n)
	for i := len(parts) - 1; i >= 0; i-- {
		out = append(out, parts[i]...)
	}
	return out
}

func readFileWindow(path string, off int64, n int) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	if off > 0 {
		if _, err = file.Seek(off, io.SeekStart); err != nil {
			return nil, err
		}
	}
	buf := make([]byte, n)
	got, err := io.ReadFull(file, buf)
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return buf[:got], nil
	}
	if err != nil {
		return nil, err
	}
	return buf, nil
}

func detectLogicalSample(sample []byte) (string, error) {
	file, err := os.CreateTemp("", "tiler-block-probe-*")
	if err != nil {
		return "", err
	}
	name := file.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err = file.Write(sample); err != nil {
		_ = file.Close()
		return "", dropDeletedProbePath(err, name)
	}
	if err = file.Close(); err != nil {
		return "", dropDeletedProbePath(err, name)
	}
	got, err := container.DetectFile(name)
	if err == nil {
		return got.Type(), nil
	}
	// dmg is not registered on Linux. The trailer window is the same
	// koly check a single file uses, so a split UDIF image is still
	// identified and the host policy can refuse it later.
	ok, kerr := container.ReadKolyTrailer(name)
	if kerr != nil {
		return "", fmt.Errorf("logical format: %w", dropDeletedProbePath(kerr, name))
	}
	if ok {
		return container.DMG().Type(), nil
	}
	if errors.Is(err, container.ErrNotDetected) {
		return "", fmt.Errorf("logical format: %w", container.ErrNotDetected)
	}
	return "", fmt.Errorf("logical format: %w", dropDeletedProbePath(err, name))
}

// dropDeletedProbePath removes the probe file from an error. Callers
// see the error only after that temp file has been removed.
func dropDeletedProbePath(err error, path string) error {
	if err == nil {
		return nil
	}
	var pe *os.PathError
	if errors.As(err, &pe) && pe.Path == path && pe.Err != nil {
		return pe.Err
	}
	if errors.Is(err, container.ErrAmbiguous) {
		return container.ErrAmbiguous
	}
	if path != "" && strings.Contains(err.Error(), path) {
		return errors.New(strings.ReplaceAll(err.Error(), path, "probe"))
	}
	return err
}
