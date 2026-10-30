package activity

import (
	"encoding/json"
	"fmt"
	"time"
)

// Phase values of one event. A lifecycle action records a started
// phase before the action and a succeeded or failed phase after it;
// an initialization writes the initialized phase.
const (
	PhaseStarted     = "started"
	PhaseSucceeded   = "succeeded"
	PhaseFailed      = "failed"
	PhaseInitialized = "initialized"
)

// Event names shared by the session and image lifecycles.
const (
	EventSessionCreate          = "session.create"
	EventSessionOpen            = "session.open"
	EventSessionClose           = "session.close"
	EventMount                  = "mount"
	EventUnmount                = "unmount"
	EventImageCreateFull        = "image.create.full"
	EventImageCreateIncremental = "image.create.incremental"
	// EventImageReuse records a publication that found the identical
	// version already published and used it instead of creating one.
	EventImageReuse      = "image.reuse"
	EventImageDependency = "image.dependency.activity"
	EventInitialize      = "activity.initialize"
)

// Event is one lifecycle record. Event and Phase are required; the
// remaining fields carry the context that explains the activity.
type Event struct {
	Event         string
	Phase         string
	Session       string
	InstanceID    string
	Seq           int
	Mountpoint    string
	Image         string
	Version       string
	SourceVersion string
	SourceEvent   string
	Error         string
}

// Validate rejects an event that cannot carry a meaningful record. A
// caller that appends several records for one logical event checks it
// first, so a malformed event never leaves a partial batch behind.
func (ev Event) Validate() error { return ev.check() }

// check rejects an event that cannot carry a meaningful record.
func (ev Event) check() error {
	if ev.Event == "" {
		return fmt.Errorf("%w: empty event name", ErrInvalidEvent)
	}
	if !knownPhase(ev.Phase) {
		return fmt.Errorf("%w: phase %q", ErrInvalidEvent, ev.Phase)
	}
	return nil
}

// knownPhase reports one of the phases the log vocabulary defines.
func knownPhase(phase string) bool {
	switch phase {
	case PhaseStarted, PhaseSucceeded, PhaseFailed, PhaseInitialized:
		return true
	default:
		return false
	}
}

// recordVersion is the format number written on every new line.
// Callers never set it; Event.Version remains the image version id.
const recordVersion = 1

// payload is the activity context of one line. ImageVersion is the
// image version id and is omitted when the event has none.
type payload struct {
	Time          string `json:"time"`
	Event         string `json:"event"`
	Phase         string `json:"phase"`
	Session       string `json:"session,omitempty"`
	InstanceID    string `json:"instance_id,omitempty"`
	Seq           int    `json:"seq,omitempty"`
	Mountpoint    string `json:"mountpoint,omitempty"`
	Image         string `json:"image,omitempty"`
	ImageVersion  string `json:"image_version,omitempty"`
	SourceVersion string `json:"source_version,omitempty"`
	SourceEvent   string `json:"source_event,omitempty"`
	Error         string `json:"error,omitempty"`
}

// record is the on-disk shape of one new line. The integer format
// version is written first, then the same context fields as before.
type record struct {
	Version int `json:"version"`
	payload
}

// record renders ev at one UTC instant.
func (ev Event) record(at time.Time) record {
	return record{
		Version: recordVersion,
		payload: payload{
			Time:          at.UTC().Format(time.RFC3339Nano),
			Event:         ev.Event,
			Phase:         ev.Phase,
			Session:       ev.Session,
			InstanceID:    ev.InstanceID,
			Seq:           ev.Seq,
			Mountpoint:    ev.Mountpoint,
			Image:         ev.Image,
			ImageVersion:  ev.Version,
			SourceVersion: ev.SourceVersion,
			SourceEvent:   ev.SourceEvent,
			Error:         ev.Error,
		},
	}
}

// encodeLine renders one event as a single JSON line within
// MaxLineBytes. Only the diagnostic text may be shortened; an
// over-long identifier reports ErrLineTooLong instead of writing a
// truncated record.
func encodeLine(ev Event, at time.Time) ([]byte, error) {
	if err := ev.check(); err != nil {
		return nil, err
	}
	rec := ev.record(at)
	line, err := marshalLine(rec)
	if err != nil {
		return nil, err
	}
	text := []rune(ev.Error)
	for len(line) > MaxLineBytes && len(text) > 0 {
		drop := len(line) - MaxLineBytes
		if drop > len(text) {
			drop = len(text)
		}
		text = text[:len(text)-drop]
		rec.Error = string(text)
		if line, err = marshalLine(rec); err != nil {
			return nil, err
		}
	}
	if len(line) > MaxLineBytes {
		return nil, fmt.Errorf("%w: %d bytes", ErrLineTooLong, len(line))
	}
	return line, nil
}

// marshalLine encodes rec and terminates the line with a newline.
// JSON escapes embedded newlines, so one event never spans lines.
func marshalLine(rec record) ([]byte, error) {
	data, err := json.Marshal(rec)
	if err != nil {
		return nil, fmt.Errorf("activity: encode event: %w", err)
	}
	return append(data, '\n'), nil
}
