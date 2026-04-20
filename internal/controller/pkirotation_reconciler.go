package controller

// PKIRotationReconciler reconciles PKIRotation CRs. It emits a RunnerConfig CR
// with a single pki-rotate step regardless of the owning TalosCluster's
// capi.enabled value — CAPI has no PKI rotation equivalent.
//
// Named Conductor capability: pki-rotate. platform-schema.md §5.
// conductor-schema.md §17 RunnerConfig Execution Model.
//
// CP-INV-010: No Kueue. RunnerConfig submitted directly.
// INV-018: gate failures are permanent — HaltOnFailure=true.

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

const capabilityPKIRotate = "pki-rotate"

// PKIRotationReconciler reconciles PKIRotation objects.
type PKIRotationReconciler struct {
	Client   client.Client
	Scheme   *runtime.Scheme
	Recorder clientevents.EventRecorder
}

// +kubebuilder:rbac:groups=platform.ontai.dev,resources=pkirotations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=pkirotations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.ontai.dev,resources=pkirotations/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=runner.ontai.dev,resources=runnerconfigs,verbs=get;list;watch;create;update;patch;delete

func (r *PKIRotationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	pkir := &platformv1alpha1.PKIRotation{}
	if err := r.Client.Get(ctx, req.NamespacedName, pkir); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get PKIRotation %s: %w", req.NamespacedName, err)
	}

	patchBase := client.MergeFrom(pkir.DeepCopy())
	defer func() {
		if err := r.Client.Status().Patch(ctx, pkir, patchBase); err != nil {
			if !apierrors.IsNotFound(err) {
				logger.Error(err, "failed to patch PKIRotation status",
					"name", pkir.Name, "namespace", pkir.Namespace)
			}
		}
	}()

	pkir.Status.ObservedGeneration = pkir.Generation

	// Initialize LineageSynced on first observation — one-time write.
	if platformv1alpha1.FindCondition(pkir.Status.Conditions, platformv1alpha1.ConditionTypeLineageSynced) == nil {
		platformv1alpha1.SetCondition(
			&pkir.Status.Conditions,
			platformv1alpha1.ConditionTypeLineageSynced,
			metav1.ConditionFalse,
			platformv1alpha1.ReasonLineageControllerAbsent,
			"InfrastructureLineageController is not yet deployed.",
			pkir.Generation,
		)
	}

	// If already complete, do nothing.
	readyCond := platformv1alpha1.FindCondition(pkir.Status.Conditions, platformv1alpha1.ConditionTypePKIRotationReady)
	if readyCond != nil && readyCond.Status == metav1.ConditionTrue {
		return ctrl.Result{}, nil
	}

	rcName := operationalRunnerConfigName(pkir.Name)

	existingRC, err := getOperationalRunnerConfig(ctx, r.Client, pkir.Namespace, rcName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("PKIRotationReconciler: check RunnerConfig: %w", err)
	}

	if existingRC == nil {
		// Resolve operator leader node. PKI rotation targets all nodes but the
		// executor Job must not run on the leader node. conductor-schema.md §13.
		leaderNode, lErr := resolveOperatorLeaderNode(ctx, r.Client)
		if lErr != nil {
			return ctrl.Result{}, fmt.Errorf("PKIRotationReconciler: resolve leader node: %w", lErr)
		}
		exclusionNodes := buildNodeExclusions(nil, leaderNode)

		steps := []OperationalStep{
			{
				Name:          "rotate",
				Capability:    capabilityPKIRotate,
				HaltOnFailure: true,
			},
		}

		rc := buildOperationalRunnerConfig(rcName, pkir.Namespace, pkir.Spec.ClusterRef.Name,
			exclusionNodes, leaderNode, steps)
		if err := controllerutil.SetControllerReference(pkir, rc, r.Scheme); err != nil {
			return ctrl.Result{}, fmt.Errorf("PKIRotationReconciler: set owner reference: %w", err)
		}
		if err := r.Client.Create(ctx, rc); err != nil {
			return ctrl.Result{}, fmt.Errorf("PKIRotationReconciler: create RunnerConfig: %w", err)
		}
		pkir.Status.JobName = rcName
		platformv1alpha1.SetCondition(
			&pkir.Status.Conditions,
			platformv1alpha1.ConditionTypePKIRotationReady,
			metav1.ConditionFalse,
			platformv1alpha1.ReasonPKIJobSubmitted,
			fmt.Sprintf("RunnerConfig %s submitted for pki-rotate.", rcName),
			pkir.Generation,
		)
		r.Recorder.Eventf(pkir, nil, "Normal", "RunnerConfigSubmitted", "RunnerConfigSubmitted",
			"Submitted RunnerConfig %s for pki-rotate", rcName)
		logger.Info("submitted PKIRotation RunnerConfig",
			"name", pkir.Name, "rcName", rcName)
		return ctrl.Result{RequeueAfter: operationalJobPollInterval}, nil
	}

	// RunnerConfig exists — check terminal condition.
	complete, failed, failedStep := readRunnerConfigTerminalCondition(existingRC)
	if failed {
		pkir.Status.OperationResult = fmt.Sprintf("RunnerConfig failed at step %q.", failedStep)
		platformv1alpha1.SetCondition(
			&pkir.Status.Conditions,
			platformv1alpha1.ConditionTypePKIRotationDegraded,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonPKIJobFailed,
			fmt.Sprintf("RunnerConfig %s failed at step %q.", rcName, failedStep),
			pkir.Generation,
		)
		r.Recorder.Eventf(pkir, nil, "Warning", "RunnerConfigFailed", "RunnerConfigFailed",
			"RunnerConfig %s failed at step %q", rcName, failedStep)
		return ctrl.Result{}, nil
	}
	if !complete {
		return ctrl.Result{RequeueAfter: operationalJobPollInterval}, nil
	}

	pkir.Status.OperationResult = "RunnerConfig completed successfully."
	platformv1alpha1.SetCondition(
		&pkir.Status.Conditions,
		platformv1alpha1.ConditionTypePKIRotationReady,
		metav1.ConditionTrue,
		platformv1alpha1.ReasonPKIJobComplete,
		fmt.Sprintf("RunnerConfig %s completed successfully.", rcName),
		pkir.Generation,
	)
	r.Recorder.Eventf(pkir, nil, "Normal", "RunnerConfigComplete", "RunnerConfigComplete",
		"RunnerConfig %s completed successfully", rcName)
	logger.Info("PKIRotation complete", "name", pkir.Name)
	return ctrl.Result{}, nil
}

// SetupWithManager registers PKIRotationReconciler with the manager.
func (r *PKIRotationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.PKIRotation{}).
		Complete(r)
}
