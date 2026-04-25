package controller

// tcor_graphquery_stub.go provides the stub for archiving an
// InfrastructureTalosClusterOperationResult to the GraphQuery DB before it is
// deleted or superseded. The real implementation is deferred until the
// GraphQuery DB service is available. CONDUCTOR-BL-GRAPHQUERY-ARCHIVE.
//
// Invariant: the stub must be called for every completed TCOR result at the
// time the platform reconciler reads a terminal status (Succeeded or Failed).
// This ensures the N-1 result is archived before the TCOR TTL expires and the
// Kubernetes GC collects it.

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/log"

	seamcorev1alpha1 "github.com/ontai-dev/seam-core/api/v1alpha1"
)

// stubDumpTCORToGraphQueryDB archives the terminal TCOR result before it is
// eventually deleted by the Kubernetes TTL controller or ownerReference GC.
// When the GraphQuery DB service is implemented, this stub will be replaced by
// a real gRPC or HTTP write to the persistence layer.
// CONDUCTOR-BL-GRAPHQUERY-ARCHIVE: stub phase -- no-op until service is ready.
func stubDumpTCORToGraphQueryDB(ctx context.Context, tcor *seamcorev1alpha1.InfrastructureTalosClusterOperationResult) {
	logger := log.FromContext(ctx)
	logger.V(1).Info("stub: would archive TCOR to GraphQuery DB",
		"name", tcor.Name,
		"namespace", tcor.Namespace,
		"capability", tcor.Spec.Capability,
		"clusterRef", tcor.Spec.ClusterRef,
		"status", tcor.Spec.Status,
		"jobRef", tcor.Spec.JobRef,
	)
}
