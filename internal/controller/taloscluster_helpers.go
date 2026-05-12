package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
	"github.com/ontai-dev/seam/pkg/lineage"
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

	// tenantNamespaceLabel is the namespace label applied to all tenant namespaces.
	tenantNamespaceLabel = "ontai.dev/tenant"

	// clusterNamespaceLabel is the namespace label applied to identify the cluster.
	clusterNamespaceLabel = "ontai.dev/cluster"

	// conductorExecuteImageName is the base image name for the Conductor executor
	// binary (debian-slim, used for executor Jobs). conductor-schema.md §3, Decision 12.
	conductorExecuteImageName = "conductor-execute"

	// devRevision is the image tag used for lab/development builds.
	// Production releases use {talosVersion} for executor and agent images.
	// conductor-schema.md §3, INV-011, INV-023.
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

// executorImageTag returns the conductor-execute (or conductor agent) image tag.
// In dev/lab (devRevision=="dev"): returns "dev" regardless of talosVersion.
// In production: returns talosVersion so the executor tracks the cluster's Talos version.
// conductor-schema.md §3, INV-011, INV-023.
func executorImageTag(talosVersion string) string {
	if devRevision == "dev" {
		return devRevision
	}
	return talosVersion
}

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

// finalizerWrapperRunnerCRBCleanup is placed on role=tenant TalosCluster objects
// that had wrapper-runner resources provisioned. The ClusterRoleBinding is
// cluster-scoped and cannot be removed by namespace deletion.
// PLATFORM-BL-WRAPPER-RUNNER-RBAC-LIFECYCLE.
const finalizerWrapperRunnerCRBCleanup = "platform.ontai.dev/wrapper-runner-crb-cleanup"

// finalizerDecisionHCascade is placed on role=tenant TalosCluster objects.
// Decision H mandates a fixed teardown order: wrapper components first
// (PackExecutions, PackInstances), guardian components second (conductor-tenant
// RBACProfile, allowedClusters removal, targetClusters removal), then existing
// finalizers handle RunnerConfig, namespace, and CRB cleanup.
//
// mode=bootstrap: cluster is permanently decommissioned and infrastructure torn down.
// mode=import: management relationship is severed only; the cluster continues to exist
// but is no longer governed by ONT (a divorce, not a destruction).
// Both share this management-cluster cleanup order. Decision H.
const finalizerDecisionHCascade = "platform.ontai.dev/decision-h-cascade"

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
// RunnerImage uses conductorExecuteImageName (conductor-execute) with a tag derived from
// tc.Spec.TalosVersion per INV-012 and conductor-schema.md §3:
//
//	{CONDUCTOR_REGISTRY}/conductor-execute:{tag}
//
// In dev/lab: tag = "dev". In production: tag = tc.Spec.TalosVersion.
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
	runnerImage := fmt.Sprintf("%s/%s:%s", registry, conductorExecuteImageName, executorImageTag(tc.Spec.TalosVersion))

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
// TalosCluster (capi.enabled=false). The job runs the bootstrap capability in executor
// mode. Image uses conductorExecuteImageName with executorImageTag derivation.
// platform-design.md §5.
func (r *TalosClusterReconciler) submitBootstrapJob(ctx context.Context, tc *platformv1alpha1.TalosCluster, jobName string) error {
	registry := os.Getenv(conductorRegistryEnv)
	if registry == "" {
		registry = conductorRegistryDefault
	}
	runnerImage := fmt.Sprintf("%s/%s:%s", registry, conductorExecuteImageName, executorImageTag(tc.Spec.TalosVersion))

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
							Name:  "executor",
							Image: runnerImage,
							Args:  []string{"execute"},
							Env: []corev1.EnvVar{
								{Name: "CAPABILITY", Value: bootstrapCapability},
								{Name: "CLUSTER_REF", Value: tc.Name},
								{Name: "OPERATION_RESULT_CR", Value: jobName},
								{
									Name: "POD_NAMESPACE",
									ValueFrom: &corev1.EnvVarSource{
										FieldRef: &corev1.ObjectFieldSelector{
											FieldPath: "metadata.namespace",
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

	// Set TalosCluster as the owner so the Job is garbage-collected with it. INV-006.
	if err := controllerutil.SetControllerReference(tc, job, r.Scheme); err != nil {
		return fmt.Errorf("submitBootstrapJob: set owner reference: %w", err)
	}

	if err := r.Client.Create(ctx, job); err != nil {
		return fmt.Errorf("submitBootstrapJob: create job %s/%s: %w", tc.Namespace, jobName, err)
	}
	return nil
}

// readOperationResult delegates to readOperationRecord using the TalosCluster
// name as clusterRef. Used by the bootstrap conductor Job path.
func (r *TalosClusterReconciler) readOperationResult(ctx context.Context, clusterName, jobName string) (complete, failed bool, message string) {
	return readOperationRecord(ctx, r.Client, clusterName, jobName)
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
		baseSysctls := map[string]interface{}{
			"net.core.bpf_jit_harden":         "0",
			"kernel.unprivileged_bpf_disabled": "0",
		}

		var hardeningPatches []interface{}
		if tc.Spec.HardeningProfileRef != nil {
			hpNS := tc.Spec.HardeningProfileRef.Namespace
			if hpNS == "" {
				hpNS = tc.Namespace
			}
			hp := &platformv1alpha1.HardeningProfile{}
			if err := r.Client.Get(ctx, types.NamespacedName{
				Name:      tc.Spec.HardeningProfileRef.Name,
				Namespace: hpNS,
			}, hp); err != nil {
				return fmt.Errorf("ensureTalosConfigTemplate: get HardeningProfile: %w", err)
			}
			for k, v := range hp.Spec.SysctlParams {
				baseSysctls[k] = v
			}
			for _, patchStr := range hp.Spec.MachineConfigPatches {
				var patchObj map[string]interface{}
				if err := json.Unmarshal([]byte(patchStr), &patchObj); err != nil {
					return fmt.Errorf("ensureTalosConfigTemplate: parse HardeningProfile patch: %w", err)
				}
				hardeningPatches = append(hardeningPatches, patchObj)
			}
		}

		machineConfigPatches := []interface{}{
			map[string]interface{}{
				"op":    "replace",
				"path":  "/cluster/network/cni/name",
				"value": "none",
			},
			// Cilium-required BPF kernel parameters merged with HardeningProfile sysctlParams. CP-INV-009.
			map[string]interface{}{
				"op":    "add",
				"path":  "/machine/sysctls",
				"value": baseSysctls,
			},
		}
		machineConfigPatches = append(machineConfigPatches, hardeningPatches...)

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

// conductorAgentNamespace is the namespace where Conductor runs on every cluster.
// Locked namespace model: CONTEXT.md §4.
const conductorAgentNamespace = "ont-system"

// EnsureRemoteConductorBootstrap sets up the bootstrap window infrastructure for
// conductor on the tenant cluster: ont-system namespace, conductor ServiceAccount,
// ClusterRole + ClusterRoleBinding (INV-020), and the InfrastructureTalosCluster
// CR copy in ont-system (Decision H). Platform never deploys the conductor
// Deployment — that is admin-controlled via the enable bundle.
//
// Returns (true, nil) when all bootstrap items are established.
// Returns (false, nil) when the kubeconfig is not yet available — caller requeues.
// Returns (false, err) only for unexpected API errors.
//
// Applies to role=tenant only. Management clusters are excluded by the caller.
// platform-schema.md §12 steps 3-6. INV-020. Decision H.
//
// If RemoteConductorBootstrapDoneFn is set on the reconciler (unit test override),
// it is called instead of the real remote cluster interaction.
func (r *TalosClusterReconciler) EnsureRemoteConductorBootstrap(
	ctx context.Context,
	tc *platformv1alpha1.TalosCluster,
) (bool, error) {
	// Unit test override — injected via RemoteConductorBootstrapDoneFn to avoid
	// requiring a live target cluster kubeconfig in tests.
	if r.RemoteConductorBootstrapDoneFn != nil {
		return r.RemoteConductorBootstrapDoneFn(ctx, tc.Name)
	}

	tenantNS := "seam-tenant-" + tc.Name

	// Both import and CAPI clusters: kubeconfig is at seam-mc-{cluster}-kubeconfig in
	// seam-tenant-{cluster}. Import path writes it via ensureKubeconfigSecret.
	// CAPI path writes it via ensureCAPIKubeconfig after the cluster reaches Running.
	kubeSecretName := kubeconfigSecretName(tc.Name)

	// Get the kubeconfig Secret for the target cluster.
	kubeconfigSecret := &corev1.Secret{}
	if err := r.Client.Get(ctx, types.NamespacedName{
		Name:      kubeSecretName,
		Namespace: tenantNS,
	}, kubeconfigSecret); err != nil {
		if apierrors.IsNotFound(err) {
			// Kubeconfig not yet available -- not fatal, requeue.
			return false, nil
		}
		return false, fmt.Errorf("ensureConductorDeployment: get kubeconfig secret %s/%s: %w",
			tenantNS, kubeSecretName, err)
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
	dynClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return false, fmt.Errorf("ensureConductorDeployment: build dynamic client for %s: %w", tc.Name, err)
	}

	// Bootstrap the conductor's runtime environment on the tenant cluster: namespace,
	// ServiceAccount, bootstrap-window RBAC, and InfrastructureTalosCluster CR copy.
	// Applies to role=tenant only. Both mode=import and mode=bootstrap clusters are
	// tenant clusters; mode=bootstrap with role=management identifies the management
	// cluster itself, which must not receive remote conductor bootstrap (its conductor
	// is installed via the enable bundle and its ont-system is not a target namespace).
	// The reconciler dispatch already exits management clusters before reaching this
	// function; this guard is an explicit second layer of defense.
	// platform-schema.md §12 steps 3-6. INV-020. Decision H.
	if tc.Spec.Role == platformv1alpha1.TalosClusterRoleTenant {
		if err := ensureRemoteNamespace(ctx, remoteK8s, conductorAgentNamespace); err != nil {
			return false, fmt.Errorf("ensureConductorDeployment: ensure namespace %s on %s: %w",
				conductorAgentNamespace, tc.Name, err)
		}
		if err := ensureRemoteConductorServiceAccount(ctx, remoteK8s); err != nil {
			return false, fmt.Errorf("ensureConductorDeployment: ensure conductor SA on %s: %w",
				tc.Name, err)
		}
		if err := EnsureRemoteConductorRBAC(ctx, remoteK8s); err != nil {
			return false, fmt.Errorf("ensureConductorDeployment: ensure conductor RBAC on %s: %w",
				tc.Name, err)
		}
		if err := EnsureRemoteTalosClusterCopy(ctx, dynClient, tc); err != nil {
			return false, fmt.Errorf("ensureConductorDeployment: ensure TalosCluster copy on %s: %w",
				tc.Name, err)
		}
	}

	// All bootstrap window items complete.
	return true, nil
}

// conductorTenantClusterRoleName is the ClusterRole and ClusterRoleBinding name for the
// conductor agent on all tenant clusters (mode=import and mode=bootstrap). Applied during
// the bootstrap window before guardian becomes operational on the tenant cluster.
// INV-020. platform-schema.md §12.
const conductorTenantClusterRoleName = "conductor-agent-tenant"

// ensureRemoteNamespace creates the given namespace on the remote cluster if it does not
// already exist. Idempotent. Used to create ont-system on tenant clusters as part of
// the conductor bootstrap window. platform-schema.md §12 step 3.
func ensureRemoteNamespace(ctx context.Context, k8s kubernetes.Interface, name string) error {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if _, err := k8s.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create namespace %s: %w", name, err)
	}
	return nil
}

// ensureRemoteConductorServiceAccount creates the conductor ServiceAccount in ont-system
// on the remote cluster if it does not already exist. Idempotent. Used on import-mode
// tenant clusters before Conductor Deployment creation. platform-schema.md §12 step 4.
func ensureRemoteConductorServiceAccount(ctx context.Context, k8s kubernetes.Interface) error {
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "conductor",
			Namespace: conductorAgentNamespace,
		},
	}
	if _, err := k8s.CoreV1().ServiceAccounts(conductorAgentNamespace).Create(ctx, sa, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create conductor ServiceAccount in %s: %w", conductorAgentNamespace, err)
	}
	return nil
}

// EnsureRemoteConductorRBAC creates the ClusterRole and ClusterRoleBinding for the
// conductor agent on the remote tenant cluster. Conductor role=tenant needs cluster-scoped
// read access to all Seam governance CRs to drive drift detection, and write access to
// create events. Applied during the bootstrap window before guardian becomes operational
// on the tenant cluster (INV-020). Idempotent. platform-schema.md §12 step 5.
func EnsureRemoteConductorRBAC(ctx context.Context, k8s kubernetes.Interface) error {
	cr := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: conductorTenantClusterRoleName,
			Labels: map[string]string{
				"runner.ontai.dev/component":  "conductor",
				"runner.ontai.dev/managed-by": "platform",
			},
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"infrastructure.ontai.dev"},
				Resources: []string{"*"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
			{
				// TalosCluster (seam.ontai.dev) read access for drift detection on tenant cluster.
				APIGroups: []string{"seam.ontai.dev"},
				Resources: []string{"talosclusters", "talosclusters/status"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch"},
			},
			{
				// RBACProfilePullLoop and RBACPolicyPullLoop SSA-patch security.ontai.dev
				// resources into ont-system. Needs create/update/patch in addition to read.
				APIGroups: []string{"security.ontai.dev"},
				Resources: []string{"*"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch"},
			},
			{
				// Full write access to core resources: conductor orphan teardown deletes
				// deployed workload resources (ServiceAccounts, ConfigMaps, Services, etc.)
				// when their governing ClusterPack is removed. Decision H.
				APIGroups: []string{""},
				Resources: []string{"*"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
			{
				APIGroups: []string{"apps"},
				Resources: []string{"*"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
			{
				APIGroups: []string{"networking.k8s.io"},
				Resources: []string{"*"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
			{
				APIGroups: []string{"batch"},
				Resources: []string{"*"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
			{
				APIGroups: []string{"events.k8s.io"},
				Resources: []string{"events"},
				Verbs:     []string{"create", "patch"},
			},
			{
				APIGroups: []string{"coordination.k8s.io"},
				Resources: []string{"leases"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
			{
				APIGroups: []string{"rbac.authorization.k8s.io"},
				Resources: []string{"*"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
		},
	}
	if _, err := k8s.RbacV1().ClusterRoles().Create(ctx, cr, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create ClusterRole %s: %w", conductorTenantClusterRoleName, err)
	}

	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: conductorTenantClusterRoleName,
			Labels: map[string]string{
				"runner.ontai.dev/component":  "conductor",
				"runner.ontai.dev/managed-by": "platform",
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     conductorTenantClusterRoleName,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      "conductor",
				Namespace: conductorAgentNamespace,
			},
		},
	}
	if _, err := k8s.RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create ClusterRoleBinding %s: %w", conductorTenantClusterRoleName, err)
	}
	return nil
}

// EnsureRemoteTalosClusterCopy creates an InfrastructureTalosCluster CR in ont-system
// on the tenant cluster that mirrors the spec declared on the management cluster.
// Conductor role=tenant watches this CR to detect drift between declared state and
// actual cluster state. Decision H: conductor is the reconciliation authority for its
// cluster's governance state. Idempotent. platform-schema.md §12 step 6.
//
// If the InfrastructureTalosCluster CRD is not yet installed on the tenant cluster
// (seam-core enable bundle not yet applied), the function returns nil and defers.
// SC-INV-003: seam-core CRDs are installed before all operators.
func EnsureRemoteTalosClusterCopy(ctx context.Context, dynClient dynamic.Interface, tc *platformv1alpha1.TalosCluster) error {
	gvr := schema.GroupVersionResource{
		Group:    "seam.ontai.dev",
		Version:  "v1alpha1",
		Resource: "talosclusters",
	}

	// Idempotency: skip if the CR already exists.
	_, err := dynClient.Resource(gvr).Namespace(conductorAgentNamespace).Get(ctx, tc.Name, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("ensureRemoteTalosClusterCopy: check existing CR on %s: %w", tc.Name, err)
	}

	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "seam.ontai.dev/v1alpha1",
			"kind":       "TalosCluster",
			"metadata": map[string]interface{}{
				"name":      tc.Name,
				"namespace": conductorAgentNamespace,
				"labels": map[string]interface{}{
					"ontai.dev/managed-by":    "platform",
					"ontai.dev/cluster-source": "management",
				},
			},
			"spec": map[string]interface{}{
				"mode":              string(tc.Spec.Mode),
				"role":              string(tc.Spec.Role),
				"talosVersion":      tc.Spec.TalosVersion,
				"kubernetesVersion": tc.Spec.KubernetesVersion,
				"clusterEndpoint":   tc.Spec.ClusterEndpoint,
			},
		},
	}

	if _, err := dynClient.Resource(gvr).Namespace(conductorAgentNamespace).Create(ctx, obj, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil
		}
		// CRD not yet installed on the tenant cluster -- seam-core enable bundle has not
		// been applied yet. Return nil and defer; next reconcile will retry. SC-INV-003.
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("ensureRemoteTalosClusterCopy: create TalosCluster on %s: %w", tc.Name, err)
	}
	return nil
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

// ensureWrapperRunnerCRBCleanupFinalizer adds finalizerWrapperRunnerCRBCleanup to
// role=tenant TalosCluster objects so the cluster-scoped ClusterRoleBinding is
// deleted on TalosCluster deletion. The binding is created by ensureWrapperRunnerResources
// and cannot be removed via namespace deletion. PLATFORM-BL-WRAPPER-RUNNER-RBAC-LIFECYCLE.
func (r *TalosClusterReconciler) ensureWrapperRunnerCRBCleanupFinalizer(
	ctx context.Context,
	tc *platformv1alpha1.TalosCluster,
) error {
	if tc.Spec.Role != platformv1alpha1.TalosClusterRoleTenant {
		return nil
	}
	if controllerutil.ContainsFinalizer(tc, finalizerWrapperRunnerCRBCleanup) {
		return nil
	}
	controllerutil.AddFinalizer(tc, finalizerWrapperRunnerCRBCleanup)
	if err := r.Client.Update(ctx, tc); err != nil {
		return fmt.Errorf("ensureWrapperRunnerCRBCleanupFinalizer: add finalizer: %w", err)
	}
	return nil
}

// ensureDecisionHCascadeFinalizer adds finalizerDecisionHCascade to role=tenant
// TalosCluster objects so the Decision H teardown order is enforced before the
// existing finalizers handle RunnerConfig, namespace, and CRB cleanup. Decision H, T-24.
func (r *TalosClusterReconciler) ensureDecisionHCascadeFinalizer(
	ctx context.Context,
	tc *platformv1alpha1.TalosCluster,
) error {
	if tc.Spec.Role != platformv1alpha1.TalosClusterRoleTenant {
		return nil
	}
	if controllerutil.ContainsFinalizer(tc, finalizerDecisionHCascade) {
		return nil
	}
	controllerutil.AddFinalizer(tc, finalizerDecisionHCascade)
	if err := r.Client.Update(ctx, tc); err != nil {
		return fmt.Errorf("ensureDecisionHCascadeFinalizer: add finalizer: %w", err)
	}
	return nil
}

// handleTalosClusterDeletion is called when tc.DeletionTimestamp is set. Handles
// four finalizers in order:
//  0. finalizerDecisionHCascade (role=tenant only): Decision H ordered teardown.
//     Deletes wrapper components (PackExecutions, PackInstances), then guardian
//     components (conductor-tenant RBACProfile, allowedClusters, targetClusters).
//  1. finalizerRunnerConfigCleanup (annotation-gated): deletes the RunnerConfig in
//     ont-system and cluster Secrets from seam-system. Bug 3.
//  2. finalizerTenantNamespaceCleanup (CAPI-enabled only): deletes the
//     seam-tenant-{name} namespace. PLATFORM-BL-TENANT-GC.
//  3. finalizerWrapperRunnerCRBCleanup (role=tenant only): deletes the
//     cluster-scoped wrapper-runner-cluster-scoped-{name} ClusterRoleBinding.
//     PLATFORM-BL-WRAPPER-RUNNER-RBAC-LIFECYCLE.
//
// All steps are idempotent on NotFound. Finalizers are removed once their cleanup
// is complete and all must be absent before the TalosCluster is released.
func (r *TalosClusterReconciler) handleTalosClusterDeletion(
	ctx context.Context,
	tc *platformv1alpha1.TalosCluster,
) (ctrl.Result, error) {
	// Step 0 — Decision H cascade (role=tenant only). Decision H, T-24.
	// Wrapper components first, guardian components second. Both mode=bootstrap
	// (cluster decommissioned) and mode=import (severance only) share this cleanup
	// order on the management cluster. Decision H.
	if controllerutil.ContainsFinalizer(tc, finalizerDecisionHCascade) {
		tenantNS := "seam-tenant-" + tc.Name

		// Step 0a — Delete all InfrastructurePackExecutions in seam-tenant-{name}.
		peList := &unstructured.UnstructuredList{}
		peList.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   packExecutionTenantGVK.Group,
			Version: packExecutionTenantGVK.Version,
			Kind:    packExecutionTenantGVK.Kind + "List",
		})
		if err := r.Client.List(ctx, peList, client.InNamespace(tenantNS)); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("handleTalosClusterDeletion: list PackExecutions in %s: %w", tenantNS, err)
		}
		for i := range peList.Items {
			pe := &peList.Items[i]
			if delErr := r.Client.Delete(ctx, pe); delErr != nil && !apierrors.IsNotFound(delErr) {
				return ctrl.Result{}, fmt.Errorf("handleTalosClusterDeletion: delete PackExecution %s/%s: %w", tenantNS, pe.GetName(), delErr)
			}
		}

		// Step 0b — Delete all InfrastructurePackInstances in seam-tenant-{name}.
		piList := &unstructured.UnstructuredList{}
		piList.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   packInstanceTenantGVK.Group,
			Version: packInstanceTenantGVK.Version,
			Kind:    packInstanceTenantGVK.Kind + "List",
		})
		if err := r.Client.List(ctx, piList, client.InNamespace(tenantNS)); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("handleTalosClusterDeletion: list PackInstances in %s: %w", tenantNS, err)
		}
		for i := range piList.Items {
			pi := &piList.Items[i]
			if delErr := r.Client.Delete(ctx, pi); delErr != nil && !apierrors.IsNotFound(delErr) {
				return ctrl.Result{}, fmt.Errorf("handleTalosClusterDeletion: delete PackInstance %s/%s: %w", tenantNS, pi.GetName(), delErr)
			}
		}

		// Step 0c — Delete conductor-tenant RBACProfile in seam-tenant-{name}.
		rbacProfile := &unstructured.Unstructured{}
		rbacProfile.SetGroupVersionKind(rbacProfileGVK)
		err := r.Client.Get(ctx, types.NamespacedName{Name: "conductor-tenant", Namespace: tenantNS}, rbacProfile)
		if err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("handleTalosClusterDeletion: get conductor-tenant RBACProfile in %s: %w", tenantNS, err)
		}
		if err == nil {
			if delErr := r.Client.Delete(ctx, rbacProfile); delErr != nil && !apierrors.IsNotFound(delErr) {
				return ctrl.Result{}, fmt.Errorf("handleTalosClusterDeletion: delete conductor-tenant RBACProfile in %s: %w", tenantNS, delErr)
			}
		}

		// Step 0d — Remove cluster from seam-platform-rbac-policy.spec.allowedClusters in ont-system.
		if err := r.removeFromUnstructuredStringSlice(
			ctx, rbacPolicyGVK, rbacPolicyNamespace, "seam-platform-rbac-policy",
			[]string{"spec", "allowedClusters"}, tc.Name,
		); err != nil {
			return ctrl.Result{}, fmt.Errorf("handleTalosClusterDeletion: remove from rbac-policy allowedClusters: %w", err)
		}

		// Step 0e — Remove cluster from spec.targetClusters on the four platform-wide RBACProfile CRs in seam-system.
		for _, profileName := range []string{"rbac-wrapper", "rbac-conductor", "rbac-platform", "rbac-seam-core"} {
			if err := r.removeFromUnstructuredStringSlice(
				ctx, rbacProfileGVK, rbacProfileNamespace, profileName,
				[]string{"spec", "targetClusters"}, tc.Name,
			); err != nil {
				return ctrl.Result{}, fmt.Errorf("handleTalosClusterDeletion: remove from RBACProfile %s targetClusters: %w", profileName, err)
			}
		}

		// Step 0f — Remove finalizer.
		controllerutil.RemoveFinalizer(tc, finalizerDecisionHCascade)
		if err := r.Client.Update(ctx, tc); err != nil {
			return ctrl.Result{}, fmt.Errorf("handleTalosClusterDeletion: remove decision-h-cascade finalizer: %w", err)
		}
	}

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

	// Step 3 — Wrapper-runner ClusterRoleBinding cleanup (role=tenant only).
	// The ClusterRoleBinding is cluster-scoped and not deleted by namespace deletion.
	// PLATFORM-BL-WRAPPER-RUNNER-RBAC-LIFECYCLE.
	if controllerutil.ContainsFinalizer(tc, finalizerWrapperRunnerCRBCleanup) {
		crbName := "wrapper-runner-cluster-scoped-" + tc.Name
		crb := &rbacv1.ClusterRoleBinding{}
		err := r.Client.Get(ctx, types.NamespacedName{Name: crbName}, crb)
		if err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("handleTalosClusterDeletion: get ClusterRoleBinding %s: %w", crbName, err)
		}
		if err == nil {
			if delErr := r.Client.Delete(ctx, crb); delErr != nil && !apierrors.IsNotFound(delErr) {
				return ctrl.Result{}, fmt.Errorf("handleTalosClusterDeletion: delete ClusterRoleBinding %s: %w", crbName, delErr)
			}
		}

		controllerutil.RemoveFinalizer(tc, finalizerWrapperRunnerCRBCleanup)
		if err := r.Client.Update(ctx, tc); err != nil {
			return ctrl.Result{}, fmt.Errorf("handleTalosClusterDeletion: remove wrapper-runner-crb finalizer: %w", err)
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

// removeFromUnstructuredStringSlice reads the object identified by gvk/namespace/name,
// extracts the string slice at the given field path, and removes value if present.
// Patches via MergePatch. Returns nil on NotFound so callers remain non-fatal when
// guardian resources are absent. Decision H (deletion cascade). T-24.
func (r *TalosClusterReconciler) removeFromUnstructuredStringSlice(
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
			return nil // non-fatal: guardian resource not present
		}
		return fmt.Errorf("removeFromUnstructuredStringSlice: get %s/%s: %w", namespace, name, err)
	}

	// Extract current slice and filter out value.
	raw, _, _ := unstructured.NestedStringSlice(obj.Object, fieldPath...)
	filtered := make([]string, 0, len(raw))
	for _, v := range raw {
		if v != value {
			filtered = append(filtered, v)
		}
	}
	if len(filtered) == len(raw) {
		return nil // value was not present, nothing to patch
	}

	// Build a MergePatch that sets only the target field.
	patch := map[string]interface{}{}
	nested := patch
	for i, key := range fieldPath {
		if i == len(fieldPath)-1 {
			nested[key] = filtered
		} else {
			child := map[string]interface{}{}
			nested[key] = child
			nested = child
		}
	}
	data, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("removeFromUnstructuredStringSlice: marshal patch: %w", err)
	}
	if err := r.Client.Patch(ctx, obj, client.RawPatch(types.MergePatchType, data)); err != nil {
		return fmt.Errorf("removeFromUnstructuredStringSlice: patch %s/%s: %w", namespace, name, err)
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

// packExecutionTenantGVK is the GVK for PackExecution CRs in
// the tenant namespace. Owned by wrapper. MIGRATION-3.2.
var packExecutionTenantGVK = schema.GroupVersionKind{
	Group:   "seam.ontai.dev",
	Version: "v1alpha1",
	Kind:    "PackExecution",
}

// packInstanceTenantGVK is the GVK for PackInstalled CRs in
// the tenant namespace. Owned by wrapper. MIGRATION-3.2.
var packInstanceTenantGVK = schema.GroupVersionKind{
	Group:   "seam.ontai.dev",
	Version: "v1alpha1",
	Kind:    "PackInstalled",
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

	// Step 4 — Executor talosconfig secret and executor SA/Role/RoleBinding for day-2 Jobs.
	if err := r.ensureExecutorTalosconfig(ctx, tc); err != nil {
		return fmt.Errorf("ensureTenantOnboarding: executor talosconfig: %w", err)
	}
	if err := r.ensureTenantExecutorResources(ctx, tc); err != nil {
		return fmt.Errorf("ensureTenantOnboarding: tenant executor resources: %w", err)
	}

	// Step 5 — wrapper-runner SA/Role/RoleBinding/ClusterRoleBinding for pack-deploy Jobs.
	// The wrapper submits pack-deploy Kueue Jobs in seam-tenant-{clusterName}. The
	// wrapper-runner SA is the Job identity. ClusterRole wrapper-runner-cluster-scoped
	// is created by the management cluster enable bundle and is shared; Platform creates
	// the per-tenant ClusterRoleBinding only.
	if err := r.ensureWrapperRunnerResources(ctx, tc); err != nil {
		return fmt.Errorf("ensureTenantOnboarding: wrapper runner resources: %w", err)
	}

	return nil
}

// ensureManagementOnboarding appends "management" to seam-platform-rbac-policy
// spec.allowedClusters and copies the cluster talosconfig Secret to ont-system so
// platform executor Jobs can mount it. Idempotent and non-fatal on NotFound.
// PLATFORM-BL-3 WS2.
func (r *TalosClusterReconciler) ensureManagementOnboarding(ctx context.Context, tc *platformv1alpha1.TalosCluster) error {
	if err := r.appendToUnstructuredStringSlice(
		ctx, rbacPolicyGVK, rbacPolicyNamespace, "seam-platform-rbac-policy",
		[]string{"spec", "allowedClusters"}, "management",
	); err != nil {
		return fmt.Errorf("ensureManagementOnboarding: rbac policy: %w", err)
	}
	if err := r.ensureExecutorTalosconfig(ctx, tc); err != nil {
		return fmt.Errorf("ensureManagementOnboarding: executor talosconfig: %w", err)
	}
	if err := r.ensureTenantExecutorResources(ctx, tc); err != nil {
		return fmt.Errorf("ensureManagementOnboarding: tenant executor resources: %w", err)
	}
	return nil
}

// ensureExecutorTalosconfig copies the cluster talosconfig Secret into both
// ont-system (for Conductor agent Jobs) and seam-tenant-{clusterName} (for
// day-2 executor Jobs). The secret name is {clusterName}-talosconfig in both
// destinations. Idempotent — skips a destination if the Secret already exists.
// Returns nil on NotFound of the source (non-fatal; cluster may not yet be ready).
func (r *TalosClusterReconciler) ensureExecutorTalosconfig(ctx context.Context, tc *platformv1alpha1.TalosCluster) error {
	srcNS := importSecretsNamespace(tc.Name)
	srcName := talosconfigSecretName(tc.Name)
	dstName := tc.Name + "-talosconfig"

	// Read source talosconfig Secret.
	src := &corev1.Secret{}
	if err := r.Client.Get(ctx, types.NamespacedName{Name: srcName, Namespace: srcNS}, src); err != nil {
		if apierrors.IsNotFound(err) {
			return nil // source not yet present, skip silently
		}
		return fmt.Errorf("ensureExecutorTalosconfig: get source Secret %s/%s: %w", srcNS, srcName, err)
	}

	// Copy to seam-tenant-{cluster} (day-2 executor Jobs). The Job namespace is always
	// seam-tenant-{clusterName}; operational_job_base.go mounts from the Job namespace.
	// ont-system is NOT a destination: the conductor agent Deployment reads its talosconfig
	// via TALOSCONFIG_PATH from the enable bundle manifest, not via this copy.
	for _, dstNS := range []string{"seam-tenant-" + tc.Name} {
		dst := &corev1.Secret{}
		if err := r.Client.Get(ctx, types.NamespacedName{Name: dstName, Namespace: dstNS}, dst); err == nil {
			continue // already exists
		} else if !apierrors.IsNotFound(err) {
			return fmt.Errorf("ensureExecutorTalosconfig: get dest Secret %s/%s: %w", dstNS, dstName, err)
		}
		cp := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      dstName,
				Namespace: dstNS,
				Labels: map[string]string{
					"platform.ontai.dev/cluster": tc.Name,
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: src.Data,
		}
		if err := r.Client.Create(ctx, cp); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("ensureExecutorTalosconfig: create dest Secret %s/%s: %w", dstNS, dstName, err)
		}
	}
	return nil
}

// ensureTenantExecutorResources creates the platform-executor ServiceAccount,
// Role, and RoleBinding in seam-tenant-{clusterName} so that day-2 Conductor
// executor Jobs can write InfrastructureTalosClusterOperationResult CRs and
// read platform CRDs (NodeOperation, NodeMaintenance, etc.) in that namespace.
// CP-INV-003, CP-INV-004: RBAC is Guardian-governed; this creates the minimal
// namespace-scoped resources required for executor Job pods.
func (r *TalosClusterReconciler) ensureTenantExecutorResources(ctx context.Context, tc *platformv1alpha1.TalosCluster) error {
	tenantNS := "seam-tenant-" + tc.Name

	sa := &corev1.ServiceAccount{}
	if err := r.Client.Get(ctx, types.NamespacedName{Name: "platform-executor", Namespace: tenantNS}, sa); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("ensureTenantExecutorResources: get SA: %w", err)
		}
		sa = &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "platform-executor",
				Namespace: tenantNS,
				Labels:    map[string]string{"platform.ontai.dev/cluster": tc.Name},
			},
		}
		if err := r.Client.Create(ctx, sa); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("ensureTenantExecutorResources: create SA: %w", err)
		}
	}

	role := &rbacv1.Role{}
	if err := r.Client.Get(ctx, types.NamespacedName{Name: "platform-executor", Namespace: tenantNS}, role); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("ensureTenantExecutorResources: get Role: %w", err)
		}
		role = &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "platform-executor",
				Namespace: tenantNS,
				Labels:    map[string]string{"platform.ontai.dev/cluster": tc.Name},
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{"infrastructure.ontai.dev"},
					Resources: []string{"infrastructuretalosclusteroperationresults"},
					Verbs:     []string{"get", "create", "update", "patch"},
				},
				{
					APIGroups: []string{"platform.ontai.dev"},
					Resources: []string{"etcdmaintenances", "hardeningprofiles", "nodemaintenances", "nodeoperations", "pkirotations", "upgradepolicies"},
					Verbs:     []string{"get", "list", "watch"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"secrets"},
					Verbs:     []string{"get", "create", "update", "patch"},
				},
			},
		}
		if err := r.Client.Create(ctx, role); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("ensureTenantExecutorResources: create Role: %w", err)
		}
	}

	rb := &rbacv1.RoleBinding{}
	if err := r.Client.Get(ctx, types.NamespacedName{Name: "platform-executor", Namespace: tenantNS}, rb); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("ensureTenantExecutorResources: get RoleBinding: %w", err)
		}
		rb = &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "platform-executor",
				Namespace: tenantNS,
				Labels:    map[string]string{"platform.ontai.dev/cluster": tc.Name},
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "Role",
				Name:     "platform-executor",
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      "platform-executor",
					Namespace: tenantNS,
				},
			},
		}
		if err := r.Client.Create(ctx, rb); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("ensureTenantExecutorResources: create RoleBinding: %w", err)
		}
	}
	// Ensure the per-cluster TCOR exists so Conductor executor Jobs can append records.
	talosVersion := ""
	if tc.Spec.TalosVersion != "" {
		talosVersion = tc.Spec.TalosVersion
	}
	if err := ensureTCOR(ctx, r.Client, tc.Name, talosVersion); err != nil {
		return fmt.Errorf("ensureTenantExecutorResources: %w", err)
	}
	return nil
}

// ensureWrapperRunnerResources creates the wrapper-runner SA, Role, RoleBinding,
// and ClusterRoleBinding in seam-tenant-{clusterName} so that pack-deploy Kueue
// Jobs submitted by Wrapper can run in that namespace. The shared ClusterRole
// wrapper-runner-cluster-scoped is created by the management cluster enable bundle;
// Platform only creates the per-tenant ClusterRoleBinding.
// wrapper-schema.md §4 §9, INV-004.
func (r *TalosClusterReconciler) ensureWrapperRunnerResources(ctx context.Context, tc *platformv1alpha1.TalosCluster) error {
	tenantNS := "seam-tenant-" + tc.Name
	clusterLabel := map[string]string{"platform.ontai.dev/cluster": tc.Name}

	// ServiceAccount.
	sa := &corev1.ServiceAccount{}
	if err := r.Client.Get(ctx, types.NamespacedName{Name: "wrapper-runner", Namespace: tenantNS}, sa); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("ensureWrapperRunnerResources: get SA: %w", err)
		}
		sa = &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "wrapper-runner",
				Namespace: tenantNS,
				Labels:    clusterLabel,
				Annotations: map[string]string{
					"ontai.dev/rbac-owner": "guardian",
				},
			},
		}
		if err := r.Client.Create(ctx, sa); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("ensureWrapperRunnerResources: create SA: %w", err)
		}
	}

	// Role.
	role := &rbacv1.Role{}
	if err := r.Client.Get(ctx, types.NamespacedName{Name: "wrapper-runner", Namespace: tenantNS}, role); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("ensureWrapperRunnerResources: get Role: %w", err)
		}
		role = &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "wrapper-runner",
				Namespace: tenantNS,
				Labels:    clusterLabel,
				Annotations: map[string]string{
					"ontai.dev/rbac-owner": "guardian",
				},
			},
			Rules: []rbacv1.PolicyRule{
				{APIGroups: []string{""}, Resources: []string{"configmaps", "secrets", "serviceaccounts", "services", "persistentvolumeclaims", "endpoints", "pods"}, Verbs: []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
				{APIGroups: []string{"apps"}, Resources: []string{"deployments", "daemonsets", "statefulsets", "replicasets"}, Verbs: []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
				{APIGroups: []string{"networking.k8s.io"}, Resources: []string{"ingresses", "ingressclasses"}, Verbs: []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
				{APIGroups: []string{"batch"}, Resources: []string{"jobs", "cronjobs"}, Verbs: []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
				{APIGroups: []string{"autoscaling"}, Resources: []string{"horizontalpodautoscalers"}, Verbs: []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
				{APIGroups: []string{"infrastructure.ontai.dev"}, Resources: []string{"infrastructurepackexecutions", "infrastructureclusterpacks", "infrastructurepackinstances"}, Verbs: []string{"get", "list", "watch"}},
				{APIGroups: []string{"infrastructure.ontai.dev"}, Resources: []string{"infrastructurerunnerconfigs"}, Verbs: []string{"get", "list", "watch", "patch", "update"}},
				{APIGroups: []string{"security.ontai.dev"}, Resources: []string{"rbacprofiles"}, Verbs: []string{"get", "list", "watch"}},
				{APIGroups: []string{"infrastructure.ontai.dev"}, Resources: []string{"packoperationresults"}, Verbs: []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
			},
		}
		if err := r.Client.Create(ctx, role); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("ensureWrapperRunnerResources: create Role: %w", err)
		}
	}

	// RoleBinding.
	rb := &rbacv1.RoleBinding{}
	if err := r.Client.Get(ctx, types.NamespacedName{Name: "wrapper-runner", Namespace: tenantNS}, rb); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("ensureWrapperRunnerResources: get RoleBinding: %w", err)
		}
		rb = &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "wrapper-runner",
				Namespace: tenantNS,
				Labels:    clusterLabel,
				Annotations: map[string]string{
					"ontai.dev/rbac-owner": "guardian",
				},
			},
			RoleRef:  rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: "wrapper-runner"},
			Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: "wrapper-runner", Namespace: tenantNS}},
		}
		if err := r.Client.Create(ctx, rb); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("ensureWrapperRunnerResources: create RoleBinding: %w", err)
		}
	}

	// ClusterRoleBinding — binds the shared ClusterRole to this tenant's SA.
	crbName := "wrapper-runner-cluster-scoped-" + tc.Name
	crb := &rbacv1.ClusterRoleBinding{}
	if err := r.Client.Get(ctx, types.NamespacedName{Name: crbName}, crb); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("ensureWrapperRunnerResources: get ClusterRoleBinding: %w", err)
		}
		crb = &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:   crbName,
				Labels: clusterLabel,
				Annotations: map[string]string{
					"ontai.dev/rbac-owner": "guardian",
				},
			},
			RoleRef:  rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "wrapper-runner-cluster-scoped"},
			Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: "wrapper-runner", Namespace: tenantNS}},
		}
		if err := r.Client.Create(ctx, crb); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("ensureWrapperRunnerResources: create ClusterRoleBinding: %w", err)
		}
	}

	return nil
}

// ensureCAPIKubeconfig copies the CAPI-generated kubeconfig Secret to the canonical
// seam-mc-{cluster}-kubeconfig name in seam-tenant-{cluster}. CAPI writes
// {cluster}-kubeconfig in the cluster namespace after the cluster reaches Running state.
// All platform operations (EnsureRemoteConductorBootstrap, PKI rotation, conductor-execute
// Jobs) read from the canonical name. Idempotent. Called from reconcileCAPIPath after
// CAPI Cluster reaches Running.
func (r *TalosClusterReconciler) ensureCAPIKubeconfig(ctx context.Context, tc *platformv1alpha1.TalosCluster) error {
	tenantNS := "seam-tenant-" + tc.Name
	dstName := kubeconfigSecretName(tc.Name)

	if err := r.Client.Get(ctx, types.NamespacedName{Name: dstName, Namespace: tenantNS}, &corev1.Secret{}); err == nil {
		return nil
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("ensureCAPIKubeconfig: check %s/%s: %w", tenantNS, dstName, err)
	}

	srcName := tc.Name + "-kubeconfig"
	src := &corev1.Secret{}
	if err := r.Client.Get(ctx, types.NamespacedName{Name: srcName, Namespace: tenantNS}, src); err != nil {
		if apierrors.IsNotFound(err) {
			return nil // CAPI not yet written; reconcile will retry
		}
		return fmt.Errorf("ensureCAPIKubeconfig: get source %s/%s: %w", tenantNS, srcName, err)
	}

	dst := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      dstName,
			Namespace: tenantNS,
			Labels:    map[string]string{"platform.ontai.dev/cluster": tc.Name},
		},
		Type: corev1.SecretTypeOpaque,
		Data: src.Data,
	}
	if err := r.Client.Create(ctx, dst); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("ensureCAPIKubeconfig: create %s/%s: %w", tenantNS, dstName, err)
	}
	return nil
}

// ensureCAPITalosconfig copies the TALM-generated talosconfig Secret to the canonical
// seam-mc-{cluster}-talosconfig name in seam-tenant-{cluster}. TALM writes
// {cluster}-talosconfig in the cluster namespace. The canonical name is what
// ensureExecutorTalosconfig reads as its source, so day-2 executor Jobs receive
// the correct talosconfig in seam-tenant-{cluster}. Idempotent. Called from
// reconcileCAPIPath after CAPI Cluster reaches Running.
func (r *TalosClusterReconciler) ensureCAPITalosconfig(ctx context.Context, tc *platformv1alpha1.TalosCluster) error {
	tenantNS := "seam-tenant-" + tc.Name
	dstName := talosconfigSecretName(tc.Name)

	if err := r.Client.Get(ctx, types.NamespacedName{Name: dstName, Namespace: tenantNS}, &corev1.Secret{}); err == nil {
		return nil
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("ensureCAPITalosconfig: check %s/%s: %w", tenantNS, dstName, err)
	}

	srcName := tc.Name + "-talosconfig"
	src := &corev1.Secret{}
	if err := r.Client.Get(ctx, types.NamespacedName{Name: srcName, Namespace: tenantNS}, src); err != nil {
		if apierrors.IsNotFound(err) {
			return nil // TALM not yet written; reconcile will retry
		}
		return fmt.Errorf("ensureCAPITalosconfig: get source %s/%s: %w", tenantNS, srcName, err)
	}

	dst := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      dstName,
			Namespace: tenantNS,
			Labels:    map[string]string{"platform.ontai.dev/cluster": tc.Name},
		},
		Type: corev1.SecretTypeOpaque,
		Data: src.Data,
	}
	if err := r.Client.Create(ctx, dst); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("ensureCAPITalosconfig: create %s/%s: %w", tenantNS, dstName, err)
	}
	return nil
}
