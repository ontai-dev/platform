package controller

// operationalJobBase provides shared helpers for day-2 operational CRD reconcilers
// that submit Conductor executor Jobs directly (without Kueue).
//
// These Jobs run in seam-tenant-{cluster-name} (for tenant clusters) or
// ont-system (for management clusters). They mount the target cluster's
// kubeconfig and talosconfig Secrets from ont-system. platform-design.md §6.
//
// CP-INV-010: Kueue is NOT used. Jobs are submitted directly.
// INV-018: gate failures are permanent — backoffLimit=0, no retries.

import (
	"context"
	"fmt"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	seamcorev1alpha1 "github.com/ontai-dev/seam-core/api/v1alpha1"
)

const (
	// operationalJobPollInterval is the requeue interval while waiting for
	// an operational Conductor Job to complete.
	operationalJobPollInterval = 15 * time.Second

	// capabilityUnavailableRetryInterval is the requeue interval when the cluster
	// RunnerConfig is absent or the required capability has not yet been published
	// by the Conductor agent. conductor-schema.md §5, CR-INV-005.
	capabilityUnavailableRetryInterval = 30 * time.Second

	// operationalJobTTL is the Job TTL in seconds after completion.
	// The reconciler reads the OperationResult before this expires.
	operationalJobTTL = int32(600)

	// operationalJobBackoffLimit enforces INV-018: gate failures are permanent.
	operationalJobBackoffLimit = int32(0)

	// executorTalosconfigMountPath is the container mount path for the talosconfig Secret.
	executorTalosconfigMountPath = "/var/run/secrets/talosconfig"

	// executorTalosconfigEnvPath is the TALOSCONFIG_PATH value injected into executor Jobs.
	executorTalosconfigEnvPath = executorTalosconfigMountPath + "/talosconfig"
)

// jobSpec builds a Conductor executor Job spec for the given capability and cluster.
// runnerImage is read from RunnerConfig.Spec.RunnerImage — callers must pass it explicitly.
// The talosconfig Secret ({clusterName}-talosconfig) is mounted from the job namespace;
// callers are responsible for ensuring it exists before submitting the Job.
// OPERATION_RESULT_CR is set to jobName — conductor creates an
// InfrastructureTalosClusterOperationResult CR with that name in POD_NAMESPACE.
// conductor-schema.md §17, config.EnvCapability/EnvClusterRef/EnvOperationResultCR.
func jobSpec(jobName, namespace, clusterName, capability, runnerImage string) *batchv1.Job {
	ttl := operationalJobTTL
	backoff := operationalJobBackoffLimit
	talosconfigName := clusterName + "-talosconfig"
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: namespace,
			Labels: map[string]string{
				"platform.ontai.dev/cluster":    clusterName,
				"platform.ontai.dev/capability": capability,
			},
		},
		Spec: batchv1.JobSpec{
			TTLSecondsAfterFinished: &ttl,
			BackoffLimit:            &backoff,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"platform.ontai.dev/cluster":    clusterName,
						"platform.ontai.dev/capability": capability,
					},
				},
				Spec: corev1.PodSpec{
					RestartPolicy:      corev1.RestartPolicyNever,
					ServiceAccountName: "platform-executor",
					Volumes: []corev1.Volume{
						{
							Name: "talosconfig",
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: talosconfigName,
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Name:            "executor",
							Image:           runnerImage,
							ImagePullPolicy: corev1.PullAlways,
							Args:            []string{"execute"},
							Env: []corev1.EnvVar{
								{Name: "CAPABILITY", Value: capability},
								{Name: "CLUSTER_REF", Value: clusterName},
								// OPERATION_RESULT_CR: conductor creates a TCOR CR with this name.
								// The platform reconciler reads it back by the same name.
								{Name: "OPERATION_RESULT_CR", Value: jobName},
								{
									Name: "POD_NAMESPACE",
									ValueFrom: &corev1.EnvVarSource{
										FieldRef: &corev1.ObjectFieldSelector{
											FieldPath: "metadata.namespace",
										},
									},
								},
								{Name: "TALOSCONFIG_PATH", Value: executorTalosconfigEnvPath},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "talosconfig",
									MountPath: executorTalosconfigMountPath,
									ReadOnly:  true,
								},
							},
						},
					},
				},
			},
		},
	}
}

// getOperationalJob returns the Job by name/namespace, or nil if not found.
func getOperationalJob(ctx context.Context, c client.Client, namespace, jobName string) (*batchv1.Job, error) {
	job := &batchv1.Job{}
	if err := c.Get(ctx, types.NamespacedName{Name: jobName, Namespace: namespace}, job); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get Job %s/%s: %w", namespace, jobName, err)
	}
	return job, nil
}

// tenantNS returns the seam-tenant-{clusterRef} namespace.
func tenantNS(clusterRef string) string {
	return "seam-tenant-" + clusterRef
}

// readOperationRecord reads the per-cluster TCOR (named clusterRef in
// seam-tenant-{clusterRef}) and looks up the record by jobName key.
// Returns (complete, failed, message).
// Returns (false, false, "") when the TCOR does not yet exist or the
// record has not been written yet — the Job is still running.
func readOperationRecord(ctx context.Context, c client.Client, clusterRef, jobName string) (complete, failed bool, message string) {
	tcor := &seamcorev1alpha1.InfrastructureTalosClusterOperationResult{}
	if err := c.Get(ctx, types.NamespacedName{Name: clusterRef, Namespace: tenantNS(clusterRef)}, tcor); err != nil {
		return false, false, ""
	}
	rec, ok := tcor.Spec.Operations[jobName]
	if !ok {
		return false, false, ""
	}
	switch rec.Status {
	case seamcorev1alpha1.TalosClusterResultSucceeded:
		return true, false, rec.Message
	case seamcorev1alpha1.TalosClusterResultFailed:
		msg := rec.Message
		if rec.FailureReason != nil && rec.FailureReason.Reason != "" {
			msg = rec.FailureReason.Reason
		}
		return false, true, msg
	}
	return false, false, ""
}

// ensureTCOR creates the per-cluster TCOR in seam-tenant-{clusterRef} if it
// does not yet exist. Called by ensureTenantExecutorResources on cluster admission.
func ensureTCOR(ctx context.Context, c client.Client, clusterRef, talosVersion string) error {
	ns := tenantNS(clusterRef)
	tcor := &seamcorev1alpha1.InfrastructureTalosClusterOperationResult{}
	if err := c.Get(ctx, types.NamespacedName{Name: clusterRef, Namespace: ns}, tcor); err == nil {
		return nil
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("ensureTCOR: get TCOR %s/%s: %w", ns, clusterRef, err)
	}
	tcor = &seamcorev1alpha1.InfrastructureTalosClusterOperationResult{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterRef,
			Namespace: ns,
			Labels:    map[string]string{"platform.ontai.dev/cluster": clusterRef},
		},
		Spec: seamcorev1alpha1.InfrastructureTalosClusterOperationResultSpec{
			ClusterRef:   clusterRef,
			TalosVersion: talosVersion,
			Revision:     1,
		},
	}
	if err := c.Create(ctx, tcor); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("ensureTCOR: create TCOR %s/%s: %w", ns, clusterRef, err)
	}
	return nil
}

// bumpTCORRevision archives the current revision of the cluster TCOR to the
// GraphQuery DB stub, then advances to a new revision for the given talosVersion.
// Called by UpgradePolicyReconciler after a successful talosVersion upgrade.
func bumpTCORRevision(ctx context.Context, c client.Client, clusterRef, newTalosVersion string) error {
	ns := tenantNS(clusterRef)
	tcor := &seamcorev1alpha1.InfrastructureTalosClusterOperationResult{}
	if err := c.Get(ctx, types.NamespacedName{Name: clusterRef, Namespace: ns}, tcor); err != nil {
		if apierrors.IsNotFound(err) {
			return ensureTCOR(ctx, c, clusterRef, newTalosVersion)
		}
		return fmt.Errorf("bumpTCORRevision: get TCOR %s/%s: %w", ns, clusterRef, err)
	}
	if tcor.Spec.TalosVersion == newTalosVersion {
		return nil
	}
	stubDumpTCORRevisionToGraphQueryDB(ctx, clusterRef, tcor.Spec.Revision, tcor.Spec.TalosVersion, tcor.Spec.Operations)
	patch := client.MergeFrom(tcor.DeepCopy())
	tcor.Spec.Revision++
	tcor.Spec.TalosVersion = newTalosVersion
	tcor.Spec.Operations = nil
	return c.Patch(ctx, tcor, patch)
}

// jobSpecWithExclusions builds a Conductor executor Job spec and applies NotIn
// NodeAffinity constraints to prevent the Job pod from landing on the listed nodes.
// Used for all day-2 self-operation Jobs to avoid scheduling on maintenance targets
// or the operator leader node. conductor-schema.md §13.
func jobSpecWithExclusions(jobName, namespace, clusterName, capability string, nodeExclusions []string, runnerImage string) *batchv1.Job {
	job := jobSpec(jobName, namespace, clusterName, capability, runnerImage)
	if len(nodeExclusions) == 0 {
		return job
	}
	job.Spec.Template.Spec.Affinity = &corev1.Affinity{
		NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{
					{
						MatchExpressions: []corev1.NodeSelectorRequirement{
							{
								Key:      "kubernetes.io/hostname",
								Operator: corev1.NodeSelectorOpNotIn,
								Values:   nodeExclusions,
							},
						},
					},
				},
			},
		},
	}
	return job
}

// resolveOperatorLeaderNode reads the platform-leader Lease from seam-system,
// resolves the holder pod, and returns the pod's node name. Returns an empty
// string (not an error) when the Lease is absent or the holder pod is not found.
// apiReader must be an uncached reader (mgr.GetAPIReader()) to avoid establishing
// a cluster-scope Pod informer that the platform SA lacks permission to populate.
// conductor-schema.md §13, CP-INV-007.
func resolveOperatorLeaderNode(ctx context.Context, c client.Client, apiReader client.Reader) (string, error) {
	lease := &coordinationv1.Lease{}
	if err := c.Get(ctx, types.NamespacedName{
		Name:      "platform-leader",
		Namespace: "seam-system",
	}, lease); err != nil {
		if apierrors.IsNotFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("get platform-leader Lease: %w", err)
	}
	if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity == "" {
		return "", nil
	}
	// HolderIdentity format from controller-runtime: "{podName}_{uid}".
	// Extract the pod name before the first underscore.
	holderIdentity := *lease.Spec.HolderIdentity
	podName := holderIdentity
	if idx := strings.Index(holderIdentity, "_"); idx != -1 {
		podName = holderIdentity[:idx]
	}
	// Use the uncached API reader to avoid triggering a cluster-scope Pod informer.
	// Treat Forbidden the same as NotFound: skip leader exclusion rather than failing.
	// apiReader may be nil in unit tests — fall back to the cached client in that case.
	reader := client.Reader(c)
	if apiReader != nil {
		reader = apiReader
	}
	pod := &corev1.Pod{}
	if err := reader.Get(ctx, types.NamespacedName{Name: podName, Namespace: "seam-system"}, pod); err != nil {
		if apierrors.IsNotFound(err) || apierrors.IsForbidden(err) {
			return "", nil
		}
		return "", fmt.Errorf("get leader pod %s/seam-system: %w", podName, err)
	}
	return pod.Spec.NodeName, nil
}

// buildNodeExclusions merges the operation's target nodes and the leader node
// into a deduplicated list of nodes that should not run the executor Job.
// All day-2 operations are self-operations (SelfOperation=true) — exclusions
// always apply. conductor-schema.md §13.
func buildNodeExclusions(targetNodes []string, leaderNode string) []string {
	if len(targetNodes) == 0 && leaderNode == "" {
		return nil
	}
	seen := make(map[string]bool)
	var exclusions []string
	for _, n := range targetNodes {
		if n != "" && !seen[n] {
			seen[n] = true
			exclusions = append(exclusions, n)
		}
	}
	if leaderNode != "" && !seen[leaderNode] {
		exclusions = append(exclusions, leaderNode)
	}
	return exclusions
}

// operationalJobName returns a deterministic Job name for an operational CRD.
// Format: {crName}-{capability} — unique within the namespace.
// Used by MaintenanceBundleReconciler (which still submits Jobs directly).
func operationalJobName(crName, capability string) string {
	return fmt.Sprintf("%s-%s", crName, capability)
}

// getClusterRunnerConfig returns the cluster-level RunnerConfig from ont-system
// for the given cluster name. Returns nil when the RunnerConfig does not yet
// exist — normal during Conductor agent startup. Day-2 reconcilers call this
// to read status.capabilities before submitting any Job.
// conductor-schema.md §5, CR-INV-005.
func getClusterRunnerConfig(ctx context.Context, c client.Client, clusterName string) (*OperationalRunnerConfig, error) {
	return getOperationalRunnerConfig(ctx, c, bootstrapRunnerConfigNamespace, bootstrapRunnerConfigName(clusterName))
}

// hasCapability reports whether the RunnerConfig status.capabilities list
// contains an entry with the given name. Used by day-2 reconcilers to gate
// Job submission. conductor-schema.md §5, CR-INV-005.
func hasCapability(rc *OperationalRunnerConfig, name string) bool {
	for _, cap := range rc.Status.Capabilities {
		if cap.Name == name {
			return true
		}
	}
	return false
}

// getOperationalRunnerConfig returns the OperationalRunnerConfig by name and
// namespace, or nil if not found. conductor-schema.md §17.
func getOperationalRunnerConfig(ctx context.Context, c client.Client, namespace, name string) (*OperationalRunnerConfig, error) {
	rc := &OperationalRunnerConfig{}
	if err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, rc); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get RunnerConfig %s/%s: %w", namespace, name, err)
	}
	return rc, nil
}
