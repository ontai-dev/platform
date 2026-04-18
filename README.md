# platform

**Seam Platform operator**
**API Group:** `platform.ontai.dev` (ONT-native), `infrastructure.cluster.x-k8s.io` (CAPI)
**Image:** `registry.ontai.dev/ontai-dev/platform:<semver>`

---

## What this repository is

`platform` is the CAPI management plane operator and the ONT-native Infrastructure
Provider for Talos. It owns the complete lifecycle of Talos clusters and all tenant
coordination.

---

## CRDs

### ONT-native (`platform.ontai.dev`)

| Kind | Role |
|---|---|
| `TalosCluster` | Root declaration for a Talos target cluster (CAPI composition root) |
| `TalosClusterReset` | Affirmative CR for cluster destruction with human approval gate |
| `TalosBackup` | Operational runner Job for etcd snapshot backup |
| `TalosEtcdMaintenance` | Operational runner Job for etcd defragmentation and compaction |
| `TalosPKIRotation` | Operational runner Job for PKI certificate rotation |
| `TalosRecovery` | Operational runner Job for cluster recovery from etcd snapshot |
| `TalosHardeningApply` | Operational runner Job for CIS benchmark hardening |
| `TalosNodePatch` | Operational runner Job for targeted node configuration patch |
| `TalosNodeOperation` | Operational runner Job for node cordon, drain, and reboot sequences |
| `TalosCredentialRotation` | Operational runner Job for credential rotation |
| `ClusterMaintenance` | Operational runner Job for scheduled maintenance windows |
| `UpgradePolicy` | Declared upgrade policy for a cluster or node pool |
| `HardeningProfile` | Declared hardening target profile |
| `MaintenanceBundle` | Aggregate maintenance intent record |

### CAPI Infrastructure Provider (`infrastructure.cluster.x-k8s.io`)

| Kind | Role |
|---|---|
| `SeamInfrastructureCluster` | CAPI InfrastructureCluster implementation for Talos |
| `SeamInfrastructureMachine` | CAPI InfrastructureMachine implementation for Talos nodes |

---

## Architecture

Platform operates in three modes.

**CAPI composition (target cluster lifecycle):**
`TalosCluster` is the root object. The platform reconciler creates and owns CAPI
objects (`Cluster`, `TalosControlPlane`, `MachineDeployment`, `SeamInfrastructureCluster`,
`SeamInfrastructureMachine`) as children of `TalosCluster`. The Seam Infrastructure
Provider reconcilers deliver machineconfigs to pre-provisioned nodes on port 50000
via the talos goclient.

**Direct bootstrap Job (management cluster):**
The ONT bootstrap path via conductor Jobs is used for management cluster bootstrap.
CAPI is not involved in management cluster provisioning.

**Operational runner Jobs (Talos operational CRDs):**
Seven CRDs (`TalosBackup`, `TalosEtcdMaintenance`, `TalosPKIRotation`, `TalosRecovery`,
`TalosHardeningApply`, `TalosNodePatch`, `TalosCredentialRotation`) submit conductor
executor Jobs directly. Kueue is not used for any platform operation.

**Tenant coordination:**
Platform creates `seam-tenant-{cluster-name}` namespaces. It is the sole namespace
creation authority. Tenant coordination CRDs (`UpgradePolicy`, `HardeningProfile`,
`MaintenanceBundle`) are pure record-keeping reconcilers with no runner Jobs.

---

## Key invariants

- The talos goclient is restricted exclusively to `SeamInfrastructureClusterReconciler`
  and `SeamInfrastructureMachineReconciler`. All other reconcilers have zero talos
  goclient access.
- `TalosCluster` deletion never triggers cluster destruction. `TalosClusterReset`
  is the only destruction path, and requires `ontai.dev/reset-approved=true`.
- Kueue is not used for any operation in platform.
- Platform installs after guardian reaches `provisioned=true` on its `RBACProfile`.

---

## Building

```sh
go build ./cmd/platform
```

The binary is built into a distroless container image:

```sh
docker build -t registry.ontai.dev/ontai-dev/platform:<semver> .
```

---

## Testing

```sh
go test ./test/unit/...
```

---

## Schema and design reference

- `docs/platform-schema.md` - API contract, field definitions, status conditions
- `platform-design.md` - Implementation architecture and reconciler design

---

*platform - Seam Platform Operator*
*Apache License, Version 2.0*
