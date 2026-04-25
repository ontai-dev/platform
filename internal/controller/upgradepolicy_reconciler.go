package controller

// UpgradePolicyReconciler reconciles UpgradePolicy CRs. It is a dual-path reconciler
// governed by spec.capi.enabled on the owning TalosCluster:
//
//   - CAPI path (capi.enabled=true): updates TalosControlPlane version and
//     MachineDeployment rolling upgrade settings natively through CAPI machinery.
//     No Conductor Job is submitted.
//
//   - Non-CAPI path (capi.enabled=false): submits a Conductor executor Job for
//     talos-upgrade, kube-upgrade, or stack-upgrade.
//
// Named Conductor capabilities (non-CAPI): talos-upgrade, kube-upgrade, stack-upgrade.
// platform-schema.md §5 UpgradePolicy. platform-design.md §2.1.

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientevents "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
)

const (
	capabilityTalosUpgrade = "talos-upgrade"
	capabilityKubeUpgrade  = "kube-upgrade"
	capabilityStackUpgrade = "stack-upgrade"
)

// UpgradePolicyReconciler reconciles UpgradePolicy objects.
type UpgradePolicyReconciler struct {
	Client   client.Client
	Scheme   *runtime.Scheme
	Recorder clientevents.EventRecorder
}

// +kubebuilder:rbac:groups=platform.ontai.dev,resources=upgradepolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=upgradepolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=upgradepolicies/finalizers,verbs=update
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=talosclusters,verbs=get;list;watch
// +kubebuilder:rbac:groups=controlplane.cluster.x-k8s.io,resources=taloscontrolplanes,verbs=get;list;watch;patch;update
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machinedeployments,verbs=get;list;watch;patch;update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch

func (r *UpgradePolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	up := &platformv1alpha1.UpgradePolicy{}
	if err := r.Client.Get(ctx, req.NamespacedName, up); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get UpgradePolicy %s: %w", req.NamespacedName, err)
	}

	patchBase := client.MergeFrom(up.DeepCopy())
	defer func() {
		if err := r.Client.Status().Patch(ctx, up, patchBase); err != nil {
			if !apierrors.IsNotFound(err) {
				logger.Error(err, "failed to patch UpgradePolicy status",
					"name", up.Name, "namespace", up.Namespace)
			}
		}
	}()

	up.Status.ObservedGeneration = up.Generation

	// Initialize LineageSynced on first observation — one-time write.
	if platformv1alpha1.FindCondition(up.Status.Conditions, platformv1alpha1.ConditionTypeLineageSynced) == nil {
		platformv1alpha1.SetCondition(
			&up.Status.Conditions,
			platformv1alpha1.ConditionTypeLineageSynced,
			metav1.ConditionFalse,
			platformv1alpha1.ReasonLineageControllerAbsent,
			"InfrastructureLineageController is not yet deployed.",
			up.Generation,
		)
	}

	// If already complete, do nothing.
	readyCond := platformv1alpha1.FindCondition(up.Status.Conditions, platformv1alpha1.ConditionTypeUpgradePolicyReady)
	if readyCond != nil && readyCond.Status == metav1.ConditionTrue {
		return ctrl.Result{}, nil
	}

	// Read TalosCluster to determine path.
	capiEnabled, err := r.upgradeCAPIEnabled(ctx, up)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("UpgradePolicyReconciler: read TalosCluster: %w", err)
	}

	if capiEnabled {
		return r.reconcileCAPIUpgrade(ctx, up)
	}
	return r.reconcileDirectUpgrade(ctx, up)
}

// reconcileCAPIUpgrade delegates the upgrade to CAPI native machinery by patching
// the TalosControlPlane version and MachineDeployment rollout settings.
func (r *UpgradePolicyReconciler) reconcileCAPIUpgrade(ctx context.Context, up *platformv1alpha1.UpgradePolicy) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	tenantNS := "seam-tenant-" + up.Spec.ClusterRef.Name

	// Patch TalosControlPlane version for talos and stack upgrades.
	if up.Spec.UpgradeType == platformv1alpha1.UpgradeTypeTalos ||
		up.Spec.UpgradeType == platformv1alpha1.UpgradeTypeStack {
		if up.Spec.TargetTalosVersion != "" {
			if err := r.patchTalosControlPlaneVersion(ctx, tenantNS, up.Spec.ClusterRef.Name, up.Spec.TargetTalosVersion); err != nil {
				return ctrl.Result{}, fmt.Errorf("reconcileCAPIUpgrade: patch TCP version: %w", err)
			}
		}
	}

	// Patch MachineDeployment version for kubernetes and stack upgrades.
	if up.Spec.UpgradeType == platformv1alpha1.UpgradeTypeKubernetes ||
		up.Spec.UpgradeType == platformv1alpha1.UpgradeTypeStack {
		if up.Spec.TargetKubernetesVersion != "" {
			if err := r.patchMachineDeploymentVersion(ctx, tenantNS, up.Spec.ClusterRef.Name, up.Spec.TargetKubernetesVersion); err != nil {
				return ctrl.Result{}, fmt.Errorf("reconcileCAPIUpgrade: patch MD version: %w", err)
			}
		}
	}

	platformv1alpha1.SetCondition(
		&up.Status.Conditions,
		platformv1alpha1.ConditionTypeUpgradePolicyCAPIDelegated,
		metav1.ConditionTrue,
		platformv1alpha1.ReasonUpgradeCAPIDelegated,
		"Upgrade delegated to CAPI native machinery via TalosControlPlane and MachineDeployment version patch.",
		up.Generation,
	)
	platformv1alpha1.SetCondition(
		&up.Status.Conditions,
		platformv1alpha1.ConditionTypeUpgradePolicyReady,
		metav1.ConditionTrue,
		platformv1alpha1.ReasonUpgradeCAPIDelegated,
		"CAPI objects patched. Upgrade progression managed by CAPI controllers.",
		up.Generation,
	)
	r.Recorder.Eventf(up, nil, "Normal", "CAPIDelegated", "CAPIDelegated",
		"Upgrade for cluster %s delegated to CAPI", up.Spec.ClusterRef.Name)
	logger.Info("UpgradePolicy reconciled via CAPI delegation",
		"name", up.Name, "upgradeType", up.Spec.UpgradeType,
		"cluster", up.Spec.ClusterRef.Name)
	return ctrl.Result{}, nil
}

// patchTalosControlPlaneVersion patches the TalosControlPlane version field
// to trigger a rolling control plane upgrade via CAPI/CACPPT.
func (r *UpgradePolicyReconciler) patchTalosControlPlaneVersion(ctx context.Context, ns, clusterName, talosVersion string) error {
	tcp := &unstructured.Unstructured{}
	tcp.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "controlplane.cluster.x-k8s.io",
		Version: "v1alpha3",
		Kind:    "TalosControlPlane",
	})
	tcpName := clusterName + "-control-plane"
	if err := r.Client.Get(ctx, types.NamespacedName{Name: tcpName, Namespace: ns}, tcp); err != nil {
		if apierrors.IsNotFound(err) {
			return nil // CAPI objects not yet created — no-op.
		}
		return fmt.Errorf("get TalosControlPlane %s/%s: %w", ns, tcpName, err)
	}
	patch := client.MergeFrom(tcp.DeepCopy())
	if err := unstructured.SetNestedField(tcp.Object, talosVersion, "spec", "version"); err != nil {
		return fmt.Errorf("set TalosControlPlane version: %w", err)
	}
	return r.Client.Patch(ctx, tcp, patch)
}

// patchMachineDeploymentVersion patches all MachineDeployments for the cluster
// to trigger a rolling worker upgrade via CAPI.
func (r *UpgradePolicyReconciler) patchMachineDeploymentVersion(ctx context.Context, ns, clusterName, k8sVersion string) error {
	mdList := &unstructured.UnstructuredList{}
	mdList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "cluster.x-k8s.io",
		Version: "v1beta1",
		Kind:    "MachineDeploymentList",
	})
	if err := r.Client.List(ctx, mdList,
		client.InNamespace(ns),
		client.MatchingLabels{"cluster.x-k8s.io/cluster-name": clusterName},
	); err != nil {
		return fmt.Errorf("list MachineDeployments in %s: %w", ns, err)
	}
	for i := range mdList.Items {
		md := mdList.Items[i].DeepCopy()
		patch := client.MergeFrom(mdList.Items[i].DeepCopy())
		if err := unstructured.SetNestedField(md.Object, k8sVersion, "spec", "template", "spec", "version"); err != nil {
			return fmt.Errorf("set MachineDeployment %s version: %w", md.GetName(), err)
		}
		if err := r.Client.Patch(ctx, md, patch); err != nil {
			return fmt.Errorf("patch MachineDeployment %s: %w", md.GetName(), err)
		}
	}
	return nil
}

// reconcileDirectUpgrade gates on capability then submits a single batch/v1
// Conductor executor Job for the non-CAPI path. conductor-schema.md §5 §17.
func (r *UpgradePolicyReconciler) reconcileDirectUpgrade(ctx context.Context, up *platformv1alpha1.UpgradePolicy) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	capability, err := upgradeCapability(up.Spec.UpgradeType)
	if err != nil {
		platformv1alpha1.SetCondition(
			&up.Status.Conditions,
			platformv1alpha1.ConditionTypeUpgradePolicyDegraded,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonUpgradeJobFailed,
			fmt.Sprintf("unknown upgradeType %q: %v", up.Spec.UpgradeType, err),
			up.Generation,
		)
		return ctrl.Result{}, nil
	}

	// Gate: read the cluster RunnerConfig from ont-system and verify capability.
	clusterRC, err := getClusterRunnerConfig(ctx, r.Client, up.Spec.ClusterRef.Name)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("UpgradePolicyReconciler: get cluster RunnerConfig: %w", err)
	}
	if clusterRC == nil {
		platformv1alpha1.SetCondition(
			&up.Status.Conditions,
			platformv1alpha1.ConditionTypeCapabilityUnavailable,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonRunnerConfigNotFound,
			"Cluster RunnerConfig not yet present in ont-system. Waiting for Conductor agent.",
			up.Generation,
		)
		return ctrl.Result{RequeueAfter: capabilityUnavailableRetryInterval}, nil
	}
	if !hasCapability(clusterRC, capability) {
		platformv1alpha1.SetCondition(
			&up.Status.Conditions,
			platformv1alpha1.ConditionTypeCapabilityUnavailable,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonCapabilityNotPublished,
			fmt.Sprintf("Capability %q not yet published by Conductor agent.", capability),
			up.Generation,
		)
		return ctrl.Result{RequeueAfter: capabilityUnavailableRetryInterval}, nil
	}
	platformv1alpha1.SetCondition(
		&up.Status.Conditions,
		platformv1alpha1.ConditionTypeCapabilityUnavailable,
		metav1.ConditionFalse,
		platformv1alpha1.ReasonCapabilityNotPublished,
		"",
		up.Generation,
	)

	jobName := operationalJobName(up.Name, capability)

	existingJob, err := getOperationalJob(ctx, r.Client, up.Namespace, jobName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("UpgradePolicyReconciler: check job: %w", err)
	}

	if existingJob == nil {
		leaderNode, lErr := resolveOperatorLeaderNode(ctx, r.Client)
		if lErr != nil {
			return ctrl.Result{}, fmt.Errorf("UpgradePolicyReconciler: resolve leader node: %w", lErr)
		}
		nodeExclusions := buildNodeExclusions(nil, leaderNode)

		job := jobSpecWithExclusions(jobName, up.Namespace, up.Spec.ClusterRef.Name, capability, nodeExclusions, clusterRC.Spec.RunnerImage)
		if err := controllerutil.SetControllerReference(up, job, r.Scheme); err != nil {
			return ctrl.Result{}, fmt.Errorf("UpgradePolicyReconciler: set owner reference: %w", err)
		}
		if err := r.Client.Create(ctx, job); err != nil {
			return ctrl.Result{}, fmt.Errorf("UpgradePolicyReconciler: create job: %w", err)
		}
		up.Status.JobName = jobName
		platformv1alpha1.SetCondition(
			&up.Status.Conditions,
			platformv1alpha1.ConditionTypeUpgradePolicyReady,
			metav1.ConditionFalse,
			platformv1alpha1.ReasonUpgradeJobSubmitted,
			fmt.Sprintf("Conductor executor Job %s submitted for %s.", jobName, capability),
			up.Generation,
		)
		r.Recorder.Eventf(up, nil, "Normal", "JobSubmitted", "JobSubmitted",
			"Submitted Conductor executor Job %s for %s", jobName, capability)
		logger.Info("submitted upgrade Conductor executor Job",
			"name", up.Name, "jobName", jobName, "upgradeType", up.Spec.UpgradeType)
		return ctrl.Result{RequeueAfter: operationalJobPollInterval}, nil
	}

	// Job exists — check OperationResult ConfigMap.
	complete, failed, result := readOperationalResult(ctx, r.Client, up.Namespace, jobName)
	if failed {
		up.Status.OperationResult = result
		platformv1alpha1.SetCondition(
			&up.Status.Conditions,
			platformv1alpha1.ConditionTypeUpgradePolicyDegraded,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonUpgradeJobFailed,
			fmt.Sprintf("Conductor executor Job %s failed: %s", jobName, result),
			up.Generation,
		)
		r.Recorder.Eventf(up, nil, "Warning", "JobFailed", "JobFailed",
			"Conductor executor Job %s failed: %s", jobName, result)
		return ctrl.Result{}, nil
	}
	if !complete {
		return ctrl.Result{RequeueAfter: operationalJobPollInterval}, nil
	}

	up.Status.OperationResult = result
	platformv1alpha1.SetCondition(
		&up.Status.Conditions,
		platformv1alpha1.ConditionTypeUpgradePolicyReady,
		metav1.ConditionTrue,
		platformv1alpha1.ReasonUpgradeJobComplete,
		fmt.Sprintf("Conductor executor Job %s completed successfully.", jobName),
		up.Generation,
	)
	r.Recorder.Eventf(up, nil, "Normal", "JobComplete", "JobComplete",
		"Conductor executor Job %s completed successfully", jobName)
	logger.Info("UpgradePolicy complete", "name", up.Name, "upgradeType", up.Spec.UpgradeType)
	return ctrl.Result{}, nil
}

// upgradeCapability maps an UpgradeType to a single Conductor capability name.
// stack-upgrade is a named compound capability in conductor — the executor handles
// talos→kube sequencing internally. conductor-schema.md §6.
func upgradeCapability(ut platformv1alpha1.UpgradeType) (string, error) {
	switch ut {
	case platformv1alpha1.UpgradeTypeTalos:
		return capabilityTalosUpgrade, nil
	case platformv1alpha1.UpgradeTypeKubernetes:
		return capabilityKubeUpgrade, nil
	case platformv1alpha1.UpgradeTypeStack:
		return capabilityStackUpgrade, nil
	default:
		return "", fmt.Errorf("unknown UpgradeType %q", ut)
	}
}

// upgradeCAPIEnabled reads the owning TalosCluster's capi.enabled field.
func (r *UpgradePolicyReconciler) upgradeCAPIEnabled(ctx context.Context, up *platformv1alpha1.UpgradePolicy) (bool, error) {
	tc := &platformv1alpha1.TalosCluster{}
	ns := up.Spec.ClusterRef.Namespace
	if ns == "" {
		ns = up.Namespace
	}
	if err := r.Client.Get(ctx, types.NamespacedName{
		Name:      up.Spec.ClusterRef.Name,
		Namespace: ns,
	}, tc); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("get TalosCluster %s/%s: %w", ns, up.Spec.ClusterRef.Name, err)
	}
	return tc.Spec.CAPI != nil && tc.Spec.CAPI.Enabled, nil
}


// SetupWithManager registers UpgradePolicyReconciler with the manager.
func (r *UpgradePolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.UpgradePolicy{}).
		Complete(r)
}
