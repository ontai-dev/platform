package identity

import (
	"context"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	seamv1alpha1 "github.com/ontai-dev/seam/api/v1alpha1"
	"github.com/ontai-dev/seam-sdk/conditions"
	"github.com/ontai-dev/seam-sdk/labels"
	"github.com/ontai-dev/seam-sdk/operator"
)

// SeamIdentity implements operator.SeamOperator for the platform operator.
type SeamIdentity struct{}

var _ operator.SeamOperator = (*SeamIdentity)(nil)

func (s *SeamIdentity) OperatorName() string       { return "platform" }
func (s *SeamIdentity) MembershipCRName() string   { return "seam-platform" }
func (s *SeamIdentity) ReadyConditionType() string { return conditions.ConditionReady }
func (s *SeamIdentity) Domain() string             { return "seam.ontai.dev" }
func (s *SeamIdentity) Subdomain() string          { return "infrastructure" }
func (s *SeamIdentity) ConditionTypes() []string {
	return []string{
		conditions.ConditionReady,
		conditions.ConditionSeamMembershipProvisioned,
		conditions.ConditionRBACProfileActive,
		conditions.ConditionReconciling,
		conditions.ConditionDegraded,
	}
}
func (s *SeamIdentity) LineageLabelSchema() map[string]string {
	return map[string]string{
		labels.LabelManagedBy:                "platform",
		labels.LabelRootDeclarationKind:      "",
		labels.LabelRootDeclarationName:      "",
		labels.LabelRootDeclarationNamespace: "",
	}
}

// EnsureSeamMembership creates the SeamMembership CR for the platform operator
// in seam-system. Idempotent: AlreadyExists is not an error.
func EnsureSeamMembership(ctx context.Context, c client.Client) error {
	id := &SeamIdentity{}
	sm := &seamv1alpha1.SeamMembership{
		ObjectMeta: metav1.ObjectMeta{
			Name:      id.MembershipCRName(),
			Namespace: "seam-system",
		},
		Spec: seamv1alpha1.SeamMembershipSpec{
			AppIdentityRef:    id.OperatorName(),
			DomainIdentityRef: id.OperatorName(),
			PrincipalRef:      "system:serviceaccount:seam-system:" + id.OperatorName(),
			Tier:              "infrastructure",
		},
	}
	if err := c.Create(ctx, sm); err != nil && !k8serrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}
