package checkpoint

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestMarkerRoundTrip(t *testing.T) {
	created := time.Date(2026, 11, 23, 10, 9, 8, 123, time.FixedZone("local", 8*3600))
	m, err := newTestMarker("/home/user/.agents", created)
	if err != nil {
		t.Fatal(err)
	}
	data, err := marshalTestMarker(m)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseMarker(data, "/home/user/.agents")
	if err != nil || !got.CreatedAt.Equal(created) || got.CreatedAt.Location() != time.UTC {
		t.Fatalf("marker = %#v, %v", got, err)
	}
}

func TestMarkerRejectsInvalidInput(t *testing.T) {
	cases := []string{
		``, `null`, `{}`, `{"version":2,"target":"/a","created_at":"2026-01-01T00:00:00Z"}`,
		`{"version":1,"target":"/other","created_at":"2026-01-01T00:00:00Z"}`,
		`{"version":1,"target":"/a/../a","created_at":"2026-01-01T00:00:00Z"}`,
		`{"version":1,"target":"/a","created_at":"0001-01-01T00:00:00Z"}`,
		`{"version":1,"target":"/a","created_at":"2026-01-01T00:00:00Z","extra":1}`,
		`{"version":1,"target":"/a","created_at":"2026-01-01T00:00:00Z"} {}`,
	}
	for _, body := range cases {
		if _, err := ParseMarker([]byte(body), "/a"); err == nil {
			t.Errorf("accepted %q", body)
		}
	}
	if _, err := newTestMarker("relative", time.Now()); err == nil {
		t.Fatal("accepted relative target")
	}
	if _, err := newTestMarker("/a", time.Time{}); err == nil || !strings.Contains(err.Error(), "time") {
		t.Fatalf("zero time error = %v", err)
	}
}

// newTestMarker constructs a marker for an absolute, resolved target.
func newTestMarker(target string, created time.Time) (Marker, error) {
	m := Marker{Version: markerVersion, Target: target, CreatedAt: created.UTC()}
	if err := m.validate(target); err != nil {
		return Marker{}, err
	}
	return m, nil
}

// marshalTestMarker returns the versioned marker content.
func marshalTestMarker(m Marker) ([]byte, error) {
	if err := m.validate(m.Target); err != nil {
		return nil, err
	}
	return json.Marshal(m)
}
