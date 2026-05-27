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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"

	talos_client "github.com/siderolabs/talos/pkg/machinery/client"
	clientconfig "github.com/siderolabs/talos/pkg/machinery/client/config"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
	sigsyaml "sigs.k8s.io/yaml"

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
// (talosconfig, kubeconfig) are stored for a given cluster.
// Governor ruling 2026-04-21: seam-tenant-{clusterName} holds these Secrets.
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
// Secret is absent — sets KubeconfigUnavailable condition and requeues. Clears the
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
		// the correct node IPs (port 50000). Do not override with ClusterEndpoint —
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

// machineConfigTypeKey is the YAML key path for the machine type field in a Talos machineconfig.
// The value is "controlplane" or "worker".
type machineTypeExtract struct {
	Machine struct {
		Type string `yaml:"type"`
	} `yaml:"machine"`
}

// ensureMachineConfigSecrets reads the running machineconfig from every node endpoint
// in the cluster's talosconfig Secret, classifies nodes by machine.type, and writes
// one source-of-truth Secret per class (controlplane, worker) to seam-tenant-{cluster}.
// For each class, it also creates a MachineConfigSync CR so the conductor will inject
// the ONT-controlled node label via the machineconfig-sync capability.
//
// Called during the import flow after ensureKubeconfigSecret succeeds and before the
// Bootstrapped=True condition transition. Idempotent: existing secrets and MachineConfigSync
// CRs are preserved (secret content is only created, not overwritten on re-run).
//
// CP-INV-001 extension: talos goclient use is authorized for this file by Governor directive.
// RECON-A2.
func (r *TalosClusterReconciler) ensureMachineConfigSecrets(ctx context.Context, tc *platformv1alpha1.TalosCluster) error {
	secretsNS := importSecretsNamespace(tc.Name)

	// Read the talosconfig secret to obtain node endpoints.
	talosconfigSecret := &corev1.Secret{}
	if err := r.Client.Get(ctx, types.NamespacedName{
		Name:      talosconfigSecretName(tc.Name),
		Namespace: secretsNS,
	}, talosconfigSecret); err != nil {
		return fmt.Errorf("ensureMachineConfigSecrets: get talosconfig secret: %w", err)
	}

	talosconfigBytes, ok := talosconfigSecret.Data[talosconfigSecretKey]
	if !ok || len(talosconfigBytes) == 0 {
		return fmt.Errorf("ensureMachineConfigSecrets: talosconfig secret missing %q key", talosconfigSecretKey)
	}

	cfg, err := clientconfig.FromBytes(talosconfigBytes)
	if err != nil {
		return fmt.Errorf("ensureMachineConfigSecrets: parse talosconfig: %w", err)
	}

	activeCtx, ok := cfg.Contexts[cfg.Context]
	if !ok || len(activeCtx.Endpoints) == 0 {
		return fmt.Errorf("ensureMachineConfigSecrets: talosconfig has no endpoints in context %q", cfg.Context)
	}

	// Build a per-node reader. When MachineConfigReaderFn is set (unit tests),
	// use it to avoid establishing a real talos goclient connection.
	readNode := r.buildMachineConfigNodeReader(ctx, tc.Name, talosconfigBytes)

	// Collect the first machineconfig seen for each class (controlplane, worker).
	classConfigs := map[string][]byte{}

	for _, endpoint := range activeCtx.Endpoints {
		if _, done := classConfigs[MachineConfigClassControlPlane]; done {
			if _, done2 := classConfigs[MachineConfigClassWorker]; done2 {
				break // Both classes collected; no need to read more nodes.
			}
		}

		configBytes, nodeClass, rErr := readNode(endpoint)
		if rErr != nil {
			log.FromContext(ctx).Info("ensureMachineConfigSecrets: could not read machineconfig from node (skipping)",
				"node", endpoint, "error", rErr.Error())
			continue
		}
		if nodeClass == "" {
			continue
		}
		if _, exists := classConfigs[nodeClass]; !exists {
			classConfigs[nodeClass] = configBytes
		}
	}

	if len(classConfigs) == 0 {
		return fmt.Errorf("ensureMachineConfigSecrets: could not read machineconfig from any node in cluster %s", tc.Name)
	}

	// Create/skip source-of-truth Secrets and MachineConfigSync CRs per class.
	for class, configBytes := range classConfigs {
		if wErr := r.writeMachineConfigSecret(ctx, tc.Name, secretsNS, class, configBytes); wErr != nil {
			return fmt.Errorf("ensureMachineConfigSecrets: write secret for class %s: %w", class, wErr)
		}
		if wErr := r.createMachineConfigSyncCR(ctx, tc.Name, secretsNS, class); wErr != nil {
			return fmt.Errorf("ensureMachineConfigSecrets: create MachineConfigSync for class %s: %w", class, wErr)
		}
	}

	return nil
}

// buildMachineConfigNodeReader returns a per-node reader function.
// When MachineConfigReaderFn is set, it wraps it directly. Otherwise, it creates
// a real talos goclient from talosconfigBytes. Returns configBytes, machineClass, error.
func (r *TalosClusterReconciler) buildMachineConfigNodeReader(
	ctx context.Context,
	clusterName string,
	talosconfigBytes []byte,
) func(endpoint string) ([]byte, string, error) {
	if r.MachineConfigReaderFn != nil {
		fn := r.MachineConfigReaderFn
		return func(endpoint string) ([]byte, string, error) {
			return fn(ctx, clusterName, endpoint)
		}
	}

	// Production path: one talos client for all nodes, using per-node context.
	cfg, _ := clientconfig.FromBytes(talosconfigBytes)
	talosC, err := talos_client.New(ctx, talos_client.WithConfig(cfg))
	if err != nil {
		return func(endpoint string) ([]byte, string, error) {
			return nil, "", fmt.Errorf("build talos client: %w", err)
		}
	}

	return func(endpoint string) ([]byte, string, error) {
		nodeCtx := talos_client.WithNode(ctx, endpoint)
		rc, rErr := talosC.Read(nodeCtx, "/system/state/config.yaml")
		if rErr != nil {
			return nil, "", rErr
		}
		defer rc.Close() //nolint:errcheck

		configBytes, rErr := io.ReadAll(rc)
		if rErr != nil {
			return nil, "", rErr
		}

		var extract machineTypeExtract
		if yErr := sigsyaml.Unmarshal(configBytes, &extract); yErr != nil {
			return nil, "", fmt.Errorf("parse machineconfig YAML: %w", yErr)
		}

		switch extract.Machine.Type {
		case "controlplane", "init":
			return configBytes, MachineConfigClassControlPlane, nil
		case "worker":
			return configBytes, MachineConfigClassWorker, nil
		default:
			return nil, "", fmt.Errorf("unknown machine.type %q", extract.Machine.Type)
		}
	}
}

// writeMachineConfigSecret creates or skips the machineconfig source-of-truth Secret
// for a given cluster and class. If the secret already exists, it is left unchanged
// (the admin may have pre-created it, or a prior import run wrote it). Idempotent.
func (r *TalosClusterReconciler) writeMachineConfigSecret(
	ctx context.Context,
	clusterName, secretsNS, class string,
	configBytes []byte,
) error {
	secretName := MachineConfigSecretName(clusterName, class)
	existing := &corev1.Secret{}
	if err := r.Client.Get(ctx, types.NamespacedName{Name: secretName, Namespace: secretsNS}, existing); err == nil {
		// Secret already exists; import does not overwrite admin-created or prior-run secrets.
		return nil
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("check secret %s/%s: %w", secretsNS, secretName, err)
	}

	hash := sha256.Sum256(configBytes)
	hashHex := hex.EncodeToString(hash[:])

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: secretsNS,
			Labels: map[string]string{
				LabelMachineConfigCluster:    clusterName,
				LabelMachineConfigClass:      class,
				LabelMachineConfigSyncStatus: MachineConfigSyncStatusPending,
				LabelMachineConfigSyncHash:   hashHex,
			},
		},
		Data: map[string][]byte{
			MachineConfigDataKey: configBytes,
		},
	}
	if err := r.Client.Create(ctx, secret); err != nil {
		return fmt.Errorf("create secret %s/%s: %w", secretsNS, secretName, err)
	}
	log.FromContext(ctx).Info("ensureMachineConfigSecrets: created machineconfig secret",
		"cluster", clusterName, "class", class, "hash", hashHex[:8])
	return nil
}

// createMachineConfigSyncCR creates a MachineConfigSync CR in secretsNS so the
// conductor will schedule a sync Job to inject the ONT-controlled node label.
// Idempotent: skips creation if the CR already exists.
// RECON-A2: reason="import-initial-sync".
func (r *TalosClusterReconciler) createMachineConfigSyncCR(
	ctx context.Context,
	clusterName, secretsNS, class string,
) error {
	crName := clusterName + "-mc-import-" + class
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
			NodeClass:  class,
			Reason:     "import-initial-sync",
		},
	}
	if err := r.Client.Create(ctx, mcs); err != nil {
		return fmt.Errorf("create MachineConfigSync %s/%s: %w", secretsNS, crName, err)
	}
	log.FromContext(ctx).Info("ensureMachineConfigSecrets: created MachineConfigSync CR",
		"cluster", clusterName, "class", class)
	return nil
}

