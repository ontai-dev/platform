package controller

// NodeMaintenanceReconciler reconciles NodeMaintenance CRs. It emits a
// RunnerConfig CR with a four-step execution sequence:
//
//	cordon → drain → {operation} → uncordon
//
// Named Conductor capabilities: node-cordon, node-drain, node-patch,
// hardening-apply, credential-rotate, node-uncordon.
// platform-schema.md §5 NodeMaintenance. platform-design.md §6.
// conductor-schema.md §17 RunnerConfig Execution Model.
//
// CP-INV-001: No talos goclient here. Node operations use Conductor executor.
// CP-INV-010: No Kueue. RunnerConfig submitted directly.
// INV-018: cordon and drain halt the sequence on failure (HaltOnFailure=true).

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientevents "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
)

// node maintenance Conductor capability names per conductor-schema.md.
// capabilityNodeDrain is declared in maintenancebundle_reconciler.go (same package).
const (
	capabilityNodeCordon       = "node-cordon"
	capabilityNodeUncordon     = "node-uncordon"
	capabilityNodePatch        = "node-patch"
	capabilityHardeningApply   = "hardening-apply"
	capabilityCredentialRotate = "credential-rotate"
)

// NodeMaintenanceReconciler reconciles NodeMaintenance objects.
type NodeMaintenanceReconciler struct {
	Client   client.Client
	Scheme   *runtime.Scheme
	Recorder clientevents.EventRecorder
}

// +kubebuilder:rbac:groups=platform.ontai.dev,resources=nodemaintenances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=nodemaintenances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=nodemaintenances/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=runner.ontai.dev,resources=runnerconfigs,verbs=get;list;watch;create;update;patch;delete

func (r *NodeMaintenanceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	nm := &platformv1alpha1.NodeMaintenance{}
	if err := r.Client.Get(ctx, req.NamespacedName, nm); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get NodeMaintenance %s: %w", req.NamespacedName, err)
	}

	patchBase := client.MergeFrom(nm.DeepCopy())
	defer func() {
		if err := r.Client.Status().Patch(ctx, nm, patchBase); err != nil {
			if !apierrors.IsNotFound(err) {
				logger.Error(err, "failed to patch NodeMaintenance status",
					"name", nm.Name, "namespace", nm.Namespace)
			}
		}
	}()

	nm.Status.ObservedGeneration = nm.Generation

	// Initialize LineageSynced on first observation — one-time write.
	if platformv1alpha1.FindCondition(nm.Status.Conditions, platformv1alpha1.ConditionTypeLineageSynced) == nil {
		platformv1alpha1.SetCondition(
			&nm.Status.Conditions,
			platformv1alpha1.ConditionTypeLineageSynced,
			metav1.ConditionFalse,
			platformv1alpha1.ReasonLineageControllerAbsent,
			"InfrastructureLineageController is not yet deployed.",
			nm.Generation,
		)
	}

	// If already complete, do nothing.
	readyCond := platformv1alpha1.FindCondition(nm.Status.Conditions, platformv1alpha1.ConditionTypeNodeMaintenanceReady)
	if readyCond != nil && readyCond.Status == metav1.ConditionTrue {
		return ctrl.Result{}, nil
	}

	operationCapability, err := nodeMaintenanceCapability(nm.Spec.Operation)
	if err != nil {
		platformv1alpha1.SetCondition(
			&nm.Status.Conditions,
			platformv1alpha1.ConditionTypeNodeMaintenanceDegraded,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonNodeJobFailed,
			fmt.Sprintf("unknown operation %q: %v", nm.Spec.Operation, err),
			nm.Generation,
		)
		return ctrl.Result{}, nil
	}

	rcName := operationalRunnerConfigName(nm.Name)

	existingRC, err := getOperationalRunnerConfig(ctx, r.Client, nm.Namespace, rcName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("NodeMaintenanceReconciler: check RunnerConfig: %w", err)
	}

	if existingRC == nil {
		// Resolve operator leader node and build node exclusions. conductor-schema.md §13.
		leaderNode, lErr := resolveOperatorLeaderNode(ctx, r.Client)
		if lErr != nil {
			return ctrl.Result{}, fmt.Errorf("NodeMaintenanceReconciler: resolve leader node: %w", lErr)
		}
		exclusionNodes := buildNodeExclusions(nm.Spec.TargetNodes, leaderNode)

		// Four-step sequence: cordon → drain → {operation} → uncordon.
		// cordon and drain halt the sequence on failure to prevent operating
		// on a node that was not safely drained. conductor-schema.md §17.
		steps := []OperationalStep{
			{
				Name:          "cordon",
				Capability:    capabilityNodeCordon,
				HaltOnFailure: true,
			},
			{
				Name:          "drain",
				Capability:    capabilityNodeDrain,
				DependsOn:     "cordon",
				HaltOnFailure: true,
			},
			{
				Name:          "operate",
				Capability:    operationCapability,
				DependsOn:     "drain",
				HaltOnFailure: false,
			},
			{
				Name:          "uncordon",
				Capability:    capabilityNodeUncordon,
				DependsOn:     "operate",
				HaltOnFailure: false,
			},
		}

		rc := buildOperationalRunnerConfig(rcName, nm.Namespace, nm.Spec.ClusterRef.Name,
			exclusionNodes, leaderNode, steps)
		if err := controllerutil.SetControllerReference(nm, rc, r.Scheme); err != nil {
			return ctrl.Result{}, fmt.Errorf("NodeMaintenanceReconciler: set owner reference: %w", err)
		}
		if err := r.Client.Create(ctx, rc); err != nil {
			return ctrl.Result{}, fmt.Errorf("NodeMaintenanceReconciler: create RunnerConfig: %w", err)
		}
		nm.Status.JobName = rcName
		platformv1alpha1.SetCondition(
			&nm.Status.Conditions,
			platformv1alpha1.ConditionTypeNodeMaintenanceReady,
			metav1.ConditionFalse,
			platformv1alpha1.ReasonNodeJobSubmitted,
			fmt.Sprintf("RunnerConfig %s submitted (cordon→drain→%s→uncordon).", rcName, operationCapability),
			nm.Generation,
		)
		r.Recorder.Eventf(nm, nil, "Normal", "RunnerConfigSubmitted", "",
			"Submitted RunnerConfig %s for %s (4-step sequence)", rcName, operationCapability)
		logger.Info("submitted NodeMaintenance RunnerConfig",
			"name", nm.Name, "rcName", rcName, "capability", operationCapability)
		return ctrl.Result{RequeueAfter: operationalJobPollInterval}, nil
	}

	// RunnerConfig exists — check terminal condition.
	complete, failed, failedStep := readRunnerConfigTerminalCondition(existingRC)
	if failed {
		nm.Status.OperationResult = fmt.Sprintf("RunnerConfig failed at step %q.", failedStep)
		platformv1alpha1.SetCondition(
			&nm.Status.Conditions,
			platformv1alpha1.ConditionTypeNodeMaintenanceDegraded,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonNodeJobFailed,
			fmt.Sprintf("RunnerConfig %s failed at step %q.", rcName, failedStep),
			nm.Generation,
		)
		r.Recorder.Eventf(nm, nil, "Warning", "RunnerConfigFailed", "",
			"RunnerConfig %s failed at step %q", rcName, failedStep)
		return ctrl.Result{}, nil
	}
	if !complete {
		return ctrl.Result{RequeueAfter: operationalJobPollInterval}, nil
	}

	nm.Status.OperationResult = "RunnerConfig completed successfully."
	platformv1alpha1.SetCondition(
		&nm.Status.Conditions,
		platformv1alpha1.ConditionTypeNodeMaintenanceReady,
		metav1.ConditionTrue,
		platformv1alpha1.ReasonNodeJobComplete,
		fmt.Sprintf("RunnerConfig %s completed successfully.", rcName),
		nm.Generation,
	)
	r.Recorder.Eventf(nm, nil, "Normal", "RunnerConfigComplete", "",
		"RunnerConfig %s completed successfully", rcName)
	logger.Info("NodeMaintenance complete", "name", nm.Name, "capability", operationCapability)
	return ctrl.Result{}, nil
}

// nodeMaintenanceCapability maps a NodeMaintenanceOperation to the Conductor capability.
func nodeMaintenanceCapability(op platformv1alpha1.NodeMaintenanceOperation) (string, error) {
	switch op {
	case platformv1alpha1.NodeMaintenanceOperationPatch:
		return capabilityNodePatch, nil
	case platformv1alpha1.NodeMaintenanceOperationHardeningApply:
		return capabilityHardeningApply, nil
	case platformv1alpha1.NodeMaintenanceOperationCredentialRotate:
		return capabilityCredentialRotate, nil
	default:
		return "", fmt.Errorf("unknown NodeMaintenanceOperation %q", op)
	}
}

// SetupWithManager registers NodeMaintenanceReconciler with the manager.
func (r *NodeMaintenanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.NodeMaintenance{}).
		Complete(r)
}
