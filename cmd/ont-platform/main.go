// Binary ont-platform is the controller-runtime manager entry point for the
// platform operator.
//
// It registers the TalosClusterReconciler and starts the manager with leader
// election. platform-design.md §2.1, CP-INV-007.
package main

import (
	"flag"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	infrav1alpha1 "github.com/ontai-dev/platform/api/infrastructure/v1alpha1"
	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
	"github.com/ontai-dev/platform/internal/controller"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(platformv1alpha1.AddToScheme(scheme))
	utilruntime.Must(infrav1alpha1.AddToScheme(scheme))
	// Register the local OperationalRunnerConfig type so the manager client can
	// create and read RunnerConfig CRs emitted by day-2 reconcilers.
	// conductor-schema.md §17.
	utilruntime.Must(controller.AddOperationalRunnerConfigToScheme(scheme))
}

func main() {
	var (
		metricsAddr          string
		healthProbeAddr      string
		enableLeaderElection bool
	)

	// METRICS_ADDR overrides the metrics bind address. Defaults to :8080.
	// ServiceMonitor CRDs for Prometheus Operator scrape configuration are
	// deferred to a post-e2e observability session.
	metricsDefault := ":8080"
	if v := os.Getenv("METRICS_ADDR"); v != "" {
		metricsDefault = v
	}
	flag.StringVar(&metricsAddr, "metrics-bind-address", metricsDefault,
		"The address the metrics endpoint binds to. Overridden by METRICS_ADDR env var.")
	flag.StringVar(&healthProbeAddr, "health-probe-bind-address", ":8081",
		"The address the health and readiness probes bind to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", true,
		"Enable leader election for controller manager. "+
			"Ensures only one instance is active at a time.")

	opts := zap.Options{}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	setupLog := ctrl.Log.WithName("setup")

	// CONDUCTOR_IMAGE is a required configuration value — the fully qualified image
	// reference used when creating RunnerConfig CRs for management cluster bootstrap
	// and import. Fail fast if absent so a misconfigured Deployment fails immediately
	// at startup rather than silently creating RunnerConfigs with empty image refs.
	// platform-schema.md §3, conductor-schema.md §17.
	conductorImage := os.Getenv("CONDUCTOR_IMAGE")
	if conductorImage == "" {
		setupLog.Error(nil, "CONDUCTOR_IMAGE env var is required but not set — cannot create RunnerConfig CRs without a valid conductor image reference")
		os.Exit(1)
	}

	// CP-INV-007: leader election required. Lease name: platform-leader.
	// Lease namespace: seam-system (canonical operator namespace).
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                  scheme,
		Metrics:                 metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress:  healthProbeAddr,
		LeaderElection:          enableLeaderElection,
		LeaderElectionID:        "platform-leader",
		LeaderElectionNamespace: "seam-system",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err := (&controller.TalosClusterReconciler{
		Client:         mgr.GetClient(),
		Scheme:         mgr.GetScheme(),
		Recorder:       mgr.GetEventRecorderFor("taloscluster-controller"),
		ConductorImage: conductorImage,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "TalosCluster")
		os.Exit(1)
	}

	// SeamInfrastructureClusterReconciler implements the CAPI InfrastructureCluster
	// contract. CP-INV-001: no talos goclient in this reconciler.
	if err := (&controller.SeamInfrastructureClusterReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("seaminfrastructurecluster-controller"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "SeamInfrastructureCluster")
		os.Exit(1)
	}

	// SeamInfrastructureMachineReconciler implements the CAPI InfrastructureMachine
	// contract. CP-INV-001: this is one of exactly two files permitted to use
	// talos goclient — TalosMachineConfigApplier is the production implementation.
	if err := (&controller.SeamInfrastructureMachineReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("seaminfrastructuremachine-controller"),
		Applier:  &controller.TalosMachineConfigApplier{},
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "SeamInfrastructureMachine")
		os.Exit(1)
	}

	// Day-2 operational reconcilers — F-P1.

	if err := (&controller.EtcdMaintenanceReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("etcdmaintenance-controller"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "EtcdMaintenance")
		os.Exit(1)
	}

	if err := (&controller.NodeMaintenanceReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("nodemaintenance-controller"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "NodeMaintenance")
		os.Exit(1)
	}

	if err := (&controller.PKIRotationReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("pkirotation-controller"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "PKIRotation")
		os.Exit(1)
	}

	// ClusterResetReconciler enforces INV-007/CP-INV-006: holds at PendingApproval
	// until ontai.dev/reset-approved=true annotation is present.
	if err := (&controller.ClusterResetReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("clusterreset-controller"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "ClusterReset")
		os.Exit(1)
	}

	if err := (&controller.HardeningProfileReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "HardeningProfile")
		os.Exit(1)
	}

	if err := (&controller.UpgradePolicyReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("upgradepolicy-controller"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "UpgradePolicy")
		os.Exit(1)
	}

	if err := (&controller.NodeOperationReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("nodeoperation-controller"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "NodeOperation")
		os.Exit(1)
	}

	if err := (&controller.ClusterMaintenanceReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("clustermaintenance-controller"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "ClusterMaintenance")
		os.Exit(1)
	}

	if err := (&controller.MaintenanceBundleReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("maintenancebundle-controller"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "MaintenanceBundle")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting platform manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
