// Package day2_integration_test contains envtest integration tests for the
// platform day-2 operational CRD reconcilers.
//
// These tests verify controller-runtime status patching, condition transitions,
// and RunnerConfig creation against a real API server and etcd — behaviour that
// the unit tests' fake client cannot replicate (no SSA merge semantics, no etcd
// visibility, no watch event propagation).
//
// envtest binaries required:
//
//	setup-envtest use --bin-dir /tmp/envtest-bins
//	export KUBEBUILDER_ASSETS=/tmp/envtest-bins/k8s/1.35.0-linux-amd64
//
// All tests skip automatically when KUBEBUILDER_ASSETS is absent.
package day2_integration_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	infrav1alpha1 "github.com/ontai-dev/platform/api/infrastructure/v1alpha1"
	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
	"github.com/ontai-dev/platform/internal/controller"
)

const envtestPollInterval = 50 * time.Millisecond
const envtestTimeout = 10 * time.Second

var (
	testEnv   *envtest.Environment
	testClient client.Client
	testScheme *runtime.Scheme
	testCtx    context.Context
	testCancel context.CancelFunc
)

func TestMain(m *testing.M) {
	assets := os.Getenv("KUBEBUILDER_ASSETS")
	if assets == "" {
		os.Exit(0) // skip all integration tests when envtest binaries absent
	}

	testScheme = runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(testScheme)
	_ = platformv1alpha1.AddToScheme(testScheme)
	_ = infrav1alpha1.AddToScheme(testScheme)
	_ = coordinationv1.AddToScheme(testScheme)
	_ = controller.AddOperationalRunnerConfigToScheme(testScheme)

	testEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "..", "config", "crd", "bases"),
		},
		BinaryAssetsDirectory: assets,
		Scheme:                testScheme,
	}

	cfg, err := testEnv.Start()
	if err != nil {
		panic(err)
	}

	testClient, err = client.New(cfg, client.Options{Scheme: testScheme})
	if err != nil {
		panic(err)
	}

	// Create required namespaces.
	testCtx, testCancel = context.WithCancel(context.Background())
	for _, ns := range []string{"seam-tenant-test", "seam-system", "ont-system"} {
		_ = testClient.Create(testCtx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: ns},
		})
	}

	code := m.Run()
	testCancel()
	_ = testEnv.Stop()
	os.Exit(code)
}
