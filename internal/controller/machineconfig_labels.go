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

	// LabelMachineConfigSyncedAt is the RFC3339 timestamp of the last confirmed sync.
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
)

// MachineConfigClass values for LabelMachineConfigClass.
const (
	// MachineConfigClassControlPlane is the label value for the base controlplane class secret.
	MachineConfigClassControlPlane = "controlplane"

	// MachineConfigClassWorker is the label value for the base worker class secret.
	MachineConfigClassWorker = "worker"
)

// MachineConfigSecretNamePrefix is the name prefix for all machineconfig source-of-truth secrets.
// Full name: seam-mc-{cluster}-{class}.
const MachineConfigSecretNamePrefix = "seam-mc-"

// MachineConfigDataKey is the key in the Secret's data map that holds the raw Talos machineconfig YAML.
const MachineConfigDataKey = "machineconfig"

// MachineConfigNodeLabel is the Talos node label injected by the machineconfig-sync conductor capability.
// Its presence on a node proves that the node accepted an ONT-governed machineconfig.
const MachineConfigNodeLabel = "ont.platform.dev/controlled"

// MachineConfigSecretName returns the canonical Secret name for a given cluster and class.
// class should be MachineConfigClassControlPlane, MachineConfigClassWorker, or "node-{name}".
func MachineConfigSecretName(cluster, class string) string {
	return MachineConfigSecretNamePrefix + cluster + "-" + class
}
