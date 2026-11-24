package checkpoint

import (
	"errors"
	"fmt"
)

// mountSnapshot retries a full buffer because the host mount table may grow
// between the count and read calls. A continuously changing table fails closed.
func mountSnapshot[T any](read func([]T) (int, error)) ([]T, error) {
	count, err := read(nil)
	if err != nil {
		return nil, err
	}
	const maxEntries = 1 << 20
	for attempt := 0; attempt < 8; attempt++ {
		if count < 0 || count >= maxEntries {
			return nil, errors.New("invalid mount table size")
		}
		buf := make([]T, count+1)
		n, err := read(buf)
		if err != nil {
			return nil, err
		}
		if n < 0 || n > maxEntries {
			return nil, errors.New("invalid mount table size")
		}
		if n < len(buf) {
			return buf[:n], nil
		}
		count = n
	}
	return nil, fmt.Errorf("mount table changed during enumeration")
}
