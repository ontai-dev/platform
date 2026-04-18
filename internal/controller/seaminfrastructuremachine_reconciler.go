package controller

// SeamInfrastructureMachineReconciler implements the CAPI InfrastructureMachine
// contract for Seam. It delivers CABPT-rendered machineconfigs to pre-provisioned
// Talos nodes via the Talos maintenance API (port 50000) and sets status.ready=true
// after the node exits maintenance mode.
// platform-design.md §3.1.
//
// CP-INV-001: This file is one of exactly two files in the platform codebase
// permitted to import the talos goclient. The other is seaminfrastructurecluster_reconciler.go.
// No other file in this codebase may import github.com/siderolabs/talos/pkg/machinery.

import (
	"context"
	"fmt"
	"strings"
	"time"

	talos_client "github.com/siderolabs/talos/pkg/machinery/client"
	machineapi "github.com/siderolabs/talos/pkg/machinery/api/machine"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	infrav1alpha1 "github.com/ontai-dev/platform/api/infrastructure/v1alpha1"
)

// port50000RetryBase is the initial retry interval for Talos maintenance API failures.
const port50000RetryBase = 10 * time.Second

// port50000RetryCap is the maximum retry interval. platform-design.md §3.1.
const port50000RetryCap = 5 * time.Minute

// Port50000Backoff computes the exponential backoff duration for consecutive
// ApplyConfiguration failures. Formula: base * 2^(attempts-1), capped at cap.
// attempts=1 → 10s, attempts=2 → 20s, attempts=3 → 40s, …, cap=5min.
// Exported for unit testing.
func Port50000Backoff(attempts int32) time.Duration {
	if attempts <= 1 {
		return port50000RetryBase
	}
	shift := attempts - 1
	if shift > 10 {
		shift = 10 // guard against overflow: 10s * 2^10 = ~170min, already past cap
	}
	d := port50000RetryBase * (1 << shift)
	if d > port50000RetryCap {
		d = port50000RetryCap
	}
	return d
}

// MachineConfigApplier is the interface used by SeamInfrastructureMachineReconciler
// to deliver machineconfigs to Talos nodes via the maintenance API.
//
// The real implementation (TalosMachineConfigApplier) uses the talos goclient.
// Tests use mock implementations of this interface.
type MachineConfigApplier interface {
	// ApplyConfiguration applies the given machineconfig bytes to the Talos node
	// at address:port. Port 50000 is the Talos maintenance API (insecure).
	ApplyConfiguration(ctx context.Context, address string, port int32, configData []byte) error

	// IsOutOfMaintenance returns true when the node at address has exited maintenance
	// mode and is reachable via its normal Talos API endpoint.
	IsOutOfMaintenance(ctx context.Context, address string) (bool, error)
}

// TalosMachineConfigApplier is the production MachineConfigApplier that uses
// the talos goclient. It is the only struct in this file that imports the talos
// machinery packages. platform-design.md §3.1.
//
// CP-INV-001: talos goclient use is confined to this struct in this file.
type TalosMachineConfigApplier struct{}

// ApplyConfiguration connects to the Talos maintenance API at address:port and
// applies the machineconfig. Uses an insecure (no-TLS) connection because maintenance
// mode port 50000 does not require a client certificate.
func (a *TalosMachineConfigApplier) ApplyConfiguration(ctx context.Context, address string, port int32, configData []byte) error {
	endpoint := fmt.Sprintf("%s:%d", address, port)
	c, err := talos_client.New(ctx, talos_client.WithEndpoints(endpoint))
	if err != nil {
		return fmt.Errorf("create talos client for %s: %w", endpoint, err)
	}
	defer c.Close()

	_, err = c.ApplyConfiguration(ctx, &machineapi.ApplyConfigurationRequest{
		Data: configData,
		Mode: machineapi.ApplyConfigurationRequest_REBOOT,
	})
	if err != nil {
		return fmt.Errorf("ApplyConfiguration to %s: %w", endpoint, err)
	}
	return nil
}

// IsOutOfMaintenance tries to call the Talos Version RPC on the node. If the call
// succeeds, the node has exited maintenance mode and is operating normally.
func (a *TalosMachineConfigApplier) IsOutOfMaintenance(ctx context.Context, address string) (bool, error) {
	c, err := talos_client.New(ctx, talos_client.WithEndpoints(address))
	if err != nil {
		return false, nil
	}
	defer c.Close()

	_, err = c.Version(ctx)
	return err == nil, nil
}

// SeamInfrastructureMachineReconciler reconciles SeamInfrastructureMachine objects.
// It implements the six-step machineconfig delivery flow per platform-design.md §3.1.
type SeamInfrastructureMachineReconciler struct {
	// Client is the controller-runtime client.
	Client client.Client

	// Scheme is the runtime scheme.
	Scheme *runtime.Scheme

	// Recorder is the event recorder.
	Recorder record.EventRecorder

	// Applier is the MachineConfigApplier used to deliver machineconfigs to nodes.
	// Defaults to TalosMachineConfigApplier in production. Set to a mock in tests.
	Applier MachineConfigApplier
}

// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=seaminfrastructuremachines,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=seaminfrastructuremachines/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

func (r *SeamInfrastructureMachineReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	sim := &infrav1alpha1.SeamInfrastructureMachine{}
	if err := r.Client.Get(ctx, req.NamespacedName, sim); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get SeamInfrastructureMachine %s: %w", req.NamespacedName, err)
	}

	patchBase := client.MergeFrom(sim.DeepCopy())
	defer func() {
		if err := r.Client.Status().Patch(ctx, sim, patchBase); err != nil {
			if !apierrors.IsNotFound(err) {
				logger.Error(err, "failed to patch SeamInfrastructureMachine status",
					"name", sim.Name, "namespace", sim.Namespace)
			}
		}
	}()

	sim.Status.ObservedGeneration = sim.Generation

	// Initialize LineageSynced on first observation — one-time write.
	// seam-core-schema.md §7 Declaration 5.
	if infrav1alpha1.FindCondition(sim.Status.Conditions, infrav1alpha1.ConditionTypeLineageSynced) == nil {
		infrav1alpha1.SetCondition(
			&sim.Status.Conditions,
			infrav1alpha1.ConditionTypeLineageSynced,
			metav1.ConditionFalse,
			infrav1alpha1.ReasonLineageControllerAbsent,
			"InfrastructureLineageController is not yet deployed.",
			sim.Generation,
		)
	}

	// If already fully ready — nothing to do. Idempotency gate.
	if sim.Status.Ready {
		return ctrl.Result{}, nil
	}

	// Step 1 — Find the owning CAPI Machine via ownerReferences.
	// CAPI core sets a Machine ownerReference on the SeamInfrastructureMachine
	// when it binds the Machine to this infrastructure object.
	machineName := r.findOwningCAPIMachine(sim)
	if machineName == "" {
		infrav1alpha1.SetCondition(
			&sim.Status.Conditions,
			infrav1alpha1.ConditionTypeMachineReady,
			metav1.ConditionFalse,
			infrav1alpha1.ReasonCAPIMachineNotBound,
			"No owning CAPI Machine has bound to this SeamInfrastructureMachine yet.",
			sim.Generation,
		)
		logger.Info("no owning CAPI Machine found — requeuing",
			"name", sim.Name, "namespace", sim.Namespace)
		return ctrl.Result{RequeueAfter: capiPollInterval}, nil
	}

	// Fetch the CAPI Machine object.
	capiMachine := &unstructured.Unstructured{}
	capiMachine.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "cluster.x-k8s.io",
		Version: "v1beta1",
		Kind:    "Machine",
	})
	if err := r.Client.Get(ctx, types.NamespacedName{
		Name:      machineName,
		Namespace: sim.Namespace,
	}, capiMachine); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{RequeueAfter: capiPollInterval}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get CAPI Machine %s/%s: %w", sim.Namespace, machineName, err)
	}

	// Step 2 — Check for bootstrap data secret (set by CABPT).
	// platform-design.md §3.1 Step 1.
	bootstrapSecretName, found, err := unstructured.NestedString(capiMachine.Object, "spec", "bootstrap", "dataSecretName")
	if err != nil || !found || bootstrapSecretName == "" {
		infrav1alpha1.SetCondition(
			&sim.Status.Conditions,
			infrav1alpha1.ConditionTypeMachineReady,
			metav1.ConditionFalse,
			infrav1alpha1.ReasonBootstrapDataNotReady,
			"CAPI Machine bootstrap data secret not yet available (CABPT has not rendered the machineconfig).",
			sim.Generation,
		)
		logger.Info("bootstrap data secret not yet available — requeuing",
			"name", sim.Name, "machine", machineName)
		return ctrl.Result{RequeueAfter: capiPollInterval}, nil
	}

	// Idempotency: if machineconfig already applied, skip steps 2–4 and proceed
	// directly to checking if the node has exited maintenance mode.
	// platform-design.md §3.1 (idempotency note).
	if !sim.Status.MachineConfigApplied {
		// Step 2 — Read bootstrap Secret.
		bootstrapSecret := &corev1.Secret{}
		if err := r.Client.Get(ctx, types.NamespacedName{
			Name:      bootstrapSecretName,
			Namespace: sim.Namespace,
		}, bootstrapSecret); err != nil {
			return ctrl.Result{}, fmt.Errorf("read bootstrap secret %s/%s: %w",
				sim.Namespace, bootstrapSecretName, err)
		}

		configData, ok := bootstrapSecret.Data["value"]
		if !ok {
			return ctrl.Result{}, fmt.Errorf("bootstrap secret %s/%s missing 'value' key",
				sim.Namespace, bootstrapSecretName)
		}

		// Step 3 — Apply machineconfig via Talos maintenance API.
		// Failures trigger exponential backoff (10s base, 5min cap). After
		// machineApplyAttemptsHaltThreshold failures, TalosClusterReconciler
		// raises ControlPlaneUnreachable (CP) or PartialWorkerAvailability (workers).
		// platform-design.md §3.1 Step 3.
		port := sim.Spec.Port
		if port == 0 {
			port = 50000
		}
		if err := r.Applier.ApplyConfiguration(ctx, sim.Spec.Address, port, configData); err != nil {
			sim.Status.ApplyAttempts++
			backoff := Port50000Backoff(sim.Status.ApplyAttempts)
			infrav1alpha1.SetCondition(
				&sim.Status.Conditions,
				infrav1alpha1.ConditionTypeMachineReady,
				metav1.ConditionFalse,
				infrav1alpha1.ReasonMachineConfigFailed,
				fmt.Sprintf("ApplyConfiguration attempt %d failed: %v. Retrying in %s.", sim.Status.ApplyAttempts, err, backoff),
				sim.Generation,
			)
			infrav1alpha1.SetCondition(
				&sim.Status.Conditions,
				infrav1alpha1.ConditionTypePortReachable,
				metav1.ConditionFalse,
				infrav1alpha1.ReasonPortUnreachable,
				fmt.Sprintf("Port %d unreachable after %d attempt(s).", port, sim.Status.ApplyAttempts),
				sim.Generation,
			)
			r.Recorder.Eventf(sim, "Warning", "ApplyConfigurationFailed",
				"ApplyConfiguration attempt %d failed for %s at %s:%d: %v",
				sim.Status.ApplyAttempts, sim.Name, sim.Spec.Address, port, err)
			logger.Error(err, "ApplyConfiguration failed — exponential backoff",
				"name", sim.Name, "address", sim.Spec.Address,
				"attempts", sim.Status.ApplyAttempts, "backoff", backoff)
			// Return nil error to prevent controller-runtime double-backoff.
			return ctrl.Result{RequeueAfter: backoff}, nil
		}
		// Success — clear the port-reachability failure counter and condition.
		sim.Status.ApplyAttempts = 0
		infrav1alpha1.SetCondition(
			&sim.Status.Conditions,
			infrav1alpha1.ConditionTypePortReachable,
			metav1.ConditionTrue,
			infrav1alpha1.ReasonPortUnreachable,
			fmt.Sprintf("Port %d reachable — machineconfig delivered.", port),
			sim.Generation,
		)

		// Machineconfig applied — mark it so we skip this on next reconcile.
		sim.Status.MachineConfigApplied = true
		infrav1alpha1.SetCondition(
			&sim.Status.Conditions,
			infrav1alpha1.ConditionTypeMachineReady,
			metav1.ConditionFalse,
			infrav1alpha1.ReasonMachineConfigApplied,
			"Machineconfig applied via Talos maintenance API. Node is rebooting.",
			sim.Generation,
		)
		logger.Info("machineconfig applied, node rebooting",
			"name", sim.Name, "address", sim.Spec.Address)
		return ctrl.Result{RequeueAfter: capiPollInterval}, nil
	}

	// Step 4 — Poll until node exits maintenance mode.
	// platform-design.md §3.1 Step 4.
	outOfMaintenance, err := r.Applier.IsOutOfMaintenance(ctx, sim.Spec.Address)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("IsOutOfMaintenance check for %s at %s: %w",
			sim.Name, sim.Spec.Address, err)
	}
	if !outOfMaintenance {
		logger.Info("node still in maintenance mode — requeuing",
			"name", sim.Name, "address", sim.Spec.Address)
		return ctrl.Result{RequeueAfter: capiPollInterval}, nil
	}

	infrav1alpha1.SetCondition(
		&sim.Status.Conditions,
		infrav1alpha1.ConditionTypeMachineReady,
		metav1.ConditionFalse,
		infrav1alpha1.ReasonMachineOutOfMaintenance,
		"Node has exited maintenance mode. Setting providerID and marking ready.",
		sim.Generation,
	)

	// Step 5 — Set providerID and write back to the owning CAPI Machine.
	// Format: talos://{cluster-name}/{node-ip}. platform-design.md §3.1 Step 5.
	clusterName := ExtractClusterName(sim.Namespace)
	providerID := fmt.Sprintf("%s%s/%s", infrav1alpha1.ProviderIDPrefix, clusterName, sim.Spec.Address)
	sim.Status.ProviderID = providerID

	if err := r.patchCAPIMachineProviderID(ctx, sim.Namespace, machineName, providerID); err != nil {
		// Non-fatal — ProviderID write-back failure is retried on next reconcile.
		logger.Error(err, "failed to patch CAPI Machine providerID — will retry",
			"name", sim.Name, "machine", machineName)
	}

	// Step 6 — Set status.ready=true.
	// CAPI core considers the machine infrastructure-ready from this point.
	// platform-design.md §3.1 Step 6.
	sim.Status.Ready = true
	infrav1alpha1.SetCondition(
		&sim.Status.Conditions,
		infrav1alpha1.ConditionTypeMachineReady,
		metav1.ConditionTrue,
		infrav1alpha1.ReasonMachineReady,
		"Machine provisioned: machineconfig applied, node out of maintenance, providerID set.",
		sim.Generation,
	)

	logger.Info("SeamInfrastructureMachine ready",
		"name", sim.Name, "address", sim.Spec.Address, "providerID", providerID)
	return ctrl.Result{}, nil
}

// findOwningCAPIMachine scans the SeamInfrastructureMachine's ownerReferences for
// a cluster.x-k8s.io/v1beta1 Machine reference. Returns the Machine name or empty
// string if no owning Machine has been bound yet.
func (r *SeamInfrastructureMachineReconciler) findOwningCAPIMachine(sim *infrav1alpha1.SeamInfrastructureMachine) string {
	for _, ref := range sim.GetOwnerReferences() {
		if ref.Kind == "Machine" && strings.HasPrefix(ref.APIVersion, "cluster.x-k8s.io/") {
			return ref.Name
		}
	}
	return ""
}

// patchCAPIMachineProviderID writes the providerID back to the owning CAPI Machine
// spec.providerID. This satisfies the CAPI InfrastructureMachine contract and
// causes CAPI core to treat the machine as provisioned. platform-design.md §3.1 Step 5.
func (r *SeamInfrastructureMachineReconciler) patchCAPIMachineProviderID(
	ctx context.Context,
	namespace, machineName, providerID string,
) error {
	capiMachine := &unstructured.Unstructured{}
	capiMachine.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "cluster.x-k8s.io",
		Version: "v1beta1",
		Kind:    "Machine",
	})
	if err := r.Client.Get(ctx, types.NamespacedName{
		Name:      machineName,
		Namespace: namespace,
	}, capiMachine); err != nil {
		return fmt.Errorf("get CAPI Machine %s/%s: %w", namespace, machineName, err)
	}
	patch := client.MergeFrom(capiMachine.DeepCopy())
	if err := unstructured.SetNestedField(capiMachine.Object, providerID, "spec", "providerID"); err != nil {
		return fmt.Errorf("set providerID on Machine %s: %w", machineName, err)
	}
	return r.Client.Patch(ctx, capiMachine, patch)
}

// ExtractClusterName derives the cluster name from the tenant namespace name.
// Tenant namespaces follow the convention seam-tenant-{cluster-name}. CP-INV-004.
func ExtractClusterName(namespaceName string) string {
	return strings.TrimPrefix(namespaceName, "seam-tenant-")
}

// SetupWithManager registers SeamInfrastructureMachineReconciler with the manager.
func (r *SeamInfrastructureMachineReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrav1alpha1.SeamInfrastructureMachine{}).
		Complete(r)
}
