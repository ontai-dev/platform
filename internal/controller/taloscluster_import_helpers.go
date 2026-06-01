package controller

// taloscluster_import_helpers.go -- management cluster import path helpers.
//
// CP-INV-001 extension (Governor-directed 2026-04-10): talos goclient is permitted
// in this file exclusively for the import kubeconfig generation path. The Platform
// Governor explicitly authorized this third use of talos goclient on 2026-04-10
// as part of the management cluster import flow. The authorized callers are:
//   - seaminfrastructuremachine_reconciler.go (original)
//   - seaminfrastructurecluster_reconciler.go (original)
//   - this file (Governor extension)
//
// No other file in this codebase may import github.com/siderolabs/talos/pkg/machinery.

import (
	"context"
	"fmt"

	talos_client "github.com/siderolabs/talos/pkg/machinery/client"
	clientconfig "github.com/siderolabs/talos/pkg/machinery/client/config"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
)

const (
	// talosconfigSecretKey is the data key under which the raw talosconfig YAML is
	// stored in the seam-mc-{cluster}-talosconfig Secret.
	talosconfigSecretKey = "talosconfig"

	// kubeconfigSecretKey is the data key under which the generated kubeconfig is
	// stored in the seam-mc-{cluster}-kubeconfig Secret.
	kubeconfigSecretKey = "value"

	// importClusterLabel is the label applied to import-path Secrets to record the
	// cluster they belong to.
	importClusterLabel = "platform.ontai.dev/cluster"
)

// importSecretsNamespace returns the namespace where import-path Secrets
// (talosconfig, kubeconfig) and MachineConfig CRs are stored for a given cluster.
// Governor ruling 2026-04-21: seam-tenant-{clusterName} holds these resources.
func importSecretsNamespace(clusterName string) string {
	return "seam-tenant-" + clusterName
}

// talosconfigSecretName returns the name of the talosconfig Secret for a cluster.
// platform-schema.md §9.
func talosconfigSecretName(clusterName string) string {
	return "seam-mc-" + clusterName + "-talosconfig"
}

// kubeconfigSecretName returns the name of the generated kubeconfig Secret.
// platform-schema.md §9.
func kubeconfigSecretName(clusterName string) string {
	return "seam-mc-" + clusterName + "-kubeconfig"
}

// ensureKubeconfigSecret generates a kubeconfig Secret for the management cluster
// import path. It reads the talosconfig Secret from seam-tenant-{cluster}, uses the
// talos goclient to request a kubeconfig from the cluster, and stores the result as
// a Secret in seam-tenant-{cluster}. Governor ruling 2026-04-21.
//
// Returns (ctrl.Result{}, nil) when the kubeconfig Secret is already present
// (idempotent) or has been successfully written.
//
// Returns (ctrl.Result{RequeueAfter: importPollInterval}, nil) when the talosconfig
// Secret is absent -- sets KubeconfigUnavailable condition and requeues. Clears the
// condition once the Secret appears and the kubeconfig is written.
//
// Returns (ctrl.Result{}, err) for unexpected API or talos client errors.
//
// platform-schema.md §5 (TalosClusterModeImport).
// CP-INV-001 extension: talos goclient use authorized by Governor directive 2026-04-10.
func (r *TalosClusterReconciler) ensureKubeconfigSecret(ctx context.Context, tc *platformv1alpha1.TalosCluster) (ctrl.Result, error) {
	kubeconfigName := kubeconfigSecretName(tc.Name)
	secretsNS := importSecretsNamespace(tc.Name)

	// Idempotency guard: if kubeconfig Secret already exists, nothing to do.
	existing := &corev1.Secret{}
	err := r.Client.Get(ctx, types.NamespacedName{
		Name:      kubeconfigName,
		Namespace: secretsNS,
	}, existing)
	if err == nil {
		return ctrl.Result{}, nil
	}
	if !apierrors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("ensureKubeconfigSecret: check kubeconfig secret %s/%s: %w",
			secretsNS, kubeconfigName, err)
	}

	// Read talosconfig Secret.
	talosconfigName := talosconfigSecretName(tc.Name)
	talosconfigSecret := &corev1.Secret{}
	if err := r.Client.Get(ctx, types.NamespacedName{
		Name:      talosconfigName,
		Namespace: secretsNS,
	}, talosconfigSecret); err != nil {
		if apierrors.IsNotFound(err) {
			// Talosconfig not yet present. Set condition and requeue.
			platformv1alpha1.SetCondition(
				&tc.Status.Conditions,
				platformv1alpha1.ConditionTypeKubeconfigUnavailable,
				metav1.ConditionTrue,
				platformv1alpha1.ReasonTalosConfigSecretAbsent,
				fmt.Sprintf("Waiting for talosconfig Secret %s/%s -- create it before kubeconfig generation can proceed.", secretsNS, talosconfigName),
				tc.Generation,
			)
			return ctrl.Result{RequeueAfter: bootstrapPollInterval}, nil
		}
		return ctrl.Result{}, fmt.Errorf("ensureKubeconfigSecret: get talosconfig secret %s/%s: %w",
			secretsNS, talosconfigName, err)
	}

	talosconfigBytes, ok := talosconfigSecret.Data[talosconfigSecretKey]
	if !ok || len(talosconfigBytes) == 0 {
		return ctrl.Result{}, fmt.Errorf("ensureKubeconfigSecret: talosconfig secret %s/%s missing %q key or empty",
			secretsNS, talosconfigName, talosconfigSecretKey)
	}

	// Generate kubeconfig.
	// If KubeconfigGeneratorFn is set (unit test override), use it directly.
	// Otherwise, parse talosconfig and use the talos goclient.
	var kubeconfigBytes []byte
	if r.KubeconfigGeneratorFn != nil {
		var genErr error
		kubeconfigBytes, genErr = r.KubeconfigGeneratorFn(ctx, tc.Name, tc.Spec.ClusterEndpoint)
		if genErr != nil {
			return ctrl.Result{}, fmt.Errorf("ensureKubeconfigSecret: generate kubeconfig for cluster %s: %w", tc.Name, genErr)
		}
	} else {
		cfg, err := clientconfig.FromBytes(talosconfigBytes)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("ensureKubeconfigSecret: parse talosconfig for cluster %s: %w", tc.Name, err)
		}

		// Use the talosconfig endpoints directly. The talosconfig already contains
		// the correct node IPs (port 50000). Do not override with ClusterEndpoint --
		// that is the Kubernetes API VIP (port 6443) which does not serve the Talos API.
		talosC, err := talos_client.New(ctx,
			talos_client.WithConfig(cfg),
		)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("ensureKubeconfigSecret: build talos client for cluster %s: %w", tc.Name, err)
		}
		defer talosC.Close() //nolint:errcheck

		kubeconfigBytes, err = talosC.Kubeconfig(ctx)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("ensureKubeconfigSecret: generate kubeconfig for cluster %s: %w", tc.Name, err)
		}
	}

	// Store kubeconfig Secret in seam-tenant-{cluster}.
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kubeconfigName,
			Namespace: secretsNS,
			Labels: map[string]string{
				importClusterLabel: tc.Name,
			},
		},
		Data: map[string][]byte{
			kubeconfigSecretKey: kubeconfigBytes,
		},
	}
	if err := r.Client.Create(ctx, secret); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensureKubeconfigSecret: create kubeconfig secret %s/%s: %w",
			secretsNS, kubeconfigName, err)
	}

	// Clear KubeconfigUnavailable if it was previously set.
	platformv1alpha1.SetCondition(
		&tc.Status.Conditions,
		platformv1alpha1.ConditionTypeKubeconfigUnavailable,
		metav1.ConditionFalse,
		"KubeconfigGenerated",
		fmt.Sprintf("Kubeconfig Secret %s/%s generated successfully.", secretsNS, kubeconfigName),
		tc.Generation,
	)

	return ctrl.Result{}, nil
}

// ensureMachineConfigCRsExist lists MachineConfig CRs in the cluster's tenant namespace
// and creates a per-node MachineConfigSync CR for each node that does not yet have one.
// Non-fatal when no MachineConfig CRs are found: admin or compiler must create them
// before machineconfig-sync can run. Idempotent.
func (r *TalosClusterReconciler) ensureMachineConfigCRsExist(ctx context.Context, tc *platformv1alpha1.TalosCluster) error {
	ns := importSecretsNamespace(tc.Name)
	logger := log.FromContext(ctx)

	mcList := &platformv1alpha1.MachineConfigList{}
	if err := r.Client.List(ctx, mcList, client.InNamespace(ns)); err != nil {
		return fmt.Errorf("ensureMachineConfigCRsExist: list MachineConfig CRs in %s: %w", ns, err)
	}

	found := 0
	for i := range mcList.Items {
		mc := &mcList.Items[i]
		if mc.Spec.ClusterRef.Name != tc.Name {
			continue
		}
		if mc.Spec.NodeHostname == "" || mc.Spec.NodeIP == "" {
			logger.Info("ensureMachineConfigCRsExist: MachineConfig CR missing hostname or IP, skipping",
				"cr", mc.Name, "cluster", tc.Name)
			continue
		}
		found++
		if err := r.createPerNodeMachineConfigSyncCR(ctx, tc.Name, ns, mc.Spec.NodeHostname, mc.Spec.NodeIP); err != nil {
			return fmt.Errorf("ensureMachineConfigCRsExist: create MachineConfigSync for %s: %w", mc.Spec.NodeHostname, err)
		}
	}

	if found == 0 {
		logger.Info("ensureMachineConfigCRsExist: no MachineConfig CRs found -- admin or compiler must create them",
			"cluster", tc.Name, "namespace", ns)
	}
	return nil
}

// createPerNodeMachineConfigSyncCR creates a per-node MachineConfigSync CR that
// targets a specific node IP via spec.nodeRef. Used in the import path when
// MachineConfig CRs are present. Idempotent.
func (r *TalosClusterReconciler) createPerNodeMachineConfigSyncCR(
	ctx context.Context,
	clusterName, secretsNS, nodeShortName, nodeRef string,
) error {
	crName := clusterName + "-mc-import-" + nodeShortName
	existing := &platformv1alpha1.MachineConfigSync{}
	if err := r.Client.Get(ctx, types.NamespacedName{Name: crName, Namespace: secretsNS}, existing); err == nil {
		return nil
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("check MachineConfigSync %s/%s: %w", secretsNS, crName, err)
	}

	mcs := &platformv1alpha1.MachineConfigSync{
		ObjectMeta: metav1.ObjectMeta{
			Name:      crName,
			Namespace: secretsNS,
		},
		Spec: platformv1alpha1.MachineConfigSyncSpec{
			ClusterRef: platformv1alpha1.LocalObjectRef{Name: clusterName},
			NodeClass:  nodeShortName,
			NodeRef:    nodeRef,
			Reason:     "import-initial-sync",
		},
	}
	if err := r.Client.Create(ctx, mcs); err != nil {
		return fmt.Errorf("create MachineConfigSync %s/%s: %w", secretsNS, crName, err)
	}
	log.FromContext(ctx).Info("ensureMachineConfigCRsExist: created per-node MachineConfigSync CR",
		"cluster", clusterName, "nodeShortName", nodeShortName, "nodeRef", nodeRef)
	return nil
}

// reconcileMachineConfigSync detects MachineConfig CR generation changes and creates
// or replaces a watch-triggered MachineConfigSync CR for each node whose MC generation
// differs from the recorded annotation. This fires only when an admin has updated the
// MachineConfig CR spec since the last sync was triggered.
//
// Watch-triggered CRs are named {cluster}-mc-sync-{hostname}, distinct from the
// import-triggered {cluster}-mc-import-{hostname} CRs created by ensureMachineConfigCRsExist.
//
// The annotation platform.ontai.dev/mc-generation on the sync CR records the MC
// generation that triggered creation. A mismatch triggers replacement.
func (r *TalosClusterReconciler) reconcileMachineConfigSync(ctx context.Context, tc *platformv1alpha1.TalosCluster) error {
	ns := importSecretsNamespace(tc.Name)
	logger := log.FromContext(ctx)

	mcList := &platformv1alpha1.MachineConfigList{}
	if err := r.Client.List(ctx, mcList, client.InNamespace(ns)); err != nil {
		return fmt.Errorf("reconcileMachineConfigSync: list MachineConfig CRs: %w", err)
	}

	for i := range mcList.Items {
		mc := &mcList.Items[i]
		if mc.Spec.ClusterRef.Name != tc.Name || mc.Spec.NodeHostname == "" {
			continue
		}

		genStr := fmt.Sprintf("%d", mc.Generation)
		crName := tc.Name + "-mc-sync-" + mc.Spec.NodeHostname

		existing := &platformv1alpha1.MachineConfigSync{}
		getErr := r.Client.Get(ctx, types.NamespacedName{Name: crName, Namespace: ns}, existing)
		if getErr == nil {
			if existing.Annotations[AnnotationMCGeneration] == genStr {
				continue
			}
			// Generation changed -- replace the stale sync CR.
			if delErr := r.Client.Delete(ctx, existing); delErr != nil && !apierrors.IsNotFound(delErr) {
				return fmt.Errorf("reconcileMachineConfigSync: delete stale CR %s/%s: %w", ns, crName, delErr)
			}
		} else if !apierrors.IsNotFound(getErr) {
			return fmt.Errorf("reconcileMachineConfigSync: get CR %s/%s: %w", ns, crName, getErr)
		}

		newCR := &platformv1alpha1.MachineConfigSync{
			ObjectMeta: metav1.ObjectMeta{
				Name:      crName,
				Namespace: ns,
				Annotations: map[string]string{
					AnnotationMCGeneration: genStr,
				},
			},
			Spec: platformv1alpha1.MachineConfigSyncSpec{
				ClusterRef: platformv1alpha1.LocalObjectRef{Name: tc.Name},
				NodeClass:  mc.Spec.NodeHostname,
				NodeRef:    mc.Spec.NodeIP,
				Reason:     "mc-generation-changed",
			},
		}
		if cErr := r.Client.Create(ctx, newCR); cErr != nil && !apierrors.IsAlreadyExists(cErr) {
			return fmt.Errorf("reconcileMachineConfigSync: create CR %s/%s: %w", ns, crName, cErr)
		}
		logger.Info("reconcileMachineConfigSync: created MachineConfigSync CR for generation change",
			"cluster", tc.Name, "hostname", mc.Spec.NodeHostname, "generation", mc.Generation)
	}
	return nil
}
