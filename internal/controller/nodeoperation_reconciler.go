package controller

// NodeOperationReconciler reconciles NodeOperation CRs. It is a dual-path reconciler
// governed by spec.capi.enabled on the owning TalosCluster:
//
//   - CAPI path (capi.enabled=true): modifies MachineDeployment replicas for
//     scale-up, deletes specific Machine objects for decommission, or sets the
//     Machine reboot annotation — all handled natively by CAPI.
//
//   - Non-CAPI path (capi.enabled=false): submits a Conductor executor Job for
//     node-scale-up, node-decommission, or node-reboot.
//
// Named Conductor capabilities (non-CAPI): node-scale-up, node-decommission, node-reboot.
// platform-schema.md §5 NodeOperation. platform-design.md §2.1.

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
	capabilityNodeScaleUp      = "node-scale-up"
	capabilityNodeDecommission = "node-decommission"
	capabilityNodeReboot       = "node-reboot"

	// capiRebootAnnotation is the CAPI annotation that triggers a node reboot.
	capiRebootAnnotation = "cluster.x-k8s.io/reboot"
)

// NodeOperationReconciler reconciles NodeOperation objects.
type NodeOperationReconciler struct {
	Client   client.Client
	Scheme   *runtime.Scheme
	Recorder clientevents.EventRecorder
}

// +kubebuilder:rbac:groups=platform.ontai.dev,resources=nodeoperations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=nodeoperations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=nodeoperations/finalizers,verbs=update
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=talosclusters,verbs=get;list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machinedeployments,verbs=get;list;watch;patch;update
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines,verbs=get;list;watch;delete;patch;update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch

func (r *NodeOperationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	nop := &platformv1alpha1.NodeOperation{}
	if err := r.Client.Get(ctx, req.NamespacedName, nop); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get NodeOperation %s: %w", req.NamespacedName, err)
	}

	patchBase := client.MergeFrom(nop.DeepCopy())
	defer func() {
		if err := r.Client.Status().Patch(ctx, nop, patchBase); err != nil {
			if !apierrors.IsNotFound(err) {
				logger.Error(err, "failed to patch NodeOperation status",
					"name", nop.Name, "namespace", nop.Namespace)
			}
		}
	}()

	nop.Status.ObservedGeneration = nop.Generation

	// Initialize LineageSynced on first observation — one-time write.
	if platformv1alpha1.FindCondition(nop.Status.Conditions, platformv1alpha1.ConditionTypeLineageSynced) == nil {
		platformv1alpha1.SetCondition(
			&nop.Status.Conditions,
			platformv1alpha1.ConditionTypeLineageSynced,
			metav1.ConditionFalse,
			platformv1alpha1.ReasonLineageControllerAbsent,
			"InfrastructureLineageController is not yet deployed.",
			nop.Generation,
		)
	}

	// If already complete, do nothing.
	readyCond := platformv1alpha1.FindCondition(nop.Status.Conditions, platformv1alpha1.ConditionTypeNodeOperationReady)
	if readyCond != nil && readyCond.Status == metav1.ConditionTrue {
		return ctrl.Result{}, nil
	}

	capiEnabled, err := r.nodeOpCAPIEnabled(ctx, nop)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("NodeOperationReconciler: read TalosCluster: %w", err)
	}

	if capiEnabled {
		return r.reconcileCAPINodeOp(ctx, nop)
	}
	return r.reconcileDirectNodeOp(ctx, nop)
}

// reconcileCAPINodeOp handles node operations via CAPI native machinery.
func (r *NodeOperationReconciler) reconcileCAPINodeOp(ctx context.Context, nop *platformv1alpha1.NodeOperation) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	tenantNS := "seam-tenant-" + nop.Spec.ClusterRef.Name

	switch nop.Spec.Operation {
	case platformv1alpha1.NodeOperationTypeScaleUp:
		if err := r.capiScaleUp(ctx, tenantNS, nop); err != nil {
			return ctrl.Result{}, fmt.Errorf("reconcileCAPINodeOp: scale-up: %w", err)
		}

	case platformv1alpha1.NodeOperationTypeDecommission:
		if err := r.capiDecommission(ctx, tenantNS, nop); err != nil {
			return ctrl.Result{}, fmt.Errorf("reconcileCAPINodeOp: decommission: %w", err)
		}

	case platformv1alpha1.NodeOperationTypeReboot:
		if err := r.capiReboot(ctx, tenantNS, nop); err != nil {
			return ctrl.Result{}, fmt.Errorf("reconcileCAPINodeOp: reboot: %w", err)
		}

	default:
		platformv1alpha1.SetCondition(
			&nop.Status.Conditions,
			platformv1alpha1.ConditionTypeNodeOperationDegraded,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonNodeOpJobFailed,
			fmt.Sprintf("unknown operation %q", nop.Spec.Operation),
			nop.Generation,
		)
		return ctrl.Result{}, nil
	}

	platformv1alpha1.SetCondition(
		&nop.Status.Conditions,
		platformv1alpha1.ConditionTypeNodeOperationCAPIDelegated,
		metav1.ConditionTrue,
		platformv1alpha1.ReasonNodeOpCAPIDelegated,
		"Operation delegated to CAPI native machinery.",
		nop.Generation,
	)
	platformv1alpha1.SetCondition(
		&nop.Status.Conditions,
		platformv1alpha1.ConditionTypeNodeOperationReady,
		metav1.ConditionTrue,
		platformv1alpha1.ReasonNodeOpCAPIDelegated,
		"CAPI objects updated. Operation progression managed by CAPI controllers.",
		nop.Generation,
	)
	r.Recorder.Eventf(nop, nil, "Normal", "CAPIDelegated", "CAPIDelegated",
		"NodeOperation %s for cluster %s delegated to CAPI", nop.Spec.Operation, nop.Spec.ClusterRef.Name)
	logger.Info("NodeOperation reconciled via CAPI delegation",
		"name", nop.Name, "operation", nop.Spec.Operation, "cluster", nop.Spec.ClusterRef.Name)
	return ctrl.Result{}, nil
}

// capiScaleUp patches MachineDeployment replicas to trigger CAPI scale-up.
func (r *NodeOperationReconciler) capiScaleUp(ctx context.Context, ns string, nop *platformv1alpha1.NodeOperation) error {
	mdList := &unstructured.UnstructuredList{}
	mdList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "cluster.x-k8s.io",
		Version: "v1beta1",
		Kind:    "MachineDeploymentList",
	})
	if err := r.Client.List(ctx, mdList,
		client.InNamespace(ns),
		client.MatchingLabels{"cluster.x-k8s.io/cluster-name": nop.Spec.ClusterRef.Name},
	); err != nil {
		return fmt.Errorf("list MachineDeployments in %s: %w", ns, err)
	}
	replicas := int64(nop.Spec.ReplicaCount)
	for i := range mdList.Items {
		md := mdList.Items[i].DeepCopy()
		patch := client.MergeFrom(mdList.Items[i].DeepCopy())
		if err := unstructured.SetNestedField(md.Object, replicas, "spec", "replicas"); err != nil {
			return fmt.Errorf("set MachineDeployment %s replicas: %w", md.GetName(), err)
		}
		if err := r.Client.Patch(ctx, md, patch); err != nil {
			return fmt.Errorf("patch MachineDeployment %s: %w", md.GetName(), err)
		}
	}
	return nil
}

// capiDecommission deletes specific Machine objects for the listed target nodes.
func (r *NodeOperationReconciler) capiDecommission(ctx context.Context, ns string, nop *platformv1alpha1.NodeOperation) error {
	for _, nodeName := range nop.Spec.TargetNodes {
		machine := &unstructured.Unstructured{}
		machine.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "cluster.x-k8s.io",
			Version: "v1beta1",
			Kind:    "Machine",
		})
		if err := r.Client.Get(ctx, types.NamespacedName{Name: nodeName, Namespace: ns}, machine); err != nil {
			if apierrors.IsNotFound(err) {
				continue // already gone
			}
			return fmt.Errorf("get Machine %s/%s: %w", ns, nodeName, err)
		}
		if machine.GetDeletionTimestamp() == nil {
			if err := r.Client.Delete(ctx, machine); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("delete Machine %s/%s: %w", ns, nodeName, err)
			}
		}
	}
	return nil
}

// capiReboot annotates specific Machine objects to trigger CAPI-managed reboot.
func (r *NodeOperationReconciler) capiReboot(ctx context.Context, ns string, nop *platformv1alpha1.NodeOperation) error {
	for _, nodeName := range nop.Spec.TargetNodes {
		machine := &unstructured.Unstructured{}
		machine.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "cluster.x-k8s.io",
			Version: "v1beta1",
			Kind:    "Machine",
		})
		if err := r.Client.Get(ctx, types.NamespacedName{Name: nodeName, Namespace: ns}, machine); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return fmt.Errorf("get Machine %s/%s: %w", ns, nodeName, err)
		}
		patch := client.MergeFrom(machine.DeepCopy())
		annotations := machine.GetAnnotations()
		if annotations == nil {
			annotations = make(map[string]string)
		}
		annotations[capiRebootAnnotation] = "true"
		machine.SetAnnotations(annotations)
		if err := r.Client.Patch(ctx, machine, patch); err != nil {
			return fmt.Errorf("patch Machine %s reboot annotation: %w", nodeName, err)
		}
	}
	return nil
}

// reconcileDirectNodeOp gates on capability then submits a single batch/v1
// Conductor executor Job for the non-CAPI path. conductor-schema.md §5 §17.
func (r *NodeOperationReconciler) reconcileDirectNodeOp(ctx context.Context, nop *platformv1alpha1.NodeOperation) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	capability, err := nodeOpCapability(nop.Spec.Operation)
	if err != nil {
		platformv1alpha1.SetCondition(
			&nop.Status.Conditions,
			platformv1alpha1.ConditionTypeNodeOperationDegraded,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonNodeOpJobFailed,
			fmt.Sprintf("unknown operation %q: %v", nop.Spec.Operation, err),
			nop.Generation,
		)
		return ctrl.Result{}, nil
	}

	// Gate: read the cluster RunnerConfig from ont-system and verify capability.
	clusterRC, err := getClusterRunnerConfig(ctx, r.Client, nop.Spec.ClusterRef.Name)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("NodeOperationReconciler: get cluster RunnerConfig: %w", err)
	}
	if clusterRC == nil {
		platformv1alpha1.SetCondition(
			&nop.Status.Conditions,
			platformv1alpha1.ConditionTypeCapabilityUnavailable,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonRunnerConfigNotFound,
			"Cluster RunnerConfig not yet present in ont-system. Waiting for Conductor agent.",
			nop.Generation,
		)
		return ctrl.Result{RequeueAfter: capabilityUnavailableRetryInterval}, nil
	}
	if !hasCapability(clusterRC, capability) {
		platformv1alpha1.SetCondition(
			&nop.Status.Conditions,
			platformv1alpha1.ConditionTypeCapabilityUnavailable,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonCapabilityNotPublished,
			fmt.Sprintf("Capability %q not yet published by Conductor agent.", capability),
			nop.Generation,
		)
		return ctrl.Result{RequeueAfter: capabilityUnavailableRetryInterval}, nil
	}
	platformv1alpha1.SetCondition(
		&nop.Status.Conditions,
		platformv1alpha1.ConditionTypeCapabilityUnavailable,
		metav1.ConditionFalse,
		platformv1alpha1.ReasonCapabilityNotPublished,
		"",
		nop.Generation,
	)

	jobName := operationalJobName(nop.Name, capability)

	existingJob, err := getOperationalJob(ctx, r.Client, nop.Namespace, jobName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("NodeOperationReconciler: check job: %w", err)
	}

	if existingJob == nil {
		leaderNode, lErr := resolveOperatorLeaderNode(ctx, r.Client)
		if lErr != nil {
			return ctrl.Result{}, fmt.Errorf("NodeOperationReconciler: resolve leader node: %w", lErr)
		}
		nodeExclusions := buildNodeExclusions(nop.Spec.TargetNodes, leaderNode)

		job := jobSpecWithExclusions(jobName, nop.Namespace, nop.Spec.ClusterRef.Name, capability, nodeExclusions)
		if err := controllerutil.SetControllerReference(nop, job, r.Scheme); err != nil {
			return ctrl.Result{}, fmt.Errorf("NodeOperationReconciler: set owner reference: %w", err)
		}
		if err := r.Client.Create(ctx, job); err != nil {
			return ctrl.Result{}, fmt.Errorf("NodeOperationReconciler: create job: %w", err)
		}
		nop.Status.JobName = jobName
		platformv1alpha1.SetCondition(
			&nop.Status.Conditions,
			platformv1alpha1.ConditionTypeNodeOperationReady,
			metav1.ConditionFalse,
			platformv1alpha1.ReasonNodeOpJobSubmitted,
			fmt.Sprintf("Conductor executor Job %s submitted for %s.", jobName, capability),
			nop.Generation,
		)
		r.Recorder.Eventf(nop, nil, "Normal", "JobSubmitted", "JobSubmitted",
			"Submitted Conductor executor Job %s for %s", jobName, capability)
		logger.Info("submitted NodeOperation Conductor executor Job",
			"name", nop.Name, "jobName", jobName, "capability", capability)
		return ctrl.Result{RequeueAfter: operationalJobPollInterval}, nil
	}

	// Job exists — check OperationResult ConfigMap.
	complete, failed, result := readOperationalResult(ctx, r.Client, nop.Namespace, jobName)
	if failed {
		nop.Status.OperationResult = result
		platformv1alpha1.SetCondition(
			&nop.Status.Conditions,
			platformv1alpha1.ConditionTypeNodeOperationDegraded,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonNodeOpJobFailed,
			fmt.Sprintf("Conductor executor Job %s failed: %s", jobName, result),
			nop.Generation,
		)
		r.Recorder.Eventf(nop, nil, "Warning", "JobFailed", "JobFailed",
			"Conductor executor Job %s failed: %s", jobName, result)
		return ctrl.Result{}, nil
	}
	if !complete {
		return ctrl.Result{RequeueAfter: operationalJobPollInterval}, nil
	}

	nop.Status.OperationResult = result
	platformv1alpha1.SetCondition(
		&nop.Status.Conditions,
		platformv1alpha1.ConditionTypeNodeOperationReady,
		metav1.ConditionTrue,
		platformv1alpha1.ReasonNodeOpJobComplete,
		fmt.Sprintf("Conductor executor Job %s completed successfully.", jobName),
		nop.Generation,
	)
	r.Recorder.Eventf(nop, nil, "Normal", "JobComplete", "JobComplete",
		"Conductor executor Job %s completed successfully", jobName)
	logger.Info("NodeOperation complete", "name", nop.Name, "capability", capability)
	return ctrl.Result{}, nil
}

// nodeOpCAPIEnabled reads the owning TalosCluster's capi.enabled field.
func (r *NodeOperationReconciler) nodeOpCAPIEnabled(ctx context.Context, nop *platformv1alpha1.NodeOperation) (bool, error) {
	tc := &platformv1alpha1.TalosCluster{}
	ns := nop.Spec.ClusterRef.Namespace
	if ns == "" {
		ns = nop.Namespace
	}
	if err := r.Client.Get(ctx, types.NamespacedName{
		Name:      nop.Spec.ClusterRef.Name,
		Namespace: ns,
	}, tc); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("get TalosCluster %s/%s: %w", ns, nop.Spec.ClusterRef.Name, err)
	}
	return tc.Spec.CAPIEnabled(), nil
}

// nodeOpCapability maps a NodeOperationType to the Conductor capability name.
func nodeOpCapability(op platformv1alpha1.NodeOperationType) (string, error) {
	switch op {
	case platformv1alpha1.NodeOperationTypeScaleUp:
		return capabilityNodeScaleUp, nil
	case platformv1alpha1.NodeOperationTypeDecommission:
		return capabilityNodeDecommission, nil
	case platformv1alpha1.NodeOperationTypeReboot:
		return capabilityNodeReboot, nil
	default:
		return "", fmt.Errorf("unknown NodeOperationType %q", op)
	}
}

// SetupWithManager registers NodeOperationReconciler with the manager.
func (r *NodeOperationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.NodeOperation{}).
		Complete(r)
}
