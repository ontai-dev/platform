package controller

// SeamInfrastructureClusterReconciler implements the CAPI InfrastructureCluster
// contract for Seam. It sets status.ready=true when all control plane
// SeamInfrastructureMachine objects in the tenant namespace are ready, and writes
// the controlPlaneEndpoint back to the owning CAPI Cluster object.
// platform-design.md §2.1, §3.
//
// CP-INV-001: This reconciler does NOT import the talos goclient. Only
// SeamInfrastructureMachineReconciler uses the talos goclient.

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	infrav1alpha1 "github.com/ontai-dev/platform/api/infrastructure/v1alpha1"
)

// SeamInfrastructureClusterReconciler reconciles SeamInfrastructureCluster objects.
type SeamInfrastructureClusterReconciler struct {
	// Client is the controller-runtime client.
	Client client.Client

	// Scheme is the runtime scheme.
	Scheme *runtime.Scheme

	// Recorder is the event recorder.
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=seaminfrastructureclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=seaminfrastructureclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=seaminfrastructuremachines,verbs=get;list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters,verbs=get;list;watch;patch

func (r *SeamInfrastructureClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	sic := &infrav1alpha1.SeamInfrastructureCluster{}
	if err := r.Client.Get(ctx, req.NamespacedName, sic); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get SeamInfrastructureCluster %s: %w", req.NamespacedName, err)
	}

	patchBase := client.MergeFrom(sic.DeepCopy())
	defer func() {
		if err := r.Client.Status().Patch(ctx, sic, patchBase); err != nil {
			if !apierrors.IsNotFound(err) {
				logger.Error(err, "failed to patch SeamInfrastructureCluster status",
					"name", sic.Name, "namespace", sic.Namespace)
			}
		}
	}()

	sic.Status.ObservedGeneration = sic.Generation

	// Initialize LineageSynced on first observation — one-time write.
	// InfrastructureLineageController takes ownership when deployed.
	// seam-core-schema.md §7 Declaration 5.
	if infrav1alpha1.FindCondition(sic.Status.Conditions, infrav1alpha1.ConditionTypeLineageSynced) == nil {
		infrav1alpha1.SetCondition(
			&sic.Status.Conditions,
			infrav1alpha1.ConditionTypeLineageSynced,
			metav1.ConditionFalse,
			infrav1alpha1.ReasonLineageControllerAbsent,
			"InfrastructureLineageController is not yet deployed.",
			sic.Generation,
		)
	}

	// If cluster is already ready, nothing to do.
	if sic.Status.Ready {
		return ctrl.Result{}, nil
	}

	// List all SeamInfrastructureMachine objects in this namespace and filter
	// to control plane role. Using in-code filtering avoids requiring a label
	// index on spec.nodeRole.
	allMachines := &infrav1alpha1.SeamInfrastructureMachineList{}
	if err := r.Client.List(ctx, allMachines, client.InNamespace(sic.Namespace)); err != nil {
		return ctrl.Result{}, fmt.Errorf("list SeamInfrastructureMachines in %s: %w", sic.Namespace, err)
	}

	var cpMachines []infrav1alpha1.SeamInfrastructureMachine
	for _, m := range allMachines.Items {
		if m.Spec.NodeRole == infrav1alpha1.NodeRoleControlPlane {
			cpMachines = append(cpMachines, m)
		}
	}

	if len(cpMachines) == 0 {
		infrav1alpha1.SetCondition(
			&sic.Status.Conditions,
			infrav1alpha1.ConditionTypeInfrastructureReady,
			metav1.ConditionFalse,
			infrav1alpha1.ReasonControlPlaneMachinesPending,
			"No control plane SeamInfrastructureMachine objects found in namespace.",
			sic.Generation,
		)
		logger.Info("no control plane machines found — requeuing",
			"name", sic.Name, "namespace", sic.Namespace)
		return ctrl.Result{RequeueAfter: capiPollInterval}, nil
	}

	notReadyCount := 0
	for _, m := range cpMachines {
		if !m.Status.Ready {
			notReadyCount++
		}
	}

	if notReadyCount > 0 {
		infrav1alpha1.SetCondition(
			&sic.Status.Conditions,
			infrav1alpha1.ConditionTypeInfrastructureReady,
			metav1.ConditionFalse,
			infrav1alpha1.ReasonControlPlaneMachinesNotReady,
			fmt.Sprintf("%d of %d control plane machines not yet ready.", notReadyCount, len(cpMachines)),
			sic.Generation,
		)
		logger.Info("control plane machines not all ready — requeuing",
			"name", sic.Name, "namespace", sic.Namespace,
			"total", len(cpMachines), "notReady", notReadyCount)
		return ctrl.Result{RequeueAfter: capiPollInterval}, nil
	}

	// All CP machines ready — set cluster ready and patch CAPI Cluster endpoint.
	sic.Status.Ready = true
	infrav1alpha1.SetCondition(
		&sic.Status.Conditions,
		infrav1alpha1.ConditionTypeInfrastructureReady,
		metav1.ConditionTrue,
		infrav1alpha1.ReasonAllControlPlaneMachinesReady,
		fmt.Sprintf("All %d control plane SeamInfrastructureMachine objects are ready.", len(cpMachines)),
		sic.Generation,
	)

	// Patch controlPlaneEndpoint into the CAPI Cluster object.
	// CAPI InfrastructureCluster contract requires this. platform-design.md §2.1.
	if err := r.patchCAPIClusterEndpoint(ctx, sic); err != nil {
		// Log but do not fail — status.ready is already set and will persist via
		// the deferred patch. The endpoint patch will be retried on next reconcile.
		logger.Error(err, "failed to patch CAPI Cluster controlPlaneEndpoint — will retry",
			"name", sic.Name, "namespace", sic.Namespace)
	}

	logger.Info("SeamInfrastructureCluster ready",
		"name", sic.Name, "namespace", sic.Namespace,
		"cpMachines", len(cpMachines),
		"endpoint", sic.Spec.ControlPlaneEndpoint.Host)
	return ctrl.Result{}, nil
}

// patchCAPIClusterEndpoint writes the controlPlaneEndpoint from the
// SeamInfrastructureCluster spec back to the owning CAPI Cluster object.
// This is required by the CAPI InfrastructureCluster contract.
func (r *SeamInfrastructureClusterReconciler) patchCAPIClusterEndpoint(
	ctx context.Context,
	sic *infrav1alpha1.SeamInfrastructureCluster,
) error {
	capiCluster := &unstructured.Unstructured{}
	capiCluster.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "cluster.x-k8s.io",
		Version: "v1beta1",
		Kind:    "Cluster",
	})
	if err := r.Client.Get(ctx, client.ObjectKey{
		Name:      sic.Name,
		Namespace: sic.Namespace,
	}, capiCluster); err != nil {
		if apierrors.IsNotFound(err) {
			// CAPI Cluster not yet visible — not an error, will retry.
			return nil
		}
		return fmt.Errorf("get CAPI Cluster %s/%s: %w", sic.Namespace, sic.Name, err)
	}

	port := sic.Spec.ControlPlaneEndpoint.Port
	if port == 0 {
		port = 6443
	}

	patch := client.MergeFrom(capiCluster.DeepCopy())
	if err := unstructured.SetNestedField(capiCluster.Object, map[string]interface{}{
		"host": sic.Spec.ControlPlaneEndpoint.Host,
		"port": int64(port),
	}, "spec", "controlPlaneEndpoint"); err != nil {
		return fmt.Errorf("set controlPlaneEndpoint on CAPI Cluster: %w", err)
	}
	return r.Client.Patch(ctx, capiCluster, patch)
}

// SetupWithManager registers SeamInfrastructureClusterReconciler with the manager.
func (r *SeamInfrastructureClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrav1alpha1.SeamInfrastructureCluster{}).
		Complete(r)
}
