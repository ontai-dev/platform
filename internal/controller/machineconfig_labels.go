package controller

// MachineConfig Secret schema constants.
// platform is the sole writer of all sync-status/sync-hash labels on machineconfig secrets.
// Admins may create the secret with data.machineconfig content; labels are managed by platform.
// platform-schema.md §15 (MachineConfig Source of Truth).

const (
	// LabelMachineConfigCluster is the label key carrying the TalosCluster name.
	LabelMachineConfigCluster = "platform.ontai.dev/cluster"

	// LabelMachineConfigClass identifies the class of machineconfig stored in the secret.
	// Values: "controlplane", "worker", or "node-{node-name}".
	LabelMachineConfigClass = "platform.ontai.dev/mc-class"

	// LabelMachineConfigSyncStatus tracks the last-known sync state.
	// Values: MachineConfigSyncStatusPending, MachineConfigSyncStatusSynced, MachineConfigSyncStatusDrift.
	LabelMachineConfigSyncStatus = "platform.ontai.dev/sync-status"

	// LabelMachineConfigSyncHash is the hex-encoded SHA-256 of the machineconfig bytes at last sync.
	// Written by platform after each confirmed MachineConfigSync Job completion.
	LabelMachineConfigSyncHash = "platform.ontai.dev/sync-hash"

	// LabelMachineConfigSyncedAt is the UTC timestamp of the last confirmed sync,
	// formatted as "20060102-150405Z" (ISO 8601 basic with dashes, no colons)
	// so the value is valid as a Kubernetes label.
	LabelMachineConfigSyncedAt = "platform.ontai.dev/synced-at"
)

// MachineConfigSyncStatus values for LabelMachineConfigSyncStatus.
const (
	// MachineConfigSyncStatusPending means the secret exists but no sync has been confirmed yet.
	MachineConfigSyncStatusPending = "pending"

	// MachineConfigSyncStatusSynced means the last MachineConfigSync Job completed successfully
	// and the hash in LabelMachineConfigSyncHash matches the secret content.
	MachineConfigSyncStatusSynced = "synced"

	// MachineConfigSyncStatusDrift means the secret content hash differs from the last
	// confirmed sync hash -- a new MachineConfigSync Job will be triggered.
	MachineConfigSyncStatusDrift = "drift"

	// MachineConfigSyncStatusDecommissioned marks a per-node secret whose node no longer
	// appears in the live Talos API roster. The secret is retained for audit (INV-006).
	MachineConfigSyncStatusDecommissioned = "decommissioned"
)

// MachineConfigClass values for LabelMachineConfigClass.
const (
	// MachineConfigClassControlPlane is the label value for the base controlplane class secret.
	MachineConfigClassControlPlane = "controlplane"

	// MachineConfigClassWorker is the label value for the base worker class secret.
	MachineConfigClassWorker = "worker"
)

// LabelMachineConfigCompression indicates the compression algorithm applied to the
// machineconfig data bytes. Absent label means no compression (raw YAML). RECON-F5.
const LabelMachineConfigCompression = "platform.ontai.dev/compression"

// MachineConfigCompressionGzip is the label value when data.machineconfig is gzip-compressed.
const MachineConfigCompressionGzip = "gzip"

// MachineConfigSecretNamePrefix is the name prefix for all machineconfig source-of-truth secrets.
// Full name: seam-mc-{cluster}-{class}.
const MachineConfigSecretNamePrefix = "seam-mc-"

// MachineConfigDataKey is the key in the Secret's data map that holds the raw Talos machineconfig YAML.
// Used by platform-generated machineconfig secrets (gzip-compressed).
const MachineConfigDataKey = "machineconfig"

// MachineConfigDataKeyYAML is the data key used by compiler-generated per-node secrets.
// Compiler generates secrets with key "machineconfig.yaml" containing raw uncompressed YAML.
// The MachineConfigSync reconciler and conductor capability fall back to this key when
// the primary MachineConfigDataKey is absent. PLT-BUG-3-ARCH.
const MachineConfigDataKeyYAML = "machineconfig.yaml"

// LabelCompilerManagedBy is the label key used by compiler-generated secrets.
// Value is always "compiler". Used to detect compiler per-node secrets in the import path.
const LabelCompilerManagedBy = "ontai.dev/managed-by"

// LabelCompilerNodeRole is the label on compiler-generated per-node secrets.
// Values: "init" (first control-plane node), "controlplane", "worker".
const LabelCompilerNodeRole = "ontai.dev/node-role"

// LabelCompilerNode is the label carrying the full node name on compiler-generated secrets.
// Example value: "ccs-dev-cp1".
const LabelCompilerNode = "ontai.dev/node"

// LabelCompilerCluster is the label carrying the cluster name on compiler-generated secrets.
const LabelCompilerCluster = "ontai.dev/cluster"

// MachineConfigNodeLabel is the Talos node label injected by the machineconfig-sync conductor capability.
// Its presence on a node proves that the node accepted an ONT-governed machineconfig.
const MachineConfigNodeLabel = "ont.platform.dev/controlled"

// AnnotationMCGeneration is the annotation key on a watch-triggered MachineConfigSync CR recording
// the MachineConfig CR generation that triggered creation. Used by reconcileMachineConfigSync to
// detect generation changes and replace stale sync CRs.
const AnnotationMCGeneration = "platform.ontai.dev/mc-generation"

// MachineConfigSecretName returns the canonical Secret name for a given cluster and class.
// class should be MachineConfigClassControlPlane, MachineConfigClassWorker, or "node-{name}".
func MachineConfigSecretName(cluster, class string) string {
	return MachineConfigSecretNamePrefix + cluster + "-" + class
}

// MachineConfigCRName returns the canonical MachineConfig CR name for a given cluster and hostname.
// Name convention: seam-mc-{cluster}-{hostname}.
func MachineConfigCRName(cluster, hostname string) string {
	return MachineConfigSecretNamePrefix + cluster + "-" + hostname
}

// labelSafeHash truncates a hex-encoded SHA-256 digest to 63 characters so it
// fits within the Kubernetes label value length limit. 63 hex chars = 252 bits,
// sufficient for collision resistance in any realistic label context.
func labelSafeHash(hexDigest string) string {
	if len(hexDigest) > 63 {
		return hexDigest[:63]
	}
	return hexDigest
}
