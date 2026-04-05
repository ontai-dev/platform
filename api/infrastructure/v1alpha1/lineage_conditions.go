package v1alpha1

// Lineage condition type and reason constants re-exported from
// seam-core/pkg/conditions — the canonical source. seam-core-schema.md §7
// Declaration 5. SC-INV-002 / Gap 31 WS2.
//
// Seam Infrastructure Provider reconcilers reference these via the infrav1alpha1
// package alias; they continue to compile without modification. New code should
// prefer importing github.com/ontai-dev/seam-core/pkg/conditions directly.

import "github.com/ontai-dev/seam-core/pkg/conditions"

const (
	// ConditionTypeLineageSynced is the reserved condition type for lineage
	// synchronization status on every root declaration CR.
	// Canonical source: github.com/ontai-dev/seam-core/pkg/conditions.
	ConditionTypeLineageSynced = conditions.ConditionTypeLineageSynced

	// ReasonLineageControllerAbsent is set when the reconciler initialises
	// LineageSynced to False. Canonical source: pkg/conditions.
	ReasonLineageControllerAbsent = conditions.ReasonLineageControllerAbsent
)
