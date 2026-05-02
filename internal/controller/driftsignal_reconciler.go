package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
	seamcorev1alpha1 "github.com/ontai-dev/seam-core/api/v1alpha1"
)

// DriftSignalReconciler handles cluster-state DriftSignals written by conductor role=tenant.
//
// Two signal kinds are handled:
//
//   - InfrastructureRunnerConfig (T-23): conductor detected RunnerConfig persistently absent.
//     Response: annotate TalosCluster to trigger RunnerConfig recreation.
//
//   - InfrastructureTalosCluster: conductor detected Talos version drift (out-of-band upgrade
//     on the tenant cluster). Response: patch TalosCluster.status.observedTalosVersion,
//     write a synthetic out-of-band TCOR record, bump TCOR revision epoch to observed version.
//
// conductor DriftSignalHandler skips InfrastructureTalosCluster kind signals; they are
// owned exclusively by this reconciler.
type DriftSignalReconciler struct {
	Client client.Client
}

// Reconcile reconciles a single DriftSignal.
//
// +kubebuilder:rbac:groups=infrastructure.ontai.dev,resources=driftsignals,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=infrastructure.ontai.dev,resources=infrastructuretalosclusters,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=infrastructure.ontai.dev,resources=infrastructuretalosclusteroperationresults,verbs=get;list;watch;update;patch
func (r *DriftSignalReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrllog.FromContext(ctx).WithValues("driftsignal", req.NamespacedName)

	ds := &seamcorev1alpha1.DriftSignal{}
	if err := r.Client.Get(ctx, req.NamespacedName, ds); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("DriftSignalReconciler: get DriftSignal %s: %w", req.NamespacedName, err)
	}

	if ds.Spec.State != seamcorev1alpha1.DriftSignalStatePending {
		return ctrl.Result{}, nil
	}

	clusterName := strings.TrimPrefix(req.Namespace, "seam-tenant-")
	if clusterName == req.Namespace {
		log.Info("DriftSignal not in a seam-tenant-* namespace, ignoring", "namespace", req.Namespace)
		return ctrl.Result{}, nil
	}

	switch ds.Spec.AffectedCRRef.Kind {
	case "InfrastructureRunnerConfig":
		return r.handleRunnerConfigDrift(ctx, log, ds, clusterName)
	case "InfrastructureTalosCluster":
		if strings.HasPrefix(ds.Spec.DriftReason, "kubernetes version drift") {
			return r.handleKubernetesVersionDrift(ctx, log, ds, clusterName)
		}
		return r.handleTalosVersionDrift(ctx, log, ds, clusterName)
	default:
		// Other kinds are handled by conductor DriftSignalHandler (pack drift).
		return ctrl.Result{}, nil
	}
}

// handleRunnerConfigDrift annotates the TalosCluster to trigger RunnerConfig recreation. T-23.
func (r *DriftSignalReconciler) handleRunnerConfigDrift(ctx context.Context, log logr.Logger, ds *seamcorev1alpha1.DriftSignal, clusterName string) (ctrl.Result, error) {
	log.Info("handling RunnerConfig-missing DriftSignal",
		"cluster", clusterName, "correlationID", ds.Spec.CorrelationID)

	tc, err := r.getTalosCluster(ctx, clusterName)
	if err != nil {
		return ctrl.Result{}, err
	}
	if tc == nil {
		log.Info("TalosCluster not found -- marking queued to stop retries", "cluster", clusterName)
		return ctrl.Result{}, r.advanceDriftSignalToQueued(ctx, ds)
	}

	patch := client.MergeFrom(tc.DeepCopy())
	if tc.Annotations == nil {
		tc.Annotations = map[string]string{}
	}
	tc.Annotations["ontai.dev/runnerconfig-drift-requeue"] = time.Now().UTC().Format(time.RFC3339)
	if err := r.Client.Patch(ctx, tc, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("DriftSignalReconciler: annotate TalosCluster %s: %w", clusterName, err)
	}

	log.Info("annotated TalosCluster for RunnerConfig-drift requeue", "cluster", clusterName)
	return ctrl.Result{}, r.advanceDriftSignalToQueued(ctx, ds)
}

// handleTalosVersionDrift records an out-of-band Talos version change in the TCOR and
// updates TalosCluster.status.observedTalosVersion.
//
// The observed version is embedded in ds.Spec.DriftReason as "... observed={version}".
// This function:
//  1. Parses the observed version from the drift reason.
//  2. Patches TalosCluster.status.observedTalosVersion to the observed version.
//  3. Appends a synthetic out-of-band operation record to the TCOR for the current revision
//     (documenting the change that happened outside ONT management).
//  4. Bumps the TCOR to a new revision epoch for the observed version, archiving
//     the current operations list (including the synthetic out-of-band record).
//  5. Advances the DriftSignal to state=queued.
func (r *DriftSignalReconciler) handleTalosVersionDrift(ctx context.Context, log logr.Logger, ds *seamcorev1alpha1.DriftSignal, clusterName string) (ctrl.Result, error) {
	observedVersion := extractObservedVersion(ds.Spec.DriftReason)
	if observedVersion == "" {
		log.Info("InfrastructureTalosCluster DriftSignal has no parseable observed version -- skipping",
			"cluster", clusterName, "driftReason", ds.Spec.DriftReason)
		return ctrl.Result{}, r.advanceDriftSignalToQueued(ctx, ds)
	}

	log.Info("handling Talos version drift",
		"cluster", clusterName, "observedVersion", observedVersion, "driftReason", ds.Spec.DriftReason)

	tc, err := r.getTalosCluster(ctx, clusterName)
	if err != nil {
		return ctrl.Result{}, err
	}
	if tc == nil {
		log.Info("TalosCluster not found -- marking queued to stop retries", "cluster", clusterName)
		return ctrl.Result{}, r.advanceDriftSignalToQueued(ctx, ds)
	}

	// 1. Patch TalosCluster.status.observedTalosVersion.
	if err := r.patchObservedTalosVersion(ctx, tc, observedVersion); err != nil {
		return ctrl.Result{}, fmt.Errorf("DriftSignalReconciler: patch observedTalosVersion %s: %w", clusterName, err)
	}
	log.Info("patched TalosCluster.status.observedTalosVersion", "cluster", clusterName, "version", observedVersion)

	// 2. Append a synthetic out-of-band record to the TCOR.
	if err := r.appendOutOfBandTCORRecord(ctx, clusterName, tc.Spec.TalosVersion, observedVersion); err != nil {
		return ctrl.Result{}, fmt.Errorf("DriftSignalReconciler: append out-of-band TCOR record %s: %w", clusterName, err)
	}

	// 3. Bump TCOR revision epoch to the observed version (archives current ops + synthetic record).
	if err := bumpTCORRevision(ctx, r.Client, clusterName, observedVersion); err != nil {
		return ctrl.Result{}, fmt.Errorf("DriftSignalReconciler: bump TCOR revision %s: %w", clusterName, err)
	}
	log.Info("TCOR revision bumped to observed Talos version", "cluster", clusterName, "version", observedVersion)

	// 4. Create a corrective UpgradePolicy to bring the cluster back to spec.talosVersion.
	// The talos-upgrade capability reads the UpgradePolicy from seam-tenant-{cluster} and
	// drives the Talos OS version change via the management-side executor Job. This enforces
	// the declared desired state: spec.talosVersion is the truth; out-of-band changes are reverted.
	if err := r.ensureCorrectiveUpgradePolicy(ctx, clusterName, tc.Spec.TalosVersion); err != nil {
		return ctrl.Result{}, fmt.Errorf("DriftSignalReconciler: ensure corrective UpgradePolicy %s: %w", clusterName, err)
	}
	log.Info("corrective UpgradePolicy ensured", "cluster", clusterName, "targetVersion", tc.Spec.TalosVersion)

	return ctrl.Result{}, r.advanceDriftSignalToQueued(ctx, ds)
}

// handleKubernetesVersionDrift creates a corrective kube-upgrade UpgradePolicy when
// conductor detects that node kubeletVersion disagrees with spec.kubernetesVersion.
// Unlike the Talos version drift path, there is no TCOR record and no status patch --
// the K8s version is corrected by the kubeUpgradeHandler executor Job driven by the
// UpgradePolicy. Once the upgrade converges, the K8s drift loop confirms the signal.
func (r *DriftSignalReconciler) handleKubernetesVersionDrift(ctx context.Context, log logr.Logger, ds *seamcorev1alpha1.DriftSignal, clusterName string) (ctrl.Result, error) {
	observedVersion := extractObservedVersion(ds.Spec.DriftReason)
	if observedVersion == "" {
		log.Info("K8s version drift DriftSignal has no parseable observed version -- skipping",
			"cluster", clusterName, "driftReason", ds.Spec.DriftReason)
		return ctrl.Result{}, r.advanceDriftSignalToQueued(ctx, ds)
	}

	log.Info("handling Kubernetes version drift",
		"cluster", clusterName, "observedVersion", observedVersion, "driftReason", ds.Spec.DriftReason)

	tc, err := r.getTalosCluster(ctx, clusterName)
	if err != nil {
		return ctrl.Result{}, err
	}
	if tc == nil {
		log.Info("TalosCluster not found -- marking queued to stop retries", "cluster", clusterName)
		return ctrl.Result{}, r.advanceDriftSignalToQueued(ctx, ds)
	}

	if err := r.ensureCorrectiveKubeUpgradePolicy(ctx, clusterName, tc.Spec.KubernetesVersion); err != nil {
		return ctrl.Result{}, fmt.Errorf("DriftSignalReconciler: ensure corrective kube UpgradePolicy %s: %w", clusterName, err)
	}
	log.Info("corrective kube UpgradePolicy ensured",
		"cluster", clusterName, "targetVersion", tc.Spec.KubernetesVersion)

	return ctrl.Result{}, r.advanceDriftSignalToQueued(ctx, ds)
}

// getTalosCluster fetches the TalosCluster for clusterName from seam-system. Returns nil if not found.
func (r *DriftSignalReconciler) getTalosCluster(ctx context.Context, clusterName string) (*platformv1alpha1.TalosCluster, error) {
	tc := &platformv1alpha1.TalosCluster{}
	if err := r.Client.Get(ctx, types.NamespacedName{
		Name:      clusterName,
		Namespace: rbacProfileNamespace, // seam-system
	}, tc); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get TalosCluster %s/seam-system: %w", clusterName, err)
	}
	return tc, nil
}

// patchObservedTalosVersion patches TalosCluster.status.observedTalosVersion via status subresource.
func (r *DriftSignalReconciler) patchObservedTalosVersion(ctx context.Context, tc *platformv1alpha1.TalosCluster, observedVersion string) error {
	patch := client.MergeFrom(tc.DeepCopy())
	tc.Status.ObservedTalosVersion = observedVersion
	return r.Client.Status().Patch(ctx, tc, patch)
}

// appendOutOfBandTCORRecord writes a synthetic operation record to the existing TCOR revision
// documenting the out-of-band version change. The record is keyed by a timestamp-based name so
// it does not collide with Job-based records. Called before bumpTCORRevision so the record is
// included in the archived revision.
func (r *DriftSignalReconciler) appendOutOfBandTCORRecord(ctx context.Context, clusterName, specVersion, observedVersion string) error {
	ns := tenantNS(clusterName)
	tcor := &seamcorev1alpha1.InfrastructureTalosClusterOperationResult{}
	if err := r.Client.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: ns}, tcor); err != nil {
		if apierrors.IsNotFound(err) {
			// TCOR does not exist yet -- nothing to append to; bumpTCORRevision will create it.
			return nil
		}
		return fmt.Errorf("get TCOR %s/%s: %w", ns, clusterName, err)
	}

	patch := client.MergeFrom(tcor.DeepCopy())
	if tcor.Spec.Operations == nil {
		tcor.Spec.Operations = map[string]seamcorev1alpha1.TalosClusterOperationRecord{}
	}
	now := metav1.Now()
	recordKey := fmt.Sprintf("out-of-band-%d", now.UnixNano())
	tcor.Spec.Operations[recordKey] = seamcorev1alpha1.TalosClusterOperationRecord{
		Capability:  "talos-version-drift",
		StartedAt:   &now,
		CompletedAt: &now,
		Status:      seamcorev1alpha1.TalosClusterResultSucceeded,
		Message:     fmt.Sprintf("talos version changed outside ONT management: %s -> %s", specVersion, observedVersion),
	}
	tcor.Spec.OperationCount = int64(len(tcor.Spec.Operations))
	return r.Client.Patch(ctx, tcor, patch)
}

// advanceDriftSignalToQueued patches the DriftSignal spec.state to "queued".
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

// ensureCorrectiveUpgradePolicy creates a UpgradePolicy in seam-tenant-{cluster} to bring
// the cluster back to specVersion (the declared desired state in TalosCluster.spec.talosVersion).
// Idempotent: create is skipped if the UpgradePolicy already exists.
// The UpgradePolicyReconciler picks it up and submits a talos-upgrade executor Job on the
// management cluster that drives the Talos OS version correction via the cluster talosconfig.
func (r *DriftSignalReconciler) ensureCorrectiveUpgradePolicy(ctx context.Context, clusterName, specVersion string) error {
	ns := tenantNS(clusterName)
	up := &platformv1alpha1.UpgradePolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "drift-version-" + clusterName,
			Namespace: ns,
		},
		Spec: platformv1alpha1.UpgradePolicySpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{
				Name:      clusterName,
				Namespace: rbacProfileNamespace, // seam-system -- where TalosCluster lives
			},
			UpgradeType:        platformv1alpha1.UpgradeTypeTalos,
			TargetTalosVersion: specVersion,
		},
	}
	if err := r.Client.Create(ctx, up); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create UpgradePolicy drift-version-%s: %w", clusterName, err)
	}
	return nil
}

// ensureCorrectiveKubeUpgradePolicy creates a UpgradePolicy in seam-tenant-{cluster} to
// bring the cluster Kubernetes version back to specVersion. Idempotent: create is skipped
// if the UpgradePolicy already exists. The UpgradePolicyReconciler submits a kube-upgrade
// executor Job that drives the kubelet image patch via kubeUpgradeHandler.
func (r *DriftSignalReconciler) ensureCorrectiveKubeUpgradePolicy(ctx context.Context, clusterName, specVersion string) error {
	ns := tenantNS(clusterName)
	up := &platformv1alpha1.UpgradePolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "drift-k8s-version-" + clusterName,
			Namespace: ns,
		},
		Spec: platformv1alpha1.UpgradePolicySpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{
				Name:      clusterName,
				Namespace: rbacProfileNamespace, // seam-system -- where TalosCluster lives
			},
			UpgradeType:             platformv1alpha1.UpgradeTypeKubernetes,
			TargetKubernetesVersion: specVersion,
		},
	}
	if err := r.Client.Create(ctx, up); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create UpgradePolicy drift-k8s-version-%s: %w", clusterName, err)
	}
	return nil
}

// extractObservedVersion parses the observed talos version from a driftReason string
// produced by TalosVersionDriftLoop. Format: "talos version drift: spec={x} observed={y}".
func extractObservedVersion(driftReason string) string {
	for _, part := range strings.Fields(driftReason) {
		if strings.HasPrefix(part, "observed=") {
			return strings.TrimPrefix(part, "observed=")
		}
	}
	return ""
}

// SetupWithManager registers DriftSignalReconciler with the controller-runtime manager.
func (r *DriftSignalReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&seamcorev1alpha1.DriftSignal{}).
		Complete(r)
}
