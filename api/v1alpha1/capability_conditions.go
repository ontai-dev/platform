package v1alpha1

// Condition types and reasons shared across all day-2 operational reconcilers
// that gate Job submission on conductor-published capability availability.
// conductor-schema.md §5, CR-INV-005.
const (
	// ConditionTypeCapabilityUnavailable is set on a day-2 CR when the required
	// Conductor capability is not yet published in the cluster RunnerConfig status.
	// Clears automatically when the capability becomes available.
	ConditionTypeCapabilityUnavailable = "CapabilityUnavailable"

	// ReasonRunnerConfigNotFound is set when the cluster RunnerConfig does not yet
	// exist in ont-system. This is normal during Conductor agent startup.
	ReasonRunnerConfigNotFound = "RunnerConfigNotFound"

	// ReasonCapabilityNotPublished is set when the cluster RunnerConfig exists but
	// the required capability is absent from status.capabilities. This is normal
	// while the Conductor agent is initialising its capability manifest.
	ReasonCapabilityNotPublished = "CapabilityNotPublished"
)
