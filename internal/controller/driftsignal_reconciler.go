package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
	seamcorev1alpha1 "github.com/ontai-dev/seam-core/api/v1alpha1"
)

// DriftSignalReconciler handles cluster-state DriftSignals written by conductor
// role=management when RunnerConfig or TalosCluster state drifts. For each signal
// with affectedCRRef.Kind == "InfrastructureRunnerConfig" and state == "pending",
// it requeues the TalosCluster for the affected cluster so the TalosCluster
// reconciler recreates the missing resource. T-23.
type DriftSignalReconciler struct {
	// Client is the controller-runtime client for Kubernetes API access.
	Client client.Client
}

// Reconcile reconciles a single DriftSignal. T-23.
//
// +kubebuilder:rbac:groups=infrastructure.ontai.dev,resources=driftsignals,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=infrastructure.ontai.dev,resources=infrastructuretalosclusters,verbs=get;list;watch;update;patch
func (r *DriftSignalReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrllog.FromContext(ctx).WithValues("driftsignal", req.NamespacedName)

	// Fetch the DriftSignal.
	ds := &seamcorev1alpha1.DriftSignal{}
	if err := r.Client.Get(ctx, req.NamespacedName, ds); err != nil {
		if apierrors.IsNotFound(err) {
			// Already deleted -- no action needed.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("DriftSignalReconciler: get DriftSignal %s: %w", req.NamespacedName, err)
	}

	// Only act on pending signals.
	if ds.Spec.State != seamcorev1alpha1.DriftSignalStatePending {
		return ctrl.Result{}, nil
	}

	// Only handle RunnerConfig drift -- other kinds are handled by the DriftSignalHandler
	// in conductor. T-23.
	if ds.Spec.AffectedCRRef.Kind != "InfrastructureRunnerConfig" {
		return ctrl.Result{}, nil
	}

	// Derive cluster name from the namespace: seam-tenant-{cluster}.
	clusterName := strings.TrimPrefix(req.Namespace, "seam-tenant-")
	if clusterName == req.Namespace {
		// Namespace did not have the expected prefix -- not a tenant namespace.
		log.Info("DriftSignal not in a seam-tenant-* namespace, ignoring", "namespace", req.Namespace)
		return ctrl.Result{}, nil
	}

	log.Info("handling RunnerConfig-missing DriftSignal",
		"cluster", clusterName, "correlationID", ds.Spec.CorrelationID)

	// Locate the TalosCluster for this cluster in seam-system.
	tc := &platformv1alpha1.TalosCluster{}
	if err := r.Client.Get(ctx, types.NamespacedName{
		Name:      clusterName,
		Namespace: rbacProfileNamespace, // seam-system
	}, tc); err != nil {
		if apierrors.IsNotFound(err) {
			// No TalosCluster -- signal is orphaned, advance to queued to avoid retry storms.
			log.Info("TalosCluster not found for drift signal -- marking queued to stop retries",
				"cluster", clusterName)
			return ctrl.Result{}, r.advanceDriftSignalToQueued(ctx, ds)
		}
		return ctrl.Result{}, fmt.Errorf("DriftSignalReconciler: get TalosCluster %s/seam-system: %w", clusterName, err)
	}

	// Annotate the TalosCluster with a drift-requeue timestamp so the reconciler
	// re-evaluates and recreates any missing RunnerConfig.
	patch := client.MergeFrom(tc.DeepCopy())
	if tc.Annotations == nil {
		tc.Annotations = map[string]string{}
	}
	tc.Annotations["ontai.dev/runnerconfig-drift-requeue"] = time.Now().UTC().Format(time.RFC3339)
	if err := r.Client.Patch(ctx, tc, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("DriftSignalReconciler: annotate TalosCluster %s: %w", clusterName, err)
	}

	log.Info("annotated TalosCluster for RunnerConfig-drift requeue", "cluster", clusterName)

	// Advance DriftSignal state to queued.
	return ctrl.Result{}, r.advanceDriftSignalToQueued(ctx, ds)
}

// advanceDriftSignalToQueued patches the DriftSignal spec.state to "queued".
// Uses MergePatch targeting the top-level spec field. T-23.
func (r *DriftSignalReconciler) advanceDriftSignalToQueued(ctx context.Context, ds *seamcorev1alpha1.DriftSignal) error {
	patch := map[string]interface{}{
		"spec": map[string]interface{}{
			"state": string(seamcorev1alpha1.DriftSignalStateQueued),
		},
	}
	data, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("advanceDriftSignalToQueued: marshal patch: %w", err)
	}
	if err := r.Client.Patch(
		context.Background(),
		ds,
		client.RawPatch(types.MergePatchType, data),
	); err != nil {
		return fmt.Errorf("advanceDriftSignalToQueued: patch DriftSignal %s/%s: %w",
			ds.Namespace, ds.Name, err)
	}
	return nil
}

// SetupWithManager registers DriftSignalReconciler with the controller-runtime
// manager. Watches DriftSignal objects using the seam-core typed client. T-23.
func (r *DriftSignalReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&seamcorev1alpha1.DriftSignal{}).
		Complete(r)
}

