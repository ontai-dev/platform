package v1alpha1

// LineageSynced condition type and reason constants for Seam Infrastructure
// Provider CRDs. These values are defined by seam-core-schema.md §7 Declaration 5
// and are reserved platform-wide.

const (
	// ConditionTypeLineageSynced is the reserved condition type for lineage
	// synchronization status on every root declaration CR.
	// seam-core-schema.md §7 Declaration 5.
	ConditionTypeLineageSynced = "LineageSynced"

	// ReasonLineageControllerAbsent is set when a reconciler initializes the
	// LineageSynced condition to False. It indicates InfrastructureLineageController
	// has not yet been deployed and has not processed this root declaration.
	// seam-core-schema.md §7 Declaration 5.
	ReasonLineageControllerAbsent = "LineageControllerAbsent"
)
