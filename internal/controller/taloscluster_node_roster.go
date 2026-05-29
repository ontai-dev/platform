package controller

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
)

// AnnotationRefreshNodeRoster is the annotation an admin sets to trigger a full
// re-read of the live node roster and reconciliation of per-node machineconfig secrets.
// Platform clears the annotation after a successful refresh. RECON-C9.
const AnnotationRefreshNodeRoster = "platform.ontai.dev/refresh-node-roster"

// reconcileNodeRosterRefresh detects the AnnotationRefreshNodeRoster annotation
// on a TalosCluster and, when present, re-reads the live node roster via the Talos API,
// creates per-node machineconfig secrets for newly discovered nodes, marks disappeared
// nodes as decommissioned, emits a NodeRosterRefreshed Event, and clears the annotation.
//
// This is the post-import node enrollment path: after the initial import has been
// completed (RECON-A2), admins may add new nodes to an imported cluster. Setting the
// annotation triggers ONT to discover and enroll them. RECON-C9.
func (r *TalosClusterReconciler) reconcileNodeRosterRefresh(ctx context.Context, tc *platformv1alpha1.TalosCluster) error {
	if tc.Annotations == nil || tc.Annotations[AnnotationRefreshNodeRoster] != "true" {
		return nil
	}

	logger := log.FromContext(ctx)
	logger.Info("reconcileNodeRosterRefresh: annotation detected, re-reading node roster",
		"cluster", tc.Name)

	ns := importSecretsNamespace(tc.Name)

	// Step 1: discover the current live node roster from the Talos API.
	// ensureMachineConfigSecrets reads all node endpoints from the talosconfig secret
	// and creates per-node machineconfig secrets for any not yet present.
	if err := r.ensureMachineConfigSecrets(ctx, tc); err != nil {
		return fmt.Errorf("reconcileNodeRosterRefresh: re-read machine configs: %w", err)
	}

	// Step 2: build the set of node IP endpoints the Talos API just returned.
	// We derive this by listing all per-node secrets that were just written.
	// Secrets with mc-class prefix "node-" were created/confirmed in the ensureMachineConfigSecrets call.
	allSecrets := &corev1.SecretList{}
	if err := r.Client.List(ctx, allSecrets, client.InNamespace(ns),
		client.MatchingLabels{LabelMachineConfigCluster: tc.Name}); err != nil {
		return fmt.Errorf("reconcileNodeRosterRefresh: list machineconfig secrets: %w", err)
	}

	// Separate known node secrets (node-{hostname}) from base class secrets.
	// Build set of node hostnames that are currently known from live Talos roster
	// (these are in sync-status: pending or synced after ensureMachineConfigSecrets).
	liveNodeClasses := map[string]bool{}
	for i := range allSecrets.Items {
		s := &allSecrets.Items[i]
		class := s.Labels[LabelMachineConfigClass]
		if !strings.HasPrefix(class, "node-") {
			continue
		}
		status := s.Labels[LabelMachineConfigSyncStatus]
		if status != MachineConfigSyncStatusDecommissioned {
			liveNodeClasses[class] = true
		}
	}

	// Step 3: mark any per-node secret that is no longer in the live roster as decommissioned.
	// We track which ones were already decommissioned to avoid double-patching.
	newDecommissioned := 0
	newDiscovered := 0
	for i := range allSecrets.Items {
		s := &allSecrets.Items[i]
		class := s.Labels[LabelMachineConfigClass]
		if !strings.HasPrefix(class, "node-") {
			continue
		}
		if liveNodeClasses[class] {
			if s.Labels[LabelMachineConfigSyncStatus] == MachineConfigSyncStatusPending {
				newDiscovered++
			}
			continue
		}
		// Node not in live roster: mark decommissioned if not already.
		if s.Labels[LabelMachineConfigSyncStatus] == MachineConfigSyncStatusDecommissioned {
			continue
		}
		patch := client.MergeFrom(s.DeepCopy())
		if s.Labels == nil {
			s.Labels = map[string]string{}
		}
		s.Labels[LabelMachineConfigSyncStatus] = MachineConfigSyncStatusDecommissioned
		if err := r.Client.Patch(ctx, s, patch); err != nil {
			logger.Error(err, "reconcileNodeRosterRefresh: mark decommissioned",
				"secret", s.Name, "namespace", ns)
			continue
		}
		newDecommissioned++
		logger.Info("reconcileNodeRosterRefresh: marked node secret decommissioned",
			"cluster", tc.Name, "secret", s.Name)
	}

	// Step 4: emit a Normal Event on TalosCluster summarizing the refresh.
	msg := fmt.Sprintf("node roster refresh complete: %d new nodes discovered, %d nodes decommissioned",
		newDiscovered, newDecommissioned)
	r.Recorder.Eventf(tc, nil, "Normal", "NodeRosterRefreshed", "NodeRosterRefreshed", msg)
	logger.Info("reconcileNodeRosterRefresh: complete", "cluster", tc.Name,
		"newDiscovered", newDiscovered, "decommissioned", newDecommissioned)

	// Step 5: clear the annotation so this does not re-trigger on the next reconcile.
	patch := client.MergeFrom(tc.DeepCopy())
	delete(tc.Annotations, AnnotationRefreshNodeRoster)
	if err := r.Client.Patch(ctx, tc, patch); err != nil {
		return fmt.Errorf("reconcileNodeRosterRefresh: clear annotation: %w", err)
	}

	return nil
}
