package session

// diskActivity uses attachment identity even for closed or unmounted actions.
// A shadow owned by the session also identifies otherwise unrecorded remnants.
func (e *Engine) diskActivity(view View, images []DiskImage) mountEvidence {
	evidence := mountEvidence{Available: true}
	for _, attached := range images {
		internal := e.activityRuntimeOwner(attached.Point) == view.Name
		owned := e.activityRuntimeOwner(diskRuntime(attached.Shadow)) == view.Name
		if internal || owned {
			evidence.InternalMount = true
		}
		for _, action := range view.Actions {
			if action.Type == TypeMount && !internal &&
				e.diskMatch(view.Name, action.Action, []DiskImage{attached}) {
				evidence.UserMount = true
			}
		}
	}
	return evidence
}
