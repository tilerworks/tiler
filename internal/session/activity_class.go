package session

// ActivityClass describes query-time activity without changing record state.
type ActivityClass string

// Activity classes and diagnostic reasons describe the query-time read model.
const (
	ActivityActive   ActivityClass = "active"
	ActivityInactive ActivityClass = "inactive"
	ActivityUnknown  ActivityClass = "unknown"

	ActivityMissingMounts  = "missing-mounts"
	ActivityResidualMounts = "residual-mounts"
	ActivityUnknownReason  = "activity-unknown"
)

// mountEvidence describes actual mounts from one host snapshot, not health
// probes or the existence of runtime directories and image files.
type mountEvidence struct {
	Available     bool
	UserMount     bool
	InternalMount bool
}

func classifyActivity(state string, locked bool, mounts mountEvidence) (ActivityClass, []string) {
	if locked {
		return ActivityUnknown, []string{ActivityUnknownReason}
	}
	if mounts.UserMount || mounts.InternalMount {
		if state == StateClosed || !mounts.UserMount {
			return ActivityActive, []string{ActivityResidualMounts}
		}
		return ActivityActive, nil
	}
	if state == StateActive {
		if mounts.Available {
			return ActivityActive, []string{ActivityMissingMounts}
		}
		return ActivityActive, nil
	}
	if !mounts.Available {
		return ActivityUnknown, []string{ActivityUnknownReason}
	}
	return ActivityInactive, nil
}
