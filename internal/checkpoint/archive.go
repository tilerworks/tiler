package checkpoint

import (
	"errors"
	"time"
)

var archiveNow = time.Now

// archiveName chooses the restore time and records it in the transaction.
func archiveName(target string, marker Marker) (string, error) {
	if err := marker.validate(target); err != nil {
		return "", err
	}
	stamp := archiveNow()
	if stamp.IsZero() || stamp.Year() < 1 || stamp.Year() > 9999 {
		return "", errors.New("invalid archive timestamp")
	}
	return target + "-" + stamp.Local().Format("20060102-150405"), nil
}
