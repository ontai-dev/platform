package controller

import (
	"context"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
)

const (
	// bootstrapPollInterval is the requeue interval while waiting for a bootstrap Job.
	bootstrapPollInterval = 15 * time.Second

	// capiPollInterval is the requeue interval while waiting for CAPI status transitions.
	capiPollInterval = 20 * time.Second

	// bootstrapCapability is the Conductor executor capability for cluster bootstrap.
	// Verify against conductor-schema.md §capabilities table.
	bootstrapCapability = "cluster-bootstrap"

	// operationResultConfigMapSuffix is appended to the job name to form the
	// OperationResult ConfigMap name.
	operationResultConfigMapSuffix = "-result"

	// tenantNamespaceLabel is the namespace label applied to all tenant namespaces.
	tenantNamespaceLabel = "ontai.dev/tenant"

	// clusterNamespaceLabel is the namespace label applied to identify the cluster.
	clusterNamespaceLabel = "ontai.dev/cluster"
)

// bootstrapJobName returns the Kubernetes Job name for the bootstrap Job of a
// given TalosCluster.
func bootstrapJobName(clusterName string) string {
	return fmt.Sprintf("%s-bootstrap", clusterName)
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
	status, _ := cm.Data["status"]
	msg, _ := cm.Data["message"]
	switch status {
	case "success":
		return true, false, msg
	case "failure", "failed":
		return false, true, msg
	default:
		return false, false, msg
	}
}

// ensureTenantNamespace creates the tenant-{cluster-name} namespace if it does
// not exist. Platform is the sole namespace creation authority. CP-INV-004.
// platform-design.md §7.
func (r *TalosClusterReconciler) ensureTenantNamespace(ctx context.Context, tc *platformv1alpha1.TalosCluster) error {
	ns := &corev1.Namespace{}
	nsName := "tenant-" + tc.Name
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
	nsName := "tenant-" + tc.Name
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

		if err := r.Client.Create(ctx, sic); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("ensureSeamInfrastructureCluster: create: %w", err)
		}
	}
	return nil
}

// ensureCAPICluster creates the CAPI Cluster object in the tenant namespace if
// it does not exist. Owned by TalosCluster. CP-INV-008.
func (r *TalosClusterReconciler) ensureCAPICluster(ctx context.Context, tc *platformv1alpha1.TalosCluster) error {
	nsName := "tenant-" + tc.Name
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
	nsName := "tenant-" + tc.Name
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
		machineConfigPatches := []interface{}{
			map[string]interface{}{
				"op":    "replace",
				"path":  "/cluster/network/cni/name",
				"value": "none",
			},
			// Cilium-required BPF kernel parameters.
			map[string]interface{}{
				"op":    "add",
				"path":  "/machine/sysctls",
				"value": map[string]interface{}{
					"net.core.bpf_jit_harden": "1",
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
	nsName := "tenant-" + tc.Name
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

		replicas := int64(tc.Spec.CAPI.ControlPlane.Replicas)
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

		if err := r.Client.Create(ctx, tcp); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("ensureTalosControlPlane: create: %w", err)
		}
	}
	return nil
}

// ensureWorkerPool creates the MachineDeployment and SeamInfrastructureMachineTemplate
// for a worker pool if they do not exist. platform-schema.md §2.2.
func (r *TalosClusterReconciler) ensureWorkerPool(ctx context.Context, tc *platformv1alpha1.TalosCluster, pool platformv1alpha1.CAPIWorkerPool) error {
	nsName := "tenant-" + tc.Name
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
	nsName := "tenant-" + tc.Name
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
	nsName := "tenant-" + tc.Name
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

// boolPtr returns a pointer to a bool value.
func boolPtr(b bool) *bool { return &b }
