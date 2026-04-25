package controller

// tcor_graphquery_stub.go provides the stub for archiving an
// InfrastructureTalosClusterOperationResult revision to the GraphQuery DB
// when a talosVersion upgrade obsoletes it. The real implementation is
// deferred until the GraphQuery DB service is available.
//
// Invariant: the stub must be called for every completed TCOR revision before
// it is cleared. Cleared = Operations wiped, Revision incremented, TalosVersion
// advanced. The archived data is the infrastructure memory for that version epoch.

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/log"

	seamcorev1alpha1 "github.com/ontai-dev/seam-core/api/v1alpha1"
)

// stubDumpTCORRevisionToGraphQueryDB archives the completed revision of a cluster
// TCOR before its Operations list is cleared on talosVersion upgrade.
// When the GraphQuery DB service is implemented, this stub will be replaced by
// a real gRPC or HTTP write to the persistence layer.
func stubDumpTCORRevisionToGraphQueryDB(ctx context.Context, clusterRef string, revision int64, talosVersion string, ops map[string]seamcorev1alpha1.TalosClusterOperationRecord) {
	logger := log.FromContext(ctx)
	logger.V(1).Info("stub: would archive TCOR revision to GraphQuery DB",
		"cluster", clusterRef,
		"revision", revision,
		"talosVersion", talosVersion,
		"operationCount", len(ops),
	)
}
