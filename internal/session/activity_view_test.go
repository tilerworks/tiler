package session

import "testing"

func TestSessionLastActivityDistinguishesMissingAndUnknown(t *testing.T) {
	e := &Engine{Store: Store{LogsDir: t.TempDir()}}
	value, err := e.LastActivity("dev")
	if err != nil || value.Unknown || !value.At.IsZero() {
		t.Fatalf("missing = %+v, %v", value, err)
	}
	e.LogsDir = "relative-invalid-root"
	value, err = e.LastActivity("dev")
	if err == nil || !value.Unknown || !value.At.IsZero() {
		t.Fatalf("unreadable = %+v, %v", value, err)
	}
}
