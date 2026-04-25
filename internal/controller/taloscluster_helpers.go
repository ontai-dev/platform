package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
	"github.com/ontai-dev/seam-core/pkg/lineage"
)

const (
	// bootstrapPollInterval is the requeue interval while waiting for a bootstrap Job.
	bootstrapPollInterval = 15 * time.Second

	// capiPollInterval is the requeue interval while waiting for CAPI status transitions.
	capiPollInterval = 20 * time.Second

	// bootstrapCapability is the Conductor executor capability for cluster bootstrap.
	// Must match runnerlib.CapabilityBootstrap = "bootstrap". conductor-schema.md §6.
	// WS2: was incorrectly "cluster-bootstrap" — corrected to "bootstrap".
	bootstrapCapability = "bootstrap"

	// operationResultConfigMapSuffix is appended to the job name to form the
	// OperationResult ConfigMap name.
	operationResultConfigMapSuffix = "-result"

	// tenantNamespaceLabel is the namespace label applied to all tenant namespaces.
	tenantNamespaceLabel = "ontai.dev/tenant"

	// clusterNamespaceLabel is the namespace label applied to identify the cluster.
	clusterNamespaceLabel = "ontai.dev/cluster"

	// conductorImageName is the base image name for the Conductor binary.
	// conductor-schema.md §3.
	conductorImageName = "conductor"

	// devRevision is the revision suffix used for lab/development conductor images.
	// Stable releases use v{talosVersion}-r{N}. Dev builds use {talosVersion}-dev.
	// conductor-schema.md §3, INV-011.
	devRevision = "dev"

	// conductorRegistryEnv is the env var name for overriding the conductor image registry.
	conductorRegistryEnv = "CONDUCTOR_REGISTRY"

	// conductorRegistryDefault is the default conductor image registry (lab local registry).
	// INV-011: lab tags never enter the public registry.
	conductorRegistryDefault = "10.20.0.1:5000/ontai-dev"
)

// errTalosVersionRequired is returned by ensureBootstrapRunnerConfig when
// TalosCluster.Spec.TalosVersion is empty. The caller must return ctrl.Result{}
// without error — the PhaseFailed condition is already written to tc.Status.
var errTalosVersionRequired = errors.New("spec.talosVersion is required for conductor image derivation")

// bootstrapJobName returns the Kubernetes Job name for the bootstrap Job of a
// given TalosCluster.
func bootstrapJobName(clusterName string) string {
	return fmt.Sprintf("%s-bootstrap", clusterName)
}

// bootstrapRunnerConfigNamespace is the namespace where management cluster
// RunnerConfig CRs are created and where Conductor looks them up.
// conductor-schema.md §17, platform-schema.md §3.
const bootstrapRunnerConfigNamespace = "ont-system"

// finalizerRunnerConfigCleanup is placed on TalosCluster objects that carry the
// ontai.dev/owns-runnerconfig=true annotation so the RunnerConfig in ont-system
// is deleted before the TalosCluster is garbage-collected. Bug 3.
const finalizerRunnerConfigCleanup = "platform.ontai.dev/runnerconfig-cleanup"

// finalizerTenantNamespaceCleanup is placed on CAPI-enabled TalosCluster objects
// so the seam-tenant-{name} namespace is deleted before the TalosCluster is
// garbage-collected. Cross-namespace ownerReferences are not supported by the
// Kubernetes GC controller; a finalizer is required. PLATFORM-BL-TENANT-GC.
const finalizerTenantNamespaceCleanup = "platform.ontai.dev/tenant-namespace-cleanup"

// bootstrapRunnerConfigName returns the name of the RunnerConfig for a management
// cluster bootstrap. The name is the cluster name exactly — Conductor resolves the
// RunnerConfig by the value of its --cluster flag, which equals TalosCluster.Name.
// runner.ontai.dev/v1alpha1 RunnerConfig. conductor-schema.md §17.
func bootstrapRunnerConfigName(clusterName string) string {
	return clusterName
}

// getBootstrapRunnerConfig returns the RunnerConfig for this TalosCluster from
// bootstrapRunnerConfigNamespace (ont-system), or nil if it does not yet exist.
func (r *TalosClusterReconciler) getBootstrapRunnerConfig(ctx context.Context, clusterName string) (*OperationalRunnerConfig, error) {
	return getOperationalRunnerConfig(ctx, r.Client, bootstrapRunnerConfigNamespace, bootstrapRunnerConfigName(clusterName))
}

// ensureBootstrapRunnerConfig creates the RunnerConfig CR in bootstrapRunnerConfigNamespace
// (ont-system) for a management cluster bootstrap or import if it does not already exist.
// Name equals TalosCluster.Name so Conductor can locate it by cluster-ref flag value.
// RunnerImage is derived from tc.Spec.TalosVersion per INV-012 and conductor-schema.md §3:
//
//	{CONDUCTOR_REGISTRY}/{conductorImageName}:{talosVersion}-dev
//
// If TalosVersion is empty, sets ConditionTypePhaseFailed on tc and returns
// errTalosVersionRequired — the caller must return ctrl.Result{}, nil.
// Idempotent — returns nil when RunnerConfig already present.
// platform-schema.md §3, CP-INV-003.
func (r *TalosClusterReconciler) ensureBootstrapRunnerConfig(ctx context.Context, tc *platformv1alpha1.TalosCluster) error {
	if tc.Spec.TalosVersion == "" {
		platformv1alpha1.SetCondition(
			&tc.Status.Conditions,
			platformv1alpha1.ConditionTypePhaseFailed,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonTalosVersionRequired,
			"spec.talosVersion is required: the conductor image is derived from the cluster's Talos version (INV-012). Set spec.talosVersion to proceed.",
			tc.Generation,
		)
		return errTalosVersionRequired
	}

	registry := os.Getenv(conductorRegistryEnv)
	if registry == "" {
		registry = conductorRegistryDefault
	}
	runnerImage := fmt.Sprintf("%s/%s:%s-%s", registry, conductorImageName, tc.Spec.TalosVersion, devRevision)

	name := bootstrapRunnerConfigName(tc.Name)
	rc := &OperationalRunnerConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: bootstrapRunnerConfigNamespace,
			Labels: map[string]string{
				"platform.ontai.dev/cluster": tc.Name,
			},
		},
		Spec: OperationalRunnerConfigSpec{
			ClusterRef:  tc.Name,
			RunnerImage: runnerImage,
			Steps: []OperationalStep{
				{
					Name:          "enable",
					Capability:    bootstrapCapability,
					HaltOnFailure: true,
					Parameters: map[string]string{
						"cluster": tc.Name,
					},
				},
			},
		},
	}
	// Wire descendant lineage so the DescendantReconciler can append this RunnerConfig
	// to the TalosCluster's InfrastructureLineageIndex. The ILI is in tc.Namespace
	// (seam-system) while the RunnerConfig is in ont-system; the explicit ILI namespace
	// label enables the cross-namespace lookup. seam-core-schema.md §3.
	lineage.SetDescendantLabels(rc, lineage.IndexName("TalosCluster", tc.Name), tc.Namespace, "platform", lineage.ConductorAssignment, tc.GetAnnotations()[lineage.AnnotationDeclaringPrincipal])
	if err := r.Client.Create(ctx, rc); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("ensureBootstrapRunnerConfig: create RunnerConfig %s/%s: %w",
			bootstrapRunnerConfigNamespace, name, err)
	}
	return nil
}

// getBootstrapJob returns the bootstrap Job for a TalosCluster if it exists,
// or nil if it has not been created yet.
func (r *TalosClusterReconciler) getBootstrapJob(ctx context.Context, namespace, jobName string) (*batchv1.Job, error) {
	job := &batchv1.Job{}
	if err := r.Client.Get(ctx, types.NamespacedName{Name: jobName, Namespace: namespace}, job); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get bootstrap job %s/%s: %w", namespace, jobName, err)
	}
	return job, nil
}

// submitBootstrapJob creates the bootstrap Conductor Job for a management cluster
// TalosCluster (capi.enabled=false). The job mounts the bootstrap secrets from
// ont-system and runs the cluster-bootstrap capability in executor mode.
// platform-design.md §5.
func (r *TalosClusterReconciler) submitBootstrapJob(ctx context.Context, tc *platformv1alpha1.TalosCluster, jobName string) error {
	ttlSeconds := int32(600)
	backoffLimit := int32(0) // INV-018: gate failures are permanent, no retry.
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: tc.Namespace,
			Labels: map[string]string{
				"platform.ontai.dev/cluster":    tc.Name,
				"platform.ontai.dev/capability": bootstrapCapability,
			},
		},
		Spec: batchv1.JobSpec{
			TTLSecondsAfterFinished: &ttlSeconds,
			BackoffLimit:            &backoffLimit,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"platform.ontai.dev/cluster":    tc.Name,
						"platform.ontai.dev/capability": bootstrapCapability,
					},
				},
				Spec: corev1.PodSpec{
					RestartPolicy:      corev1.RestartPolicyNever,
					ServiceAccountName: "platform-executor",
					Containers: []corev1.Container{
						{
							// Image is resolved at runtime from RunnerConfig.
							// This is a placeholder — the conductor image is pulled
							// from the cluster's RunnerConfig.conductorImage field.
							// TODO: read image from RunnerConfig when RunnerConfig
							// CRD transfer to seam-core is complete (SC-INV-002).
							Name:  "executor",
							Image: "registry.ontai.dev/ontai-dev/conductor:latest",
							Args: []string{
								"execute",
								"--capability", bootstrapCapability,
								"--cluster", tc.Name,
							},
							Env: []corev1.EnvVar{
								{
									Name:  "CLUSTER_NAME",
									Value: tc.Name,
								},
								{
									Name:  "CLUSTER_NAMESPACE",
									Value: tc.Namespace,
								},
							},
						},
					},
				},
			},
		},
	}

	// Set TalosCluster as the owner so the Job is garbage-collected with it. INV-006.
	if err := controllerutil.SetControllerReference(tc, job, r.Scheme); err != nil {
		return fmt.Errorf("submitBootstrapJob: set owner reference: %w", err)
	}

	if err := r.Client.Create(ctx, job); err != nil {
		return fmt.Errorf("submitBootstrapJob: create job %s/%s: %w", tc.Namespace, jobName, err)
	}
	return nil
}

// readOperationResult checks for the OperationResult ConfigMap written by the
// bootstrap Conductor executor. Returns (complete, failed, message).
func (r *TalosClusterReconciler) readOperationResult(ctx context.Context, namespace, jobName string) (complete, failed bool, message string) {
	cmName := jobName + operationResultConfigMapSuffix
	cm := &corev1.ConfigMap{}
	if err := r.Client.Get(ctx, types.NamespacedName{Name: cmName, Namespace: namespace}, cm); err != nil {
		// ConfigMap not yet written — job still running.
		return false, false, ""
	}
	status := cm.Data["status"]
	msg := cm.Data["message"]
	switch status {
	case "success":
		return true, false, msg
	case "failure", "failed":
		return false, true, msg
	default:
		return false, false, msg
	}
}

// ensureTenantNamespace creates the seam-tenant-{cluster-name} namespace if it
// does not exist. Platform is the sole namespace creation authority. CP-INV-004.
// platform-design.md §7.
func (r *TalosClusterReconciler) ensureTenantNamespace(ctx context.Context, tc *platformv1alpha1.TalosCluster) error {
	ns := &corev1.Namespace{}
	nsName := "seam-tenant-" + tc.Name
	if err := r.Client.Get(ctx, types.NamespacedName{Name: nsName}, ns); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("ensureTenantNamespace: get namespace %s: %w", nsName, err)
		}
		// Namespace does not exist — create it with the authoritative labels.
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: nsName,
				Labels: map[string]string{
					tenantNamespaceLabel:   tc.Namespace, // using TalosCluster namespace as tenant ID
					clusterNamespaceLabel: tc.Name,
				},
			},
		}
		if err := r.Client.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("ensureTenantNamespace: create namespace %s: %w", nsName, err)
		}
	}
	return nil
}

// ensureSeamInfrastructureCluster creates the SeamInfrastructureCluster CR in
// the tenant namespace if it does not exist. Owned by TalosCluster. CP-INV-008.
// platform-schema.md §4.
func (r *TalosClusterReconciler) ensureSeamInfrastructureCluster(ctx context.Context, tc *platformv1alpha1.TalosCluster) error {
	nsName := "seam-tenant-" + tc.Name
	sic := &unstructured.Unstructured{}
	sic.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "infrastructure.cluster.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "SeamInfrastructureCluster",
	})
	if err := r.Client.Get(ctx, types.NamespacedName{Name: tc.Name, Namespace: nsName}, sic); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("ensureSeamInfrastructureCluster: get: %w", err)
		}
		// Create SeamInfrastructureCluster.
		sic = &unstructured.Unstructured{}
		sic.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "infrastructure.cluster.x-k8s.io",
			Version: "v1alpha1",
			Kind:    "SeamInfrastructureCluster",
		})
		sic.SetName(tc.Name)
		sic.SetNamespace(nsName)

		// Set ownerReference to TalosCluster. CP-INV-008.
		ownerRef := metav1.OwnerReference{
			APIVersion:         platformv1alpha1.GroupVersion.String(),
			Kind:               "TalosCluster",
			Name:               tc.Name,
			UID:                tc.UID,
			Controller:         boolPtr(true),
			BlockOwnerDeletion: boolPtr(true),
		}
		sic.SetOwnerReferences([]metav1.OwnerReference{ownerRef})

		// controlPlaneEndpoint is derived from the first control plane
		// SeamInfrastructureMachine address. Placeholder until SIM types are defined.
		// TODO: read controlPlaneEndpoint from TalosControlPlane spec.endpointVIP.
		if err := unstructured.SetNestedField(sic.Object, map[string]interface{}{
			"host": "",
			"port": int64(6443),
		}, "spec", "controlPlaneEndpoint"); err != nil {
			return fmt.Errorf("ensureSeamInfrastructureCluster: set controlPlaneEndpoint: %w", err)
		}

		lineage.SetDescendantLabels(sic, lineage.IndexName("TalosCluster", tc.Name), tc.Namespace, "platform", lineage.ClusterProvision, tc.GetAnnotations()[lineage.AnnotationDeclaringPrincipal])
		if err := r.Client.Create(ctx, sic); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("ensureSeamInfrastructureCluster: create: %w", err)
		}
	}
	return nil
}

// ensureCAPICluster creates the CAPI Cluster object in the tenant namespace if
// it does not exist. Owned by TalosCluster. CP-INV-008.
func (r *TalosClusterReconciler) ensureCAPICluster(ctx context.Context, tc *platformv1alpha1.TalosCluster) error {
	nsName := "seam-tenant-" + tc.Name
	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "cluster.x-k8s.io",
		Version: "v1beta1",
		Kind:    "Cluster",
	})
	if err := r.Client.Get(ctx, types.NamespacedName{Name: tc.Name, Namespace: nsName}, cluster); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("ensureCAPICluster: get: %w", err)
		}
		cluster = &unstructured.Unstructured{}
		cluster.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "cluster.x-k8s.io",
			Version: "v1beta1",
			Kind:    "Cluster",
		})
		cluster.SetName(tc.Name)
		cluster.SetNamespace(nsName)

		ownerRef := metav1.OwnerReference{
			APIVersion:         platformv1alpha1.GroupVersion.String(),
			Kind:               "TalosCluster",
			Name:               tc.Name,
			UID:                tc.UID,
			Controller:         boolPtr(true),
			BlockOwnerDeletion: boolPtr(true),
		}
		cluster.SetOwnerReferences([]metav1.OwnerReference{ownerRef})

		// InfrastructureRef points to the SeamInfrastructureCluster.
		if err := unstructured.SetNestedField(cluster.Object, map[string]interface{}{
			"apiVersion": "infrastructure.cluster.x-k8s.io/v1alpha1",
			"kind":       "SeamInfrastructureCluster",
			"name":       tc.Name,
			"namespace":  nsName,
		}, "spec", "infrastructureRef"); err != nil {
			return fmt.Errorf("ensureCAPICluster: set infrastructureRef: %w", err)
		}

		// ControlPlaneRef points to TalosControlPlane (CACPPT).
		if err := unstructured.SetNestedField(cluster.Object, map[string]interface{}{
			"apiVersion": "controlplane.cluster.x-k8s.io/v1alpha3",
			"kind":       "TalosControlPlane",
			"name":       tc.Name + "-control-plane",
			"namespace":  nsName,
		}, "spec", "controlPlaneRef"); err != nil {
			return fmt.Errorf("ensureCAPICluster: set controlPlaneRef: %w", err)
		}

		lineage.SetDescendantLabels(cluster, lineage.IndexName("TalosCluster", tc.Name), tc.Namespace, "platform", lineage.ClusterProvision, tc.GetAnnotations()[lineage.AnnotationDeclaringPrincipal])
		if err := r.Client.Create(ctx, cluster); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("ensureCAPICluster: create: %w", err)
		}
	}
	return nil
}

// ensureTalosConfigTemplate creates the TalosConfigTemplate (CABPT) in the
// tenant namespace. Every template must include CNI=none and Cilium BPF params.
// CP-INV-009.
func (r *TalosClusterReconciler) ensureTalosConfigTemplate(ctx context.Context, tc *platformv1alpha1.TalosCluster) error {
	nsName := "seam-tenant-" + tc.Name
	tmplName := tc.Name + "-config-template"
	tct := &unstructured.Unstructured{}
	tct.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "bootstrap.cluster.x-k8s.io",
		Version: "v1alpha3",
		Kind:    "TalosConfigTemplate",
	})
	if err := r.Client.Get(ctx, types.NamespacedName{Name: tmplName, Namespace: nsName}, tct); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("ensureTalosConfigTemplate: get: %w", err)
		}
		tct = &unstructured.Unstructured{}
		tct.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "bootstrap.cluster.x-k8s.io",
			Version: "v1alpha3",
			Kind:    "TalosConfigTemplate",
		})
		tct.SetName(tmplName)
		tct.SetNamespace(nsName)

		ownerRef := metav1.OwnerReference{
			APIVersion:         platformv1alpha1.GroupVersion.String(),
			Kind:               "TalosCluster",
			Name:               tc.Name,
			UID:                tc.UID,
			Controller:         boolPtr(true),
			BlockOwnerDeletion: boolPtr(true),
		}
		tct.SetOwnerReferences([]metav1.OwnerReference{ownerRef})

		// CP-INV-009: CNI=none is mandatory. Cilium BPF kernel parameters required.
		// platform-design.md §3.2.
		// net.core.bpf_jit_harden=0: disable JIT hardening so Cilium BPF programs are
		//   not blocked by the kernel JIT hardening security gate.
		// kernel.unprivileged_bpf_disabled=0: allow non-privileged BPF, required for
		//   Cilium's host networking and L3/L4 policy enforcement datapath.
		machineConfigPatches := []interface{}{
			map[string]interface{}{
				"op":    "replace",
				"path":  "/cluster/network/cni/name",
				"value": "none",
			},
			// Cilium-required BPF kernel parameters. CP-INV-009.
			map[string]interface{}{
				"op":    "add",
				"path":  "/machine/sysctls",
				"value": map[string]interface{}{
					"net.core.bpf_jit_harden":       "0",
					"kernel.unprivileged_bpf_disabled": "0",
				},
			},
		}
		if err := unstructured.SetNestedField(tct.Object, map[string]interface{}{
			"generateType": "worker",
			"talosVersion": tc.Spec.CAPI.TalosVersion,
			"configPatches": machineConfigPatches,
		}, "spec", "template", "spec"); err != nil {
			return fmt.Errorf("ensureTalosConfigTemplate: set spec: %w", err)
		}

		if err := r.Client.Create(ctx, tct); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("ensureTalosConfigTemplate: create: %w", err)
		}
	}
	return nil
}

// ensureTalosControlPlane creates the TalosControlPlane (CACPPT) in the tenant
// namespace if it does not exist.
func (r *TalosClusterReconciler) ensureTalosControlPlane(ctx context.Context, tc *platformv1alpha1.TalosCluster) error {
	nsName := "seam-tenant-" + tc.Name
	tcpName := tc.Name + "-control-plane"
	tcp := &unstructured.Unstructured{}
	tcp.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "controlplane.cluster.x-k8s.io",
		Version: "v1alpha3",
		Kind:    "TalosControlPlane",
	})
	if err := r.Client.Get(ctx, types.NamespacedName{Name: tcpName, Namespace: nsName}, tcp); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("ensureTalosControlPlane: get: %w", err)
		}
		tcp = &unstructured.Unstructured{}
		tcp.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "controlplane.cluster.x-k8s.io",
			Version: "v1alpha3",
			Kind:    "TalosControlPlane",
		})
		tcp.SetName(tcpName)
		tcp.SetNamespace(nsName)

		ownerRef := metav1.OwnerReference{
			APIVersion:         platformv1alpha1.GroupVersion.String(),
			Kind:               "TalosCluster",
			Name:               tc.Name,
			UID:                tc.UID,
			Controller:         boolPtr(true),
			BlockOwnerDeletion: boolPtr(true),
		}
		tcp.SetOwnerReferences([]metav1.OwnerReference{ownerRef})

		var replicas int64
		if tc.Spec.CAPI.ControlPlane != nil {
			replicas = int64(tc.Spec.CAPI.ControlPlane.Replicas)
		}
		if err := unstructured.SetNestedField(tcp.Object, map[string]interface{}{
			"replicas": replicas,
			"version":  tc.Spec.CAPI.KubernetesVersion,
			"infrastructureTemplate": map[string]interface{}{
				"apiVersion": "infrastructure.cluster.x-k8s.io/v1alpha1",
				"kind":       "SeamInfrastructureMachineTemplate",
				"name":       tc.Name + "-control-plane-template",
				"namespace":  nsName,
			},
		}, "spec"); err != nil {
			return fmt.Errorf("ensureTalosControlPlane: set spec: %w", err)
		}

		lineage.SetDescendantLabels(tcp, lineage.IndexName("TalosCluster", tc.Name), tc.Namespace, "platform", lineage.ClusterProvision, tc.GetAnnotations()[lineage.AnnotationDeclaringPrincipal])
		if err := r.Client.Create(ctx, tcp); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("ensureTalosControlPlane: create: %w", err)
		}
	}
	return nil
}

// ensureWorkerPool creates the MachineDeployment and SeamInfrastructureMachineTemplate
// for a worker pool if they do not exist. platform-schema.md §2.2.
func (r *TalosClusterReconciler) ensureWorkerPool(ctx context.Context, tc *platformv1alpha1.TalosCluster, pool platformv1alpha1.CAPIWorkerPool) error {
	nsName := "seam-tenant-" + tc.Name
	mdName := fmt.Sprintf("%s-%s", tc.Name, pool.Name)

	// Ensure SeamInfrastructureMachineTemplate for this pool.
	simtName := mdName + "-template"
	simt := &unstructured.Unstructured{}
	simt.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "infrastructure.cluster.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "SeamInfrastructureMachineTemplate",
	})
	if err := r.Client.Get(ctx, types.NamespacedName{Name: simtName, Namespace: nsName}, simt); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("ensureWorkerPool %s: get SeamInfrastructureMachineTemplate: %w", pool.Name, err)
		}
		simt = &unstructured.Unstructured{}
		simt.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "infrastructure.cluster.x-k8s.io",
			Version: "v1alpha1",
			Kind:    "SeamInfrastructureMachineTemplate",
		})
		simt.SetName(simtName)
		simt.SetNamespace(nsName)
		simt.SetOwnerReferences([]metav1.OwnerReference{{
			APIVersion:         platformv1alpha1.GroupVersion.String(),
			Kind:               "TalosCluster",
			Name:               tc.Name,
			UID:                tc.UID,
			Controller:         boolPtr(true),
			BlockOwnerDeletion: boolPtr(true),
		}})

		if err := r.Client.Create(ctx, simt); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("ensureWorkerPool %s: create SeamInfrastructureMachineTemplate: %w", pool.Name, err)
		}
	}

	// Ensure MachineDeployment for this pool.
	md := &unstructured.Unstructured{}
	md.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "cluster.x-k8s.io",
		Version: "v1beta1",
		Kind:    "MachineDeployment",
	})
	if err := r.Client.Get(ctx, types.NamespacedName{Name: mdName, Namespace: nsName}, md); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("ensureWorkerPool %s: get MachineDeployment: %w", pool.Name, err)
		}
		md = &unstructured.Unstructured{}
		md.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "cluster.x-k8s.io",
			Version: "v1beta1",
			Kind:    "MachineDeployment",
		})
		md.SetName(mdName)
		md.SetNamespace(nsName)
		md.SetOwnerReferences([]metav1.OwnerReference{{
			APIVersion:         platformv1alpha1.GroupVersion.String(),
			Kind:               "TalosCluster",
			Name:               tc.Name,
			UID:                tc.UID,
			Controller:         boolPtr(true),
			BlockOwnerDeletion: boolPtr(true),
		}})

		replicas := int64(pool.Replicas)
		configTmplName := tc.Name + "-config-template"
		if err := unstructured.SetNestedField(md.Object, map[string]interface{}{
			"clusterName": tc.Name,
			"replicas":    replicas,
			"selector": map[string]interface{}{
				"matchLabels": map[string]interface{}{
					"cluster.x-k8s.io/cluster-name":      tc.Name,
					"cluster.x-k8s.io/deployment-name":   mdName,
				},
			},
			"template": map[string]interface{}{
				"metadata": map[string]interface{}{
					"labels": map[string]interface{}{
						"cluster.x-k8s.io/cluster-name":    tc.Name,
						"cluster.x-k8s.io/deployment-name": mdName,
					},
				},
				"spec": map[string]interface{}{
					"clusterName": tc.Name,
					"bootstrap": map[string]interface{}{
						"configRef": map[string]interface{}{
							"apiVersion": "bootstrap.cluster.x-k8s.io/v1alpha3",
							"kind":       "TalosConfigTemplate",
							"name":       configTmplName,
						},
					},
					"infrastructureRef": map[string]interface{}{
						"apiVersion": "infrastructure.cluster.x-k8s.io/v1alpha1",
						"kind":       "SeamInfrastructureMachineTemplate",
						"name":       simtName,
					},
				},
			},
		}, "spec"); err != nil {
			return fmt.Errorf("ensureWorkerPool %s: set MachineDeployment spec: %w", pool.Name, err)
		}

		lineage.SetDescendantLabels(md, lineage.IndexName("TalosCluster", tc.Name), tc.Namespace, "platform", lineage.ClusterProvision, tc.GetAnnotations()[lineage.AnnotationDeclaringPrincipal])
		if err := r.Client.Create(ctx, md); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("ensureWorkerPool %s: create MachineDeployment: %w", pool.Name, err)
		}
	}
	return nil
}

// getCAPIClusterPhase reads the status.phase field of the CAPI Cluster object
// for this TalosCluster. Returns the phase string or an error if the object
// is not yet visible.
func (r *TalosClusterReconciler) getCAPIClusterPhase(ctx context.Context, tc *platformv1alpha1.TalosCluster) (string, error) {
	nsName := "seam-tenant-" + tc.Name
	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "cluster.x-k8s.io",
		Version: "v1beta1",
		Kind:    "Cluster",
	})
	if err := r.Client.Get(ctx, types.NamespacedName{Name: tc.Name, Namespace: nsName}, cluster); err != nil {
		return "", fmt.Errorf("getCAPIClusterPhase: get CAPI Cluster: %w", err)
	}
	phase, _, _ := unstructured.NestedString(cluster.Object, "status", "phase")
	return phase, nil
}

// isCiliumPackInstanceReady reads the PackInstance status for the Cilium pack
// and returns true when the PackInstance has reached Ready status.
// platform-design.md §4.
func (r *TalosClusterReconciler) isCiliumPackInstanceReady(ctx context.Context, tc *platformv1alpha1.TalosCluster) (bool, error) {
	if tc.Spec.CAPI.CiliumPackRef == nil {
		return true, nil
	}
	// Look up the PackInstance for the Cilium ClusterPack in the tenant namespace.
	// PackInstance is owned by infra.ontai.dev — we read it as unstructured.
	// platform-schema.md §9: reads infra.ontai.dev/PackInstance.
	nsName := "seam-tenant-" + tc.Name
	packInstanceList := &unstructured.UnstructuredList{}
	packInstanceList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "infra.ontai.dev",
		Version: "v1alpha1",
		Kind:    "PackInstanceList",
	})
	if err := r.Client.List(ctx, packInstanceList,
		client.InNamespace(nsName),
		client.MatchingLabels{"infra.ontai.dev/pack-name": tc.Spec.CAPI.CiliumPackRef.Name}); err != nil {
		// PackInstance CRD not yet registered — not ready.
		return false, nil
	}

	for _, pi := range packInstanceList.Items {
		ready, _, _ := unstructured.NestedBool(pi.Object, "status", "ready")
		if ready {
			return true, nil
		}
	}
	return false, nil
}

// conductorDeploymentName is the canonical name for the Conductor agent Deployment
// on any cluster. Matches the name stamped by compiler enable on the management cluster.
const conductorDeploymentName = "conductor-agent"

// conductorAgentNamespace is the namespace where Conductor runs on every cluster.
// Locked namespace model: CONTEXT.md §4.
const conductorAgentNamespace = "ont-system"

// conductorRoleEnvVar is the env var carrying the role stamp. conductor-schema.md §15.
// The role field is a first-class spec field — it is in the container spec, not
// in metadata. It is never modified after Deployment creation.
const conductorRoleEnvVar = "CONDUCTOR_ROLE"

// EnsureConductorDeploymentOnTargetCluster creates the Conductor agent Deployment
// in ont-system on the target cluster if it does not already exist, then checks
// whether the Deployment has reached Available=True.
//
// Returns (true, nil) when the Deployment is Available.
// Returns (false, nil) when the kubeconfig is not yet available, or when the
// Deployment was just created and is not yet Available — the caller should requeue.
// Returns (false, err) only for unexpected API errors.
//
// Platform is the sole authority for deploying Conductor to tenant clusters.
// The Deployment is stamped with CONDUCTOR_ROLE=tenant. platform-schema.md §12.
// conductor-schema.md §15 Role Declaration Contract. Gap 27.
//
// If RemoteConductorAvailableFn is set on the reconciler (unit test override), it
// is called instead of the real remote cluster interaction.
//
// Kubeconfig resolution: CAPI generates a Secret named {cluster-name}-kubeconfig
// in seam-tenant-{cluster-name} after the cluster reaches Running state. Platform
// reads this Secret to connect to the target cluster.
func (r *TalosClusterReconciler) EnsureConductorDeploymentOnTargetCluster(
	ctx context.Context,
	tc *platformv1alpha1.TalosCluster,
) (bool, error) {
	// Unit test override — injected via RemoteConductorAvailableFn to avoid
	// requiring a live target cluster kubeconfig in tests.
	if r.RemoteConductorAvailableFn != nil {
		return r.RemoteConductorAvailableFn(ctx, tc.Name)
	}

	tenantNS := "seam-tenant-" + tc.Name
	kubeconfigSecretName := tc.Name + "-kubeconfig"

	// Get the CAPI-generated kubeconfig Secret for the target cluster.
	kubeconfigSecret := &corev1.Secret{}
	if err := r.Client.Get(ctx, types.NamespacedName{
		Name:      kubeconfigSecretName,
		Namespace: tenantNS,
	}, kubeconfigSecret); err != nil {
		if apierrors.IsNotFound(err) {
			// CAPI has not yet generated the kubeconfig — not fatal, requeue.
			return false, nil
		}
		return false, fmt.Errorf("ensureConductorDeployment: get kubeconfig secret %s/%s: %w",
			tenantNS, kubeconfigSecretName, err)
	}

	kubeconfigBytes, ok := kubeconfigSecret.Data["value"]
	if !ok || len(kubeconfigBytes) == 0 {
		// Secret exists but kubeconfig not yet written — not fatal.
		return false, nil
	}

	// Build a remote Kubernetes client for the target cluster.
	restConfig, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigBytes)
	if err != nil {
		return false, fmt.Errorf("ensureConductorDeployment: parse kubeconfig for %s: %w", tc.Name, err)
	}
	remoteK8s, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return false, fmt.Errorf("ensureConductorDeployment: build remote client for %s: %w", tc.Name, err)
	}

	// Check whether the Conductor Deployment already exists.
	dep, err := remoteK8s.AppsV1().Deployments(conductorAgentNamespace).Get(
		ctx, conductorDeploymentName, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return false, fmt.Errorf("ensureConductorDeployment: check deployment %s/%s on %s: %w",
				conductorAgentNamespace, conductorDeploymentName, tc.Name, err)
		}
		// Deployment does not exist — create it.
		newDep := BuildConductorAgentDeployment(tc.Name)
		if _, createErr := remoteK8s.AppsV1().Deployments(conductorAgentNamespace).Create(
			ctx, newDep, metav1.CreateOptions{}); createErr != nil && !apierrors.IsAlreadyExists(createErr) {
			return false, fmt.Errorf("ensureConductorDeployment: create deployment on %s: %w", tc.Name, createErr)
		}
		// Just created — not yet Available.
		return false, nil
	}

	// Deployment exists — check the Available condition.
	// Available=True means all desired replicas are up and healthy.
	for _, cond := range dep.Status.Conditions {
		if cond.Type == appsv1.DeploymentAvailable && cond.Status == corev1.ConditionTrue {
			return true, nil
		}
	}
	// Deployment exists but not yet Available.
	return false, nil
}

// BuildConductorAgentDeployment builds the Conductor agent Deployment spec for a
// tenant cluster. CLUSTER_REF and CONDUCTOR_ROLE are injected via downward API
// fieldRef from pod template annotations so the pod reads its own identity without
// hard-coding values. Annotations must be on the pod template, not the Deployment
// metadata: fieldRef metadata.annotations resolves from the pod's own annotations.
// conductor-schema.md §15. platform-schema.md §12.
func BuildConductorAgentDeployment(clusterName string) *appsv1.Deployment {
	replicas := int32(1)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      conductorDeploymentName,
			Namespace: conductorAgentNamespace,
			Labels: map[string]string{
				"runner.ontai.dev/component": "conductor",
				"runner.ontai.dev/cluster":   clusterName,
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"runner.ontai.dev/component": "conductor",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"runner.ontai.dev/component": "conductor",
						"runner.ontai.dev/cluster":   clusterName,
					},
					Annotations: map[string]string{
						"platform.ontai.dev/cluster-ref": clusterName,
						"platform.ontai.dev/role":        "tenant",
					},
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: "conductor",
					Containers: []corev1.Container{
						{
							Name:  "conductor",
							Image: conductorImage, // Resolved from RunnerConfig.agentImage -- placeholder per SC-INV-002.
							Args:  []string{"agent"},
							Env: []corev1.EnvVar{
								{
									Name: conductorRoleEnvVar,
									ValueFrom: &corev1.EnvVarSource{
										FieldRef: &corev1.ObjectFieldSelector{
											FieldPath: "metadata.annotations['platform.ontai.dev/role']",
										},
									},
								},
								{
									Name: "CLUSTER_REF",
									ValueFrom: &corev1.EnvVarSource{
										FieldRef: &corev1.ObjectFieldSelector{
											FieldPath: "metadata.annotations['platform.ontai.dev/cluster-ref']",
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

// boolPtr returns a pointer to a bool value.
func boolPtr(b bool) *bool { return &b }

// --- Bug 3: RunnerConfig cleanup finalizer ---

// ensureRunnerConfigCleanupFinalizer adds finalizerRunnerConfigCleanup to tc when
// the ontai.dev/owns-runnerconfig=true annotation is set and the finalizer is not
// yet present. The Update is issued immediately so the finalizer is persisted before
// any reconcile logic proceeds. Bug 3.
func (r *TalosClusterReconciler) ensureRunnerConfigCleanupFinalizer(
	ctx context.Context,
	tc *platformv1alpha1.TalosCluster,
) error {
	if tc.Annotations["ontai.dev/owns-runnerconfig"] != "true" {
		return nil
	}
	if controllerutil.ContainsFinalizer(tc, finalizerRunnerConfigCleanup) {
		return nil
	}
	controllerutil.AddFinalizer(tc, finalizerRunnerConfigCleanup)
	if err := r.Client.Update(ctx, tc); err != nil {
		return fmt.Errorf("ensureRunnerConfigCleanupFinalizer: add finalizer: %w", err)
	}
	return nil
}

// ensureTenantNamespaceCleanupFinalizer adds finalizerTenantNamespaceCleanup to tc
// when spec.capi.enabled=true and the finalizer is not yet present. The Update is
// issued immediately so the finalizer is persisted before any reconcile logic proceeds.
// PLATFORM-BL-TENANT-GC.
func (r *TalosClusterReconciler) ensureTenantNamespaceCleanupFinalizer(
	ctx context.Context,
	tc *platformv1alpha1.TalosCluster,
) error {
	if tc.Spec.CAPI == nil || !tc.Spec.CAPI.Enabled {
		return nil
	}
	if controllerutil.ContainsFinalizer(tc, finalizerTenantNamespaceCleanup) {
		return nil
	}
	controllerutil.AddFinalizer(tc, finalizerTenantNamespaceCleanup)
	if err := r.Client.Update(ctx, tc); err != nil {
		return fmt.Errorf("ensureTenantNamespaceCleanupFinalizer: add finalizer: %w", err)
	}
	return nil
}

// handleTalosClusterDeletion is called when tc.DeletionTimestamp is set. Handles
// two finalizers:
//  1. finalizerRunnerConfigCleanup (annotation-gated): deletes the RunnerConfig in
//     ont-system and cluster Secrets from seam-system. Bug 3.
//  2. finalizerTenantNamespaceCleanup (CAPI-enabled only): deletes the
//     seam-tenant-{name} namespace. PLATFORM-BL-TENANT-GC.
//
// Both steps are idempotent on NotFound. Finalizers are removed once their cleanup
// is complete and both must be absent before the TalosCluster is released.
func (r *TalosClusterReconciler) handleTalosClusterDeletion(
	ctx context.Context,
	tc *platformv1alpha1.TalosCluster,
) (ctrl.Result, error) {
	// Step 1 — RunnerConfig and Secret cleanup (annotation-gated).
	if controllerutil.ContainsFinalizer(tc, finalizerRunnerConfigCleanup) {
		rc := &OperationalRunnerConfig{}
		err := r.Client.Get(ctx, types.NamespacedName{
			Name:      tc.Name,
			Namespace: bootstrapRunnerConfigNamespace,
		}, rc)
		if err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("handleTalosClusterDeletion: get RunnerConfig %s/%s: %w",
				bootstrapRunnerConfigNamespace, tc.Name, err)
		}
		if err == nil {
			if delErr := r.Client.Delete(ctx, rc); delErr != nil && !apierrors.IsNotFound(delErr) {
				return ctrl.Result{}, fmt.Errorf("handleTalosClusterDeletion: delete RunnerConfig %s/%s: %w",
					bootstrapRunnerConfigNamespace, tc.Name, delErr)
			}
		}

		tenantSecretNS := importSecretsNamespace(tc.Name)
		for _, secretName := range []string{
			"seam-mc-" + tc.Name + "-kubeconfig",
			"seam-mc-" + tc.Name + "-talosconfig",
		} {
			secret := &corev1.Secret{}
			err := r.Client.Get(ctx, types.NamespacedName{
				Name:      secretName,
				Namespace: tenantSecretNS,
			}, secret)
			if err != nil && !apierrors.IsNotFound(err) {
				return ctrl.Result{}, fmt.Errorf("handleTalosClusterDeletion: get Secret %s: %w", secretName, err)
			}
			if err == nil {
				if delErr := r.Client.Delete(ctx, secret); delErr != nil && !apierrors.IsNotFound(delErr) {
					return ctrl.Result{}, fmt.Errorf("handleTalosClusterDeletion: delete Secret %s: %w", secretName, delErr)
				}
			}
		}

		controllerutil.RemoveFinalizer(tc, finalizerRunnerConfigCleanup)
		if err := r.Client.Update(ctx, tc); err != nil {
			return ctrl.Result{}, fmt.Errorf("handleTalosClusterDeletion: remove runnerconfig finalizer: %w", err)
		}
	}

	// Step 2 — Tenant namespace cleanup (CAPI-enabled only). PLATFORM-BL-TENANT-GC.
	if controllerutil.ContainsFinalizer(tc, finalizerTenantNamespaceCleanup) {
		nsName := "seam-tenant-" + tc.Name
		ns := &corev1.Namespace{}
		err := r.Client.Get(ctx, types.NamespacedName{Name: nsName}, ns)
		if err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("handleTalosClusterDeletion: get tenant namespace %s: %w", nsName, err)
		}
		if err == nil {
			if delErr := r.Client.Delete(ctx, ns); delErr != nil && !apierrors.IsNotFound(delErr) {
				return ctrl.Result{}, fmt.Errorf("handleTalosClusterDeletion: delete tenant namespace %s: %w", nsName, delErr)
			}
		}

		controllerutil.RemoveFinalizer(tc, finalizerTenantNamespaceCleanup)
		if err := r.Client.Update(ctx, tc); err != nil {
			return ctrl.Result{}, fmt.Errorf("handleTalosClusterDeletion: remove tenant-namespace finalizer: %w", err)
		}
	}

	return ctrl.Result{}, nil
}

// --- WS1/WS2: tenant and management onboarding helpers ---

// appendToUnstructuredStringSlice reads the object identified by gvk/namespace/name,
// extracts the string slice at the given field path (e.g. ["spec","allowedClusters"]),
// and appends value if not already present. Patches via MergePatch. Returns nil on
// NotFound so callers remain non-fatal in unit test environments where guardian
// resources are not pre-loaded. PLATFORM-BL-3.
func (r *TalosClusterReconciler) appendToUnstructuredStringSlice(
	ctx context.Context,
	gvk schema.GroupVersionKind,
	namespace, name string,
	fieldPath []string,
	value string,
) error {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	if err := r.Client.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, obj); err != nil {
		if apierrors.IsNotFound(err) {
			return nil // non-fatal: guardian resource not yet present
		}
		return fmt.Errorf("appendToUnstructuredStringSlice: get %s/%s: %w", namespace, name, err)
	}

	// Extract current slice.
	raw, _, _ := unstructured.NestedStringSlice(obj.Object, fieldPath...)
	for _, v := range raw {
		if v == value {
			return nil // already present
		}
	}
	raw = append(raw, value)

	// Build a MergePatch that sets only the target field.
	patch := map[string]interface{}{}
	nested := patch
	for i, key := range fieldPath {
		if i == len(fieldPath)-1 {
			nested[key] = raw
		} else {
			child := map[string]interface{}{}
			nested[key] = child
			nested = child
		}
	}
	data, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("appendToUnstructuredStringSlice: marshal patch: %w", err)
	}
	if err := r.Client.Patch(ctx, obj, client.RawPatch(types.MergePatchType, data)); err != nil {
		return fmt.Errorf("appendToUnstructuredStringSlice: patch %s/%s: %w", namespace, name, err)
	}
	return nil
}

// ensureLocalQueue creates a Kueue LocalQueue in the given namespace pointing to
// clusterQueueName if it does not already exist. Uses unstructured to avoid importing
// Kueue types. Returns nil on AlreadyExists. PLATFORM-BL-3 step 3.
func (r *TalosClusterReconciler) ensureLocalQueue(
	ctx context.Context,
	namespace, queueName, clusterQueueName string,
) error {
	lq := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "kueue.x-k8s.io/v1beta1",
			"kind":       "LocalQueue",
			"metadata": map[string]interface{}{
				"name":      queueName,
				"namespace": namespace,
			},
			"spec": map[string]interface{}{
				"clusterQueue": clusterQueueName,
			},
		},
	}
	if err := r.Client.Create(ctx, lq); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("ensureLocalQueue: create LocalQueue %s/%s: %w", namespace, queueName, err)
	}
	return nil
}

// rbacPolicyGVK is the GVK for guardian RBACPolicy (security.ontai.dev/v1alpha1).
var rbacPolicyGVK = schema.GroupVersionKind{
	Group:   "security.ontai.dev",
	Version: "v1alpha1",
	Kind:    "RBACPolicy",
}

// rbacProfileGVK is the GVK for guardian RBACProfile (security.ontai.dev/v1alpha1).
var rbacProfileGVK = schema.GroupVersionKind{
	Group:   "security.ontai.dev",
	Version: "v1alpha1",
	Kind:    "RBACProfile",
}

// rbacPolicyNamespace is the namespace where the platform-wide RBACPolicy lives.
const rbacPolicyNamespace = "ont-system"

// rbacProfileNamespace is the namespace where RBACProfile CRs live.
const rbacProfileNamespace = "seam-system"

// ensureTenantOnboarding performs all idempotent onboarding steps for a tenant
// TalosCluster that has reached Ready on the direct path:
//  1. Append tc.Name to seam-platform-rbac-policy.spec.allowedClusters.
//  2. Append tc.Name to spec.targetClusters on rbac-wrapper, rbac-conductor,
//     rbac-platform, rbac-seam-core (guardian profile rbac-guardian is skipped).
//  3. Create LocalQueue pack-deploy-queue in seam-tenant-{tc.Name} pointing to
//     ClusterQueue seam-pack-deploy.
//
// All steps are idempotent and non-fatal on NotFound so existing tests that do not
// pre-load guardian resources continue to pass. PLATFORM-BL-3.
func (r *TalosClusterReconciler) ensureTenantOnboarding(ctx context.Context, tc *platformv1alpha1.TalosCluster) error {
	// Step 1 — RBACPolicy allowedClusters.
	if err := r.appendToUnstructuredStringSlice(
		ctx, rbacPolicyGVK, rbacPolicyNamespace, "seam-platform-rbac-policy",
		[]string{"spec", "allowedClusters"}, tc.Name,
	); err != nil {
		return fmt.Errorf("ensureTenantOnboarding: rbac policy: %w", err)
	}

	// Step 2 — RBACProfile targetClusters for all non-guardian profiles.
	for _, profileName := range []string{"rbac-wrapper", "rbac-conductor", "rbac-platform", "rbac-seam-core"} {
		if err := r.appendToUnstructuredStringSlice(
			ctx, rbacProfileGVK, rbacProfileNamespace, profileName,
			[]string{"spec", "targetClusters"}, tc.Name,
		); err != nil {
			return fmt.Errorf("ensureTenantOnboarding: rbac profile %s: %w", profileName, err)
		}
	}

	// Step 3 — LocalQueue in tenant namespace.
	tenantNS := "seam-tenant-" + tc.Name
	if err := r.ensureLocalQueue(ctx, tenantNS, "pack-deploy-queue", "seam-pack-deploy"); err != nil {
		return fmt.Errorf("ensureTenantOnboarding: local queue: %w", err)
	}

	return nil
}

// ensureManagementOnboarding appends "management" to seam-platform-rbac-policy
// spec.allowedClusters when the management TalosCluster reaches Ready. Idempotent
// and non-fatal on NotFound. PLATFORM-BL-3 WS2.
func (r *TalosClusterReconciler) ensureManagementOnboarding(ctx context.Context) error {
	if err := r.appendToUnstructuredStringSlice(
		ctx, rbacPolicyGVK, rbacPolicyNamespace, "seam-platform-rbac-policy",
		[]string{"spec", "allowedClusters"}, "management",
	); err != nil {
		return fmt.Errorf("ensureManagementOnboarding: %w", err)
	}
	return nil
}
