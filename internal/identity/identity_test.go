package identity_test

import (
	"context"
	"testing"

	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	seamv1alpha1 "github.com/ontai-dev/seam/api/v1alpha1"
	"github.com/ontai-dev/platform/internal/identity"
	"github.com/ontai-dev/seam-sdk/conditions"
	"github.com/ontai-dev/seam-sdk/operator"
)

var _ operator.SeamOperator = (*identity.SeamIdentity)(nil)

func newScheme(t *testing.T) *k8sruntime.Scheme {
	t.Helper()
	s := k8sruntime.NewScheme()
	if err := seamv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	return s
}

func TestSeamIdentity_Values(t *testing.T) {
	id := &identity.SeamIdentity{}
	if got := id.OperatorName(); got != "platform" {
		t.Errorf("OperatorName() = %q, want %q", got, "platform")
	}
	if got := id.MembershipCRName(); got != "seam-platform" {
		t.Errorf("MembershipCRName() = %q, want %q", got, "seam-platform")
	}
	if got := id.ReadyConditionType(); got != conditions.ConditionReady {
		t.Errorf("ReadyConditionType() = %q, want %q", got, conditions.ConditionReady)
	}
	if got := id.Domain(); got != "seam.ontai.dev" {
		t.Errorf("Domain() = %q, want %q", got, "seam.ontai.dev")
	}
	if got := id.Subdomain(); got != "infrastructure" {
		t.Errorf("Subdomain() = %q, want %q", got, "infrastructure")
	}
}

func TestSeamIdentity_ConditionTypes_ContainsReady(t *testing.T) {
	id := &identity.SeamIdentity{}
	for _, ct := range id.ConditionTypes() {
		if ct == conditions.ConditionReady {
			return
		}
	}
	t.Error("ConditionTypes() does not include conditions.ConditionReady")
}

func TestSeamIdentity_LineageLabelSchema_HasManagedBy(t *testing.T) {
	id := &identity.SeamIdentity{}
	schema := id.LineageLabelSchema()
	v, ok := schema["seam.ontai.dev/managed-by"]
	if !ok {
		t.Fatal("LineageLabelSchema() missing seam.ontai.dev/managed-by")
	}
	if v != "platform" {
		t.Errorf("seam.ontai.dev/managed-by = %q, want %q", v, "platform")
	}
}

func TestEnsureSeamMembership_Creates(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).Build()
	if err := identity.EnsureSeamMembership(context.Background(), c); err != nil {
		t.Fatalf("EnsureSeamMembership: %v", err)
	}
	sm := &seamv1alpha1.SeamMembership{}
	key := types.NamespacedName{Name: "seam-platform", Namespace: "seam-system"}
	if err := c.Get(context.Background(), key, sm); err != nil {
		t.Fatalf("Get SeamMembership: %v", err)
	}
	if sm.Spec.AppIdentityRef != "platform" {
		t.Errorf("AppIdentityRef = %q, want %q", sm.Spec.AppIdentityRef, "platform")
	}
	if sm.Spec.Tier != "infrastructure" {
		t.Errorf("Tier = %q, want %q", sm.Spec.Tier, "infrastructure")
	}
}

func TestEnsureSeamMembership_Idempotent(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).Build()
	if err := identity.EnsureSeamMembership(context.Background(), c); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if err := identity.EnsureSeamMembership(context.Background(), c); err != nil {
		t.Fatalf("second call (idempotency): %v", err)
	}
}
