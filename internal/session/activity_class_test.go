package session

import (
	"reflect"
	"testing"
)

func TestClassifyActivity(t *testing.T) {
	tests := []struct {
		name    string
		state   string
		locked  bool
		mounts  mountEvidence
		want    ActivityClass
		reasons []string
	}{
		{"active user mount", StateActive, false, mountEvidence{Available: true, UserMount: true}, ActivityActive, nil},
		{"active missing mounts", StateActive, false, mountEvidence{Available: true}, ActivityActive, []string{ActivityMissingMounts}},
		{"active internal residual", StateActive, false, mountEvidence{Available: true, InternalMount: true}, ActivityActive, []string{ActivityResidualMounts}},
		{"closed internal residual", StateClosed, false, mountEvidence{Available: true, InternalMount: true}, ActivityActive, []string{ActivityResidualMounts}},
		{"closed user residual", StateClosed, false, mountEvidence{Available: true, UserMount: true}, ActivityActive, []string{ActivityResidualMounts}},
		{"closed no mounts", StateClosed, false, mountEvidence{Available: true}, ActivityInactive, nil},
		{"active complete view", StateActive, false, mountEvidence{Available: true, UserMount: true, InternalMount: true}, ActivityActive, nil},
		{"locked empty record", "", true, mountEvidence{Available: true}, ActivityUnknown, []string{ActivityUnknownReason}},
		{"locked with residual", "", true, mountEvidence{Available: true, InternalMount: true}, ActivityUnknown, []string{ActivityUnknownReason}},
		{"active snapshot unavailable", StateActive, false, mountEvidence{}, ActivityActive, nil},
		{"closed snapshot unavailable", StateClosed, false, mountEvidence{}, ActivityUnknown, []string{ActivityUnknownReason}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, reasons := classifyActivity(tt.state, tt.locked, tt.mounts)
			if got != tt.want || !reflect.DeepEqual(reasons, tt.reasons) {
				t.Fatalf("classifyActivity = %q, %v; want %q, %v", got, reasons, tt.want, tt.reasons)
			}
		})
	}
}
