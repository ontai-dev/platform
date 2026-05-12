// Package controller_test tests the tenant-cluster onboarding helpers.
//
// These tests verify the bootstrap-window RBAC setup (EnsureRemoteConductorRBAC)
// and the InfrastructureTalosCluster CR copy (EnsureRemoteTalosClusterCopy) that
// platform applies to the tenant cluster as part of the T-19 import path.
//
// Both functions operate against remote cluster clients (kubernetes.Interface and
// dynamic.Interface). The tests use the respective fake clients from client-go so
// no live cluster is required.
//
// platform-schema.md §12 steps 5-6. Decision H drift detection loop.
package controller_test

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
	"github.com/ontai-dev/platform/internal/controller"
)

// buildTenantTC builds a minimal TalosCluster for the role=tenant import path.
func buildTenantTC(name string) *platformv1alpha1.TalosCluster {
	return &platformv1alpha1.TalosCluster{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "seam-system"},
		Spec: platformv1alpha1.TalosClusterSpec{
			Mode:              platformv1alpha1.TalosClusterModeImport,
			Role:              platformv1alpha1.TalosClusterRoleTenant,
			TalosVersion:      "v1.9.3",
			KubernetesVersion: "1.32.3",
			ClusterEndpoint:   "10.20.0.20:6443",
		},
	}
}

// --- EnsureRemoteConductorRBAC tests ---

// TestEnsureRemoteConductorRBAC_CreatesClusterRoleAndBinding verifies that
// EnsureRemoteConductorRBAC creates the conductor-agent-tenant ClusterRole with
// the expected API groups/resources, and binds it to the conductor ServiceAccount
// in ont-system via a ClusterRoleBinding.
func TestEnsureRemoteConductorRBAC_CreatesClusterRoleAndBinding(t *testing.T) {
	k8s := k8sfake.NewSimpleClientset()

	if err := controller.EnsureRemoteConductorRBAC(context.Background(), k8s); err != nil {
		t.Fatalf("EnsureRemoteConductorRBAC: %v", err)
	}

	// Verify ClusterRole exists with the correct name.
	cr, err := k8s.RbacV1().ClusterRoles().Get(context.Background(), "conductor-agent-tenant", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("ClusterRole not created: %v", err)
	}

	// Must include infrastructure.ontai.dev for governance CR drift detection.
	hasInfra := false
	hasSecurity := false
	hasCoordination := false
	hasRBAC := false
	for _, rule := range cr.Rules {
		for _, group := range rule.APIGroups {
			if group == "infrastructure.ontai.dev" {
				hasInfra = true
			}
			if group == "security.ontai.dev" {
				hasSecurity = true
			}
			if group == "coordination.k8s.io" {
				hasCoordination = true
			}
			if group == "rbac.authorization.k8s.io" {
				hasRBAC = true
			}
		}
	}
	if !hasInfra {
		t.Error("ClusterRole missing infrastructure.ontai.dev API group rule")
	}
	if !hasSecurity {
		t.Error("ClusterRole missing security.ontai.dev API group rule")
	}
	if !hasCoordination {
		t.Error("ClusterRole missing coordination.k8s.io API group rule (required for leader election leases)")
	}
	if !hasRBAC {
		t.Error("ClusterRole missing rbac.authorization.k8s.io API group rule (required for drift detection sweep)")
	}

	// Verify ClusterRoleBinding exists and references the conductor ServiceAccount.
	crb, err := k8s.RbacV1().ClusterRoleBindings().Get(context.Background(), "conductor-agent-tenant", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("ClusterRoleBinding not created: %v", err)
	}
	if crb.RoleRef.Name != "conductor-agent-tenant" {
		t.Errorf("RoleRef.Name = %q, want %q", crb.RoleRef.Name, "conductor-agent-tenant")
	}
	if len(crb.Subjects) == 0 {
		t.Fatal("ClusterRoleBinding has no subjects")
	}
	found := false
	for _, s := range crb.Subjects {
		if s.Kind == "ServiceAccount" && s.Name == "conductor" && s.Namespace == "ont-system" {
			found = true
		}
	}
	if !found {
		t.Error("ClusterRoleBinding does not bind ont-system/conductor ServiceAccount")
	}
}

// TestEnsureRemoteConductorRBAC_Idempotent verifies that calling
// EnsureRemoteConductorRBAC twice does not return an error (AlreadyExists is
// swallowed). The second call must not duplicate any resource.
func TestEnsureRemoteConductorRBAC_Idempotent(t *testing.T) {
	k8s := k8sfake.NewSimpleClientset()

	if err := controller.EnsureRemoteConductorRBAC(context.Background(), k8s); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if err := controller.EnsureRemoteConductorRBAC(context.Background(), k8s); err != nil {
		t.Fatalf("second call (idempotent): %v", err)
	}

	// Exactly one ClusterRole with the expected name.
	list, err := k8s.RbacV1().ClusterRoles().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, item := range list.Items {
		if item.Name == "conductor-agent-tenant" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 conductor-agent-tenant ClusterRole, got %d", count)
	}
}

// TestEnsureRemoteConductorRBAC_LabelsManagedByPlatform verifies that both the
// ClusterRole and ClusterRoleBinding carry the runner.ontai.dev/managed-by=platform
// label so guardian can identify bootstrap-window resources. INV-020.
func TestEnsureRemoteConductorRBAC_LabelsManagedByPlatform(t *testing.T) {
	k8s := k8sfake.NewSimpleClientset()
	if err := controller.EnsureRemoteConductorRBAC(context.Background(), k8s); err != nil {
		t.Fatalf("EnsureRemoteConductorRBAC: %v", err)
	}

	cr, err := k8s.RbacV1().ClusterRoles().Get(context.Background(), "conductor-agent-tenant", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if cr.Labels["runner.ontai.dev/managed-by"] != "platform" {
		t.Errorf("ClusterRole managed-by label = %q, want %q",
			cr.Labels["runner.ontai.dev/managed-by"], "platform")
	}

	crb, err := k8s.RbacV1().ClusterRoleBindings().Get(context.Background(), "conductor-agent-tenant", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if crb.Labels["runner.ontai.dev/managed-by"] != "platform" {
		t.Errorf("ClusterRoleBinding managed-by label = %q, want %q",
			crb.Labels["runner.ontai.dev/managed-by"], "platform")
	}
}

// --- EnsureRemoteTalosClusterCopy tests ---

// TestEnsureRemoteTalosClusterCopy_CreatesCR verifies that EnsureRemoteTalosClusterCopy
// creates an InfrastructureTalosCluster CR in ont-system on the tenant cluster with
// the spec fields copied from the management cluster TalosCluster. Decision H.
func TestEnsureRemoteTalosClusterCopy_CreatesCR(t *testing.T) {
	tc := buildTenantTC("ccs-dev")
	dynClient := fake.NewSimpleDynamicClient(runtime.NewScheme())

	if err := controller.EnsureRemoteTalosClusterCopy(context.Background(), dynClient, tc); err != nil {
		t.Fatalf("EnsureRemoteTalosClusterCopy: %v", err)
	}

	gvr := schema.GroupVersionResource{
		Group:    "seam.ontai.dev",
		Version:  "v1alpha1",
		Resource: "talosclusters",
	}
	obj, err := dynClient.Resource(gvr).Namespace("ont-system").Get(context.Background(), "ccs-dev", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("CR not created in ont-system: %v", err)
	}

	spec, ok := obj.Object["spec"].(map[string]interface{})
	if !ok {
		t.Fatal("CR spec is missing or wrong type")
	}
	if spec["talosVersion"] != "v1.9.3" {
		t.Errorf("talosVersion = %q, want %q", spec["talosVersion"], "v1.9.3")
	}
	if spec["clusterEndpoint"] != "10.20.0.20:6443" {
		t.Errorf("clusterEndpoint = %q, want %q", spec["clusterEndpoint"], "10.20.0.20:6443")
	}
	if spec["role"] != "tenant" {
		t.Errorf("role = %q, want %q", spec["role"], "tenant")
	}
	if spec["mode"] != "import" {
		t.Errorf("mode = %q, want %q", spec["mode"], "import")
	}

	labels := obj.GetLabels()
	if labels["ontai.dev/managed-by"] != "platform" {
		t.Errorf("CR managed-by label = %q, want %q", labels["ontai.dev/managed-by"], "platform")
	}
	if labels["ontai.dev/cluster-source"] != "management" {
		t.Errorf("CR cluster-source label = %q, want %q", labels["ontai.dev/cluster-source"], "management")
	}
}

// TestEnsureRemoteTalosClusterCopy_Idempotent verifies that calling
// EnsureRemoteTalosClusterCopy twice does not return an error. The second call
// finds the existing CR and skips creation.
func TestEnsureRemoteTalosClusterCopy_Idempotent(t *testing.T) {
	tc := buildTenantTC("ccs-dev-idem")
	dynClient := fake.NewSimpleDynamicClient(runtime.NewScheme())

	if err := controller.EnsureRemoteTalosClusterCopy(context.Background(), dynClient, tc); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if err := controller.EnsureRemoteTalosClusterCopy(context.Background(), dynClient, tc); err != nil {
		t.Fatalf("second call (idempotent): %v", err)
	}
}

// TestEnsureRemoteTalosClusterCopy_CRDNotYetInstalled verifies that if the
// InfrastructureTalosCluster CRD is not yet installed on the tenant cluster
// (dynamic client returns NotFound on Create), the function returns nil rather
// than an error. SC-INV-003: seam-core enable bundle may not yet be applied.
// The next reconcile retries when the CRD is available.
func TestEnsureRemoteTalosClusterCopy_CRDNotYetInstalled(t *testing.T) {
	tc := buildTenantTC("ccs-dev")
	dynClient := fake.NewSimpleDynamicClient(runtime.NewScheme())

	// Inject a reactor that returns NotFound for both GET and CREATE on
	// infrastructuretalosclusters, simulating a cluster where the CRD is absent.
	notFoundErr := apierrors.NewNotFound(
		schema.GroupResource{Group: "seam.ontai.dev", Resource: "talosclusters"},
		"ccs-dev",
	)
	dynClient.Fake.PrependReactor("*", "talosclusters",
		func(_ k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, notFoundErr
		},
	)

	if err := controller.EnsureRemoteTalosClusterCopy(context.Background(), dynClient, tc); err != nil {
		t.Fatalf("expected nil when CRD not installed, got: %v", err)
	}
}
