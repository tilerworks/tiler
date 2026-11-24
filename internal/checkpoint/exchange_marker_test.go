package checkpoint

import (
	"encoding/json"
	"testing"
	"time"
)

func TestExchangeMarkerRoundTrip(t *testing.T) {
	created := time.Date(2026, 11, 23, 10, 20, 30, 0, time.FixedZone("offset", 3600))
	m, err := newExchangeMarker("/home/user/.agents", created)
	if err != nil {
		t.Fatal(err)
	}
	data, err := marshalExchangeMarker(m)
	if err != nil {
		t.Fatal(err)
	}
	got, err := parseExchangeMarker(data, m.Target)
	if err != nil || got != m {
		t.Fatalf("round trip: got %+v, error %v", got, err)
	}
	if got.CreatedAt.Location() != time.UTC || !got.CreatedAt.Equal(created) {
		t.Fatalf("creation time = %v", got.CreatedAt)
	}
	if _, err := ParseMarker(data, m.Target); err == nil {
		t.Fatal("v1 parser accepted exchange marker")
	}
}

func TestExchangeMarkerRejectsInvalidData(t *testing.T) {
	created := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	valid, err := newExchangeMarker("/home/user/.agents", created)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*exchangeMarker)
	}{
		{"old version", func(m *exchangeMarker) { m.Version = 1 }},
		{"unknown version", func(m *exchangeMarker) { m.Version = 3 }},
		{"unknown protocol", func(m *exchangeMarker) { m.Protocol = "copy" }},
		{"relative target", func(m *exchangeMarker) { m.Target = "relative" }},
		{"root target", func(m *exchangeMarker) { m.Target = "/" }},
		{"unclean target", func(m *exchangeMarker) { m.Target = "/home/../home/user" }},
		{"zero time", func(m *exchangeMarker) { m.CreatedAt = time.Time{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := valid
			tc.change(&m)
			data, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := parseExchangeMarker(data, m.Target); err == nil {
				t.Fatal("accepted invalid marker")
			}
			if _, err := marshalExchangeMarker(m); err == nil {
				t.Fatal("marshaled invalid marker")
			}
		})
	}
	data, err := marshalExchangeMarker(valid)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range [][]byte{append(data, []byte(" {}")...), []byte(`{"version":2,"unknown":true}`), []byte(`{`)} {
		if _, err := parseExchangeMarker(input, valid.Target); err == nil {
			t.Fatalf("accepted invalid data %q", input)
		}
	}
	if _, err := parseExchangeMarker(data, "/other"); err == nil {
		t.Fatal("accepted mismatched target")
	}
	old, err := newTestMarker(valid.Target, created)
	if err != nil {
		t.Fatal(err)
	}
	oldData, err := marshalTestMarker(old)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseExchangeMarker(oldData, valid.Target); err == nil {
		t.Fatal("accepted v1 marker")
	}
}
