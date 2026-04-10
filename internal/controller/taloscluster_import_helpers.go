package controller

// taloscluster_import_helpers.go — management cluster import path helpers.
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

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
)

const (
	// importSecretsNamespace is the namespace where import-path Secrets are stored.
	importSecretsNamespace = "seam-system"

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
// import path. It reads the talosconfig Secret from seam-system, uses the talos
// goclient to request a kubeconfig from the cluster, and stores the result as a
// Secret in seam-system.
//
// Returns (ctrl.Result{}, nil) when the kubeconfig Secret is already present
// (idempotent) or has been successfully written.
//
// Returns (ctrl.Result{RequeueAfter: importPollInterval}, nil) when the talosconfig
// Secret is absent — sets KubeconfigUnavailable condition and requeues. Clears the
// condition once the Secret appears and the kubeconfig is written.
//
// Returns (ctrl.Result{}, err) for unexpected API or talos client errors.
//
// platform-schema.md §5 (TalosClusterModeImport).
// CP-INV-001 extension: talos goclient use authorized by Governor directive 2026-04-10.
func (r *TalosClusterReconciler) ensureKubeconfigSecret(ctx context.Context, tc *platformv1alpha1.TalosCluster) (ctrl.Result, error) {
	kubeconfigName := kubeconfigSecretName(tc.Name)

	// Idempotency guard: if kubeconfig Secret already exists, nothing to do.
	existing := &corev1.Secret{}
	err := r.Client.Get(ctx, types.NamespacedName{
		Name:      kubeconfigName,
		Namespace: importSecretsNamespace,
	}, existing)
	if err == nil {
		return ctrl.Result{}, nil
	}
	if !apierrors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("ensureKubeconfigSecret: check kubeconfig secret %s/%s: %w",
			importSecretsNamespace, kubeconfigName, err)
	}

	// Read talosconfig Secret.
	talosconfigName := talosconfigSecretName(tc.Name)
	talosconfigSecret := &corev1.Secret{}
	if err := r.Client.Get(ctx, types.NamespacedName{
		Name:      talosconfigName,
		Namespace: importSecretsNamespace,
	}, talosconfigSecret); err != nil {
		if apierrors.IsNotFound(err) {
			// Talosconfig not yet present. Set condition and requeue.
			platformv1alpha1.SetCondition(
				&tc.Status.Conditions,
				platformv1alpha1.ConditionTypeKubeconfigUnavailable,
				metav1.ConditionTrue,
				platformv1alpha1.ReasonTalosConfigSecretAbsent,
				fmt.Sprintf("Waiting for talosconfig Secret %s/%s — create it before kubeconfig generation can proceed.", importSecretsNamespace, talosconfigName),
				tc.Generation,
			)
			return ctrl.Result{RequeueAfter: bootstrapPollInterval}, nil
		}
		return ctrl.Result{}, fmt.Errorf("ensureKubeconfigSecret: get talosconfig secret %s/%s: %w",
			importSecretsNamespace, talosconfigName, err)
	}

	talosconfigBytes, ok := talosconfigSecret.Data[talosconfigSecretKey]
	if !ok || len(talosconfigBytes) == 0 {
		return ctrl.Result{}, fmt.Errorf("ensureKubeconfigSecret: talosconfig secret %s/%s missing %q key or empty",
			importSecretsNamespace, talosconfigName, talosconfigSecretKey)
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

		// Use spec.clusterEndpoint as the endpoint override so the kubeconfig target
		// address is the cluster VIP, not whatever is recorded in the talosconfig.
		talosC, err := talos_client.New(ctx,
			talos_client.WithConfig(cfg),
			talos_client.WithEndpoints(tc.Spec.ClusterEndpoint),
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

	// Store kubeconfig as a Secret in seam-system.
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kubeconfigName,
			Namespace: importSecretsNamespace,
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
			importSecretsNamespace, kubeconfigName, err)
	}

	// Clear KubeconfigUnavailable if it was previously set.
	platformv1alpha1.SetCondition(
		&tc.Status.Conditions,
		platformv1alpha1.ConditionTypeKubeconfigUnavailable,
		metav1.ConditionFalse,
		"KubeconfigGenerated",
		fmt.Sprintf("Kubeconfig Secret %s/%s generated successfully.", importSecretsNamespace, kubeconfigName),
		tc.Generation,
	)

	return ctrl.Result{}, nil
}
