package controller

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
)

// AnnotationRefreshNodeRoster is the annotation an admin sets to trigger a full
// re-reconciliation of per-node MachineConfigSync CRs for all MachineConfig CRs
// in the cluster's tenant namespace. Platform clears the annotation after a successful
// refresh. RECON-C9.
const AnnotationRefreshNodeRoster = "platform.ontai.dev/refresh-node-roster"

// reconcileNodeRosterRefresh detects the AnnotationRefreshNodeRoster annotation
// on a TalosCluster and, when present, ensures MachineConfigSync CRs exist for all
// MachineConfig CRs in the cluster's tenant namespace, emits a NodeRosterRefreshed
// Event, and clears the annotation.
//
// This is the post-import node enrollment path: after the initial import has been
// completed, admins may add new MachineConfig CRs for newly provisioned nodes.
// Setting the annotation triggers ONT to create or refresh the corresponding
// MachineConfigSync CRs. RECON-C9.
func (r *TalosClusterReconciler) reconcileNodeRosterRefresh(ctx context.Context, tc *platformv1alpha1.TalosCluster) error {
	if tc.Annotations == nil || tc.Annotations[AnnotationRefreshNodeRoster] != "true" {
		return nil
	}

	logger := log.FromContext(ctx)
	logger.Info("reconcileNodeRosterRefresh: annotation detected, refreshing MachineConfigSync CRs",
		"cluster", tc.Name)

	ns := importSecretsNamespace(tc.Name)

	// Ensure per-node MachineConfigSync CRs exist for all MachineConfig CRs.
	if err := r.ensureMachineConfigCRsExist(ctx, tc); err != nil {
		return fmt.Errorf("reconcileNodeRosterRefresh: ensure MachineConfigSync CRs: %w", err)
	}

	// Count MachineConfig CRs for the event summary.
	mcList := &platformv1alpha1.MachineConfigList{}
	if err := r.Client.List(ctx, mcList, client.InNamespace(ns)); err != nil {
		return fmt.Errorf("reconcileNodeRosterRefresh: list MachineConfig CRs: %w", err)
	}
	count := 0
	for i := range mcList.Items {
		if mcList.Items[i].Spec.ClusterRef.Name == tc.Name {
			count++
		}
	}

	msg := fmt.Sprintf("node roster refresh complete: %d MachineConfig CRs found", count)
	r.Recorder.Eventf(tc, nil, "Normal", "NodeRosterRefreshed", "NodeRosterRefreshed", msg)
	logger.Info("reconcileNodeRosterRefresh: complete", "cluster", tc.Name, "crCount", count)

	// Clear the annotation so this does not re-trigger on the next reconcile.
	patch := client.MergeFrom(tc.DeepCopy())
	delete(tc.Annotations, AnnotationRefreshNodeRoster)
	if err := r.Client.Patch(ctx, tc, patch); err != nil {
		return fmt.Errorf("reconcileNodeRosterRefresh: clear annotation: %w", err)
	}

	return nil
}
