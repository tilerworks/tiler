package checkpoint

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"time"
)

// exchangeRecord identifies every directory affected by a recoverable exchange.
type exchangeRecord struct {
	Version       int             `json:"version"`
	Protocol      string          `json:"protocol"`
	Operation     string          `json:"operation"`
	Target        string          `json:"target"`
	Temporary     string          `json:"temporary"`
	Archive       string          `json:"archive"`
	Remove        bool            `json:"remove"`
	HadTarget     bool            `json:"had_target"`
	Original      restoreIdentity `json:"original"`
	Copy          restoreIdentity `json:"copy"`
	Placeholder   restoreIdentity `json:"placeholder,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	Phase         string          `json:"phase"`
	Stage         *exchangeStage  `json:"stage,omitempty"`
	MarkerRemoved bool            `json:"marker_removed,omitempty"`
	OldMarker     restoreIdentity `json:"old_marker,omitempty"`
}

// exchangeStage records the intent before a named sibling file is created.
// A zero identity means that a found file has not been verified and must be
// preserved for manual inspection, rather than reused or removed.
type exchangeStage struct {
	Path     string          `json:"path"`
	Purpose  string          `json:"purpose"`
	Cleanup  bool            `json:"cleanup,omitempty"`
	Identity restoreIdentity `json:"identity"`
}

func (r exchangeRecord) validate(target string) error {
	if r.Version != 3 || (r.Protocol != "exchange" && r.Protocol != "placeholder") || r.Target != target || !filepath.IsAbs(target) || filepath.Clean(target) != target || target == "/" || r.Original.Inode == 0 || r.CreatedAt.IsZero() {
		return errors.New("invalid exchange record header or identity")
	}
	if r.Stage != nil {
		s := r.Stage
		prefix := "." + filepath.Base(target) + ".tiler-checkpoint-stage-"
		if r.Operation != "create" || filepath.Dir(s.Path) != filepath.Dir(target) || filepath.Clean(s.Path) != s.Path || !strings.HasPrefix(filepath.Base(s.Path), prefix) || len(filepath.Base(s.Path)) == len(prefix) || s.Path == r.Temporary {
			return errors.New("invalid exchange stage path")
		}
		if (s.Purpose != "probe" || r.Phase != "intent") && (s.Purpose != "marker" || r.Phase != "marker") {
			return errors.New("invalid exchange stage purpose or phase")
		}
		if s.Cleanup && s.Identity.Inode == 0 {
			return errors.New("cleanup stage has no identity")
		}
		if (s.Identity.Inode == 0 && s.Identity.Device != 0) || (s.Identity.Inode != 0 && (s.Identity.Device != r.Original.Device || s.Identity == r.Original || s.Identity == r.Copy)) {
			return errors.New("invalid exchange stage identity")
		}
	}
	switch r.Operation {
	case "create":
		if r.Protocol != "exchange" || r.Placeholder != (restoreIdentity{}) {
			return errors.New("invalid create protocol")
		}
		if r.OldMarker.Inode == 0 && r.OldMarker.Device != 0 {
			return errors.New("invalid old marker identity")
		}
		if !r.HadTarget || r.Remove || r.Archive != "" || filepath.Dir(r.Temporary) != filepath.Dir(target) || !strings.HasPrefix(filepath.Base(r.Temporary), "."+filepath.Base(target)+".tiler-checkpoint-") || filepath.Clean(r.Temporary) != r.Temporary {
			return errors.New("invalid create exchange record paths or options")
		}
		switch r.Phase {
		case "intent":
			if r.Copy != (restoreIdentity{}) {
				return errors.New("unexpected intent copy identity")
			}
		case "copy", "publish", "exchange", "marker", "complete":
			if r.Copy.Inode == 0 || r.Copy == r.Original {
				return errors.New("invalid create copy identity")
			}
		default:
			return errors.New("invalid create exchange phase")
		}
	case "restore":
		if r.Protocol != "placeholder" {
			return errors.New("unsupported restore protocol")
		}
		if r.Protocol == "placeholder" && r.HadTarget {
			if r.Placeholder.Inode == 0 && r.Placeholder.Device != 0 || r.Placeholder == r.Original || r.Placeholder == r.Copy || (r.Placeholder.Inode != 0 && r.Placeholder.Device != r.Original.Device) {
				return errors.New("invalid placeholder identity")
			}
			if r.Phase != "prepared" && r.Placeholder.Inode == 0 {
				return errors.New("missing placeholder identity")
			}
		} else if r.Placeholder != (restoreIdentity{}) {
			return errors.New("unexpected placeholder")
		}
		if r.MarkerRemoved || r.OldMarker != (restoreIdentity{}) {
			return errors.New("unexpected removed marker flag")
		}
		if r.Temporary != "" {
			return errors.New("unexpected restore temporary path")
		}
		if r.HadTarget {
			if r.Copy.Inode == 0 || r.Copy == r.Original || filepath.Dir(r.Archive) != filepath.Dir(target) {
				return errors.New("invalid restore identities or archive")
			}
			stamp, ok := strings.CutPrefix(r.Archive, target+"-")
			if !ok {
				return errors.New("invalid restore archive name")
			}
			if _, err := time.Parse("20060102-150405", stamp); err != nil {
				return errors.New("invalid restore archive timestamp")
			}
		} else if r.Copy != (restoreIdentity{}) || r.Archive != "" {
			return errors.New("unexpected missing-target restore identity or archive")
		}
		switch r.Phase {
		case "prepared", "marker", "complete":
		case "exchange":
			if r.Protocol == "placeholder" && r.HadTarget {
				return errors.New("invalid placeholder exchange phase")
			}
		case "reserve", "install", "cleanup":
			if r.Protocol != "placeholder" || !r.HadTarget {
				return errors.New("invalid placeholder phase")
			}
		case "archive":
			if !r.HadTarget {
				return errors.New("archive phase requires an existing target")
			}
		case "remove":
			if !r.HadTarget || !r.Remove {
				return errors.New("remove phase requires an existing target and remove option")
			}
		default:
			return errors.New("invalid restore exchange phase")
		}
	default:
		return errors.New("invalid exchange operation")
	}
	return nil
}

func parseExchangeRecord(data []byte, target string) (exchangeRecord, error) {
	var r exchangeRecord
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return r, err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return r, errors.New("exchange record has trailing data")
	}
	return r, r.validate(target)
}
