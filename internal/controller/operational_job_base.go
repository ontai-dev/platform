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
)

const (
	// operationalJobPollInterval is the requeue interval while waiting for
	// an operational Conductor Job to complete.
	operationalJobPollInterval = 15 * time.Second

	// operationalJobTTL is the Job TTL in seconds after completion.
	// The reconciler reads the OperationResult before this expires.
	operationalJobTTL = int32(600)

	// operationalJobBackoffLimit enforces INV-018: gate failures are permanent.
	operationalJobBackoffLimit = int32(0)

	// conductorImage is the Conductor executor image. In production this is
	// resolved from RunnerConfig. Placeholder until SC-INV-002 completes.
	conductorImage = "registry.ontai.dev/ontai-dev/conductor:latest"
)

// jobSpec builds a Conductor executor Job spec for the given capability and cluster.
func jobSpec(jobName, namespace, clusterName, capability string) *batchv1.Job {
	ttl := operationalJobTTL
	backoff := operationalJobBackoffLimit
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
					Containers: []corev1.Container{
						{
							Name:  "executor",
							Image: conductorImage,
							Args: []string{
								"execute",
								"--capability", capability,
								"--cluster", clusterName,
							},
							Env: []corev1.EnvVar{
								{
									Name:  "CLUSTER_NAME",
									Value: clusterName,
								},
								{
									Name:  "CLUSTER_NAMESPACE",
									Value: namespace,
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

// readOperationalResult checks for the OperationResult ConfigMap written by the
// Conductor executor. Returns (complete, failed, message).
func readOperationalResult(ctx context.Context, c client.Client, namespace, jobName string) (complete, failed bool, message string) {
	cmName := jobName + operationResultConfigMapSuffix
	cm := &corev1.ConfigMap{}
	if err := c.Get(ctx, types.NamespacedName{Name: cmName, Namespace: namespace}, cm); err != nil {
		return false, false, ""
	}
	status := cm.Data["status"]
	msg := cm.Data["message"]
	switch status {
	case "success":
		return true, false, msg
	case "failure", "failed":
		return false, true, msg
	default:
		return false, false, msg
	}
}

// jobSpecWithExclusions builds a Conductor executor Job spec and applies NotIn
// NodeAffinity constraints to prevent the Job pod from landing on the listed nodes.
// Used for all day-2 self-operation Jobs to avoid scheduling on maintenance targets
// or the operator leader node. conductor-schema.md §13.
func jobSpecWithExclusions(jobName, namespace, clusterName, capability string, nodeExclusions []string) *batchv1.Job {
	job := jobSpec(jobName, namespace, clusterName, capability)
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
// string (not an error) when the Lease is absent or the holder pod is not found —
// this allows reconcilers to proceed without exclusions rather than failing.
// conductor-schema.md §13, CP-INV-007.
func resolveOperatorLeaderNode(ctx context.Context, c client.Client) (string, error) {
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
	pod := &corev1.Pod{}
	if err := c.Get(ctx, types.NamespacedName{Name: podName, Namespace: "seam-system"}, pod); err != nil {
		if apierrors.IsNotFound(err) {
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

// operationalRunnerConfigName returns the name for the OperationalRunnerConfig
// created by an operational day-2 reconciler. Each CR produces exactly one
// RunnerConfig. Format: {crName} — deterministic, stable, namespace-scoped.
// conductor-schema.md §17.
func operationalRunnerConfigName(crName string) string {
	return crName
}

// buildOperationalRunnerConfig constructs an OperationalRunnerConfig CR for
// the given day-2 operation. maintenanceTargetNodes and leaderNode are stored
// on spec so the Conductor executor can exclude them from step Job scheduling.
// conductor-schema.md §13, §17.
func buildOperationalRunnerConfig(
	name, namespace, clusterRef string,
	maintenanceTargetNodes []string,
	leaderNode string,
	steps []OperationalStep,
) *OperationalRunnerConfig {
	return &OperationalRunnerConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"platform.ontai.dev/cluster": clusterRef,
			},
		},
		Spec: OperationalRunnerConfigSpec{
			ClusterRef:             clusterRef,
			RunnerImage:            conductorImage,
			MaintenanceTargetNodes: maintenanceTargetNodes,
			OperatorLeaderNode:     leaderNode,
			Steps:                  steps,
		},
	}
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

// readRunnerConfigTerminalCondition returns the terminal execution state of an
// OperationalRunnerConfig written by the Conductor executor.
// Returns (complete, failed, failedStep):
//   - complete=true, failed=false: Conductor reached Completed (all steps succeeded).
//   - complete=false, failed=true: Conductor reached Failed (failedStep has the step name).
//   - complete=false, failed=false: execution is still in progress.
//
// conductor-schema.md §17.
func readRunnerConfigTerminalCondition(rc *OperationalRunnerConfig) (complete, failed bool, failedStep string) {
	switch rc.Status.Phase {
	case "Completed":
		return true, false, ""
	case "Failed":
		return false, true, rc.Status.FailedStep
	default:
		return false, false, ""
	}
}
