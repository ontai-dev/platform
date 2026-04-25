package controller

// taloscluster_version_upgrade.go implements spec.versionUpgrade handling and
// version anti-regression protection for InfrastructureTalosCluster CRs.
//
// Version upgrade path:
//   - spec.versionUpgrade=true on a Ready cluster auto-creates an UpgradePolicy CR.
//   - The UpgradePolicy reconciler drives the Conductor Job.
//   - On completion, UpgradePolicy reconciler patches status.observedTalosVersion.
//   - TalosClusterReconciler detects UpgradePolicy Ready=True and clears
//     spec.versionUpgrade via spec patch, setting VersionUpgradePending=False.
//
// Anti-regression:
//   - If spec.talosVersion < status.observedTalosVersion, the reconciler sets
//     VersionRegressionBlocked=True and returns without submitting any upgrade.
//     The cluster remains at the currently running version until spec is corrected.

import (
	"context"
	"fmt"

	"github.com/Masterminds/semver/v3"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
)

const (
	// labelVersionUpgradeOwned marks an UpgradePolicy that was auto-created by
	// the TalosCluster reconciler in response to spec.versionUpgrade=true.
	labelVersionUpgradeOwned = "platform.ontai.dev/version-upgrade-owned"

	// versionUpgradeSuffix is appended to the TalosCluster name to form the
	// auto-generated UpgradePolicy name.
	versionUpgradeSuffix = "-version-upgrade"
)

// checkVersionRegression returns true and sets VersionRegressionBlocked=True when
// spec.talosVersion is a lower semver than status.observedTalosVersion. Returns false
// when the check passes (no regression) or when either version is not set.
func checkVersionRegression(tc *platformv1alpha1.TalosCluster) bool {
	if tc.Spec.TalosVersion == "" || tc.Status.ObservedTalosVersion == "" {
		return false
	}
	specVer, err := semver.NewVersion(tc.Spec.TalosVersion)
	if err != nil {
		return false
	}
	observedVer, err := semver.NewVersion(tc.Status.ObservedTalosVersion)
	if err != nil {
		return false
	}
	if specVer.LessThan(observedVer) {
		platformv1alpha1.SetCondition(
			&tc.Status.Conditions,
			platformv1alpha1.ConditionTypeVersionRegressionBlocked,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonVersionRegressionAttempted,
			fmt.Sprintf(
				"spec.talosVersion %s is lower than the cluster's current running version %s. "+
					"Version regression is blocked. Update spec.talosVersion to %s or higher, "+
					"or create an UpgradePolicy CR to upgrade explicitly.",
				tc.Spec.TalosVersion, tc.Status.ObservedTalosVersion, tc.Status.ObservedTalosVersion,
			),
			tc.Generation,
		)
		return true
	}
	// Regression cleared: spec version is acceptable.
	existing := platformv1alpha1.FindCondition(tc.Status.Conditions, platformv1alpha1.ConditionTypeVersionRegressionBlocked)
	if existing != nil && existing.Status == metav1.ConditionTrue {
		platformv1alpha1.SetCondition(
			&tc.Status.Conditions,
			platformv1alpha1.ConditionTypeVersionRegressionBlocked,
			metav1.ConditionFalse,
			platformv1alpha1.ReasonVersionRegressionAttempted,
			"spec.talosVersion is at or above the cluster running version.",
			tc.Generation,
		)
	}
	return false
}

// reconcileVersionUpgrade handles spec.versionUpgrade=true on a Ready cluster.
// It creates an UpgradePolicy CR if one does not already exist, and watches for
// completion to clear the flag and update VersionUpgradePending.
// Returns (done, result, error) where done=true means this reconcile pass is
// complete and the caller should return result.
func (r *TalosClusterReconciler) reconcileVersionUpgrade(ctx context.Context, tc *platformv1alpha1.TalosCluster) (done bool, result ctrl.Result, err error) {
	logger := log.FromContext(ctx)

	if !tc.Spec.VersionUpgrade {
		// Clear any stale VersionUpgradePending condition.
		existing := platformv1alpha1.FindCondition(tc.Status.Conditions, platformv1alpha1.ConditionTypeVersionUpgradePending)
		if existing != nil && existing.Status == metav1.ConditionTrue {
			platformv1alpha1.SetCondition(
				&tc.Status.Conditions,
				platformv1alpha1.ConditionTypeVersionUpgradePending,
				metav1.ConditionFalse,
				platformv1alpha1.ReasonVersionUpgradeComplete,
				"spec.versionUpgrade cleared.",
				tc.Generation,
			)
		}
		return false, ctrl.Result{}, nil
	}

	// spec.versionUpgrade=true: validate that talosVersion is set.
	if tc.Spec.TalosVersion == "" {
		platformv1alpha1.SetCondition(
			&tc.Status.Conditions,
			platformv1alpha1.ConditionTypePhaseFailed,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonTalosVersionRequired,
			"spec.versionUpgrade=true requires spec.talosVersion to be set to the target version.",
			tc.Generation,
		)
		return true, ctrl.Result{}, nil
	}

	// Anti-regression: if the specified version would downgrade, block.
	if checkVersionRegression(tc) {
		return true, ctrl.Result{}, nil
	}

	upName := tc.Name + versionUpgradeSuffix

	// Check if the UpgradePolicy already exists.
	existing := &platformv1alpha1.UpgradePolicy{}
	err = r.Client.Get(ctx, types.NamespacedName{Name: upName, Namespace: tc.Namespace}, existing)
	if err != nil && !apierrors.IsNotFound(err) {
		return true, ctrl.Result{}, fmt.Errorf("reconcileVersionUpgrade: get UpgradePolicy: %w", err)
	}

	if apierrors.IsNotFound(err) {
		// Create the UpgradePolicy.
		up := &platformv1alpha1.UpgradePolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upName,
				Namespace: tc.Namespace,
				Labels: map[string]string{
					labelVersionUpgradeOwned:    "true",
					"platform.ontai.dev/cluster": tc.Name,
				},
			},
			Spec: platformv1alpha1.UpgradePolicySpec{
				ClusterRef:         platformv1alpha1.LocalObjectRef{Name: tc.Name, Namespace: tc.Namespace},
				UpgradeType:        platformv1alpha1.UpgradeTypeTalos,
				TargetTalosVersion: tc.Spec.TalosVersion,
				RollingStrategy:    platformv1alpha1.RollingStrategySequential,
			},
		}
		if err := r.Client.Create(ctx, up); err != nil {
			return true, ctrl.Result{}, fmt.Errorf("reconcileVersionUpgrade: create UpgradePolicy: %w", err)
		}
		platformv1alpha1.SetCondition(
			&tc.Status.Conditions,
			platformv1alpha1.ConditionTypeVersionUpgradePending,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonVersionUpgradeSubmitted,
			fmt.Sprintf("UpgradePolicy %s created for Talos version upgrade to %s.", upName, tc.Spec.TalosVersion),
			tc.Generation,
		)
		r.Recorder.Eventf(tc, nil, "Normal", "VersionUpgradeSubmitted", "VersionUpgradeSubmitted",
			"Created UpgradePolicy %s to upgrade cluster %s to Talos %s",
			upName, tc.Name, tc.Spec.TalosVersion)
		logger.Info("created UpgradePolicy for spec.versionUpgrade",
			"cluster", tc.Name, "upgradePolicyName", upName, "targetVersion", tc.Spec.TalosVersion)
		return true, ctrl.Result{RequeueAfter: operationalJobPollInterval}, nil
	}

	// UpgradePolicy exists — check if it completed.
	readyCond := platformv1alpha1.FindCondition(existing.Status.Conditions, platformv1alpha1.ConditionTypeUpgradePolicyReady)
	if readyCond == nil || readyCond.Status != metav1.ConditionTrue {
		// Still in progress.
		platformv1alpha1.SetCondition(
			&tc.Status.Conditions,
			platformv1alpha1.ConditionTypeVersionUpgradePending,
			metav1.ConditionTrue,
			platformv1alpha1.ReasonVersionUpgradeSubmitted,
			fmt.Sprintf("UpgradePolicy %s is in progress.", upName),
			tc.Generation,
		)
		return true, ctrl.Result{RequeueAfter: operationalJobPollInterval}, nil
	}

	// UpgradePolicy is Ready=True — upgrade complete.
	// spec.versionUpgrade is left at true; the user clears it from git when ready.
	// The VersionUpgradePending=False condition is the authoritative completion signal.
	// Re-applying an old spec with versionUpgrade=true is idempotent: the next
	// reconcile will find the existing UpgradePolicy already in Ready=True and
	// return immediately without creating a duplicate.
	platformv1alpha1.SetCondition(
		&tc.Status.Conditions,
		platformv1alpha1.ConditionTypeVersionUpgradePending,
		metav1.ConditionFalse,
		platformv1alpha1.ReasonVersionUpgradeComplete,
		fmt.Sprintf("UpgradePolicy %s completed. Cluster upgraded to Talos %s.", upName, tc.Spec.TalosVersion),
		tc.Generation,
	)
	r.Recorder.Eventf(tc, nil, "Normal", "VersionUpgradeComplete", "VersionUpgradeComplete",
		"Cluster %s upgraded to Talos %s via UpgradePolicy %s",
		tc.Name, tc.Spec.TalosVersion, upName)
	logger.Info("version upgrade complete via UpgradePolicy",
		"cluster", tc.Name, "version", tc.Spec.TalosVersion)
	return true, ctrl.Result{}, nil
}
