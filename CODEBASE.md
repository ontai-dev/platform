# platform: Codebase Reference

## 1. Purpose

Platform is the cluster lifecycle authority for the ONT platform. It owns the complete creation, upgrade, and decommission lifecycle of Talos-based Kubernetes clusters via a custom CAPI infrastructure provider. It is the sole namespace creation authority for `seam-tenant-{clusterName}` (CP-INV-004). It submits Conductor execute-mode Jobs for all day-2 operations. Platform does NOT compile manifests (conductor/compiler), deliver packs (wrapper), or own RBAC governance (guardian).

Two code paths: `reconcileDirectBootstrap()` for the management cluster (mode=bootstrap, capi.enabled=false), and `reconcileCAPIPath()` for target clusters (mode=bootstrap or mode=import, capi.enabled=true).

Talos goclient is permitted ONLY in `SeamInfrastructureClusterReconciler` and `SeamInfrastructureMachineReconciler` (CP-INV-001). All other reconcilers are strictly prohibited.

---

## 2. Key Files and Locations

### API types (`api/v1alpha1/`)

| File | Type |
|------|------|
| `taloscluster_types.go` | `TalosCluster` -- platform's CR, owned by seam-core schema. `spec.mode` (bootstrap/import), `spec.capi.enabled`, `spec.role` (management/tenant). |
| `etcdmaintenance_types.go` | `EtcdMaintenance` day-2 CR |
| `nodemaintenance_types.go` | `NodeMaintenance` day-2 CR |
| `pkirotation_types.go` | `PKIRotation` day-2 CR |
| `clusterreset_types.go` | `ClusterReset` day-2 CR (requires `ontai.dev/reset-approved=true` annotation, CP-INV-006) |
| `upgradepolicy_types.go` | `UpgradePolicy` -- dual-path: CAPI (modifies TalosControlPlane) or direct Job |
| `nodeoperation_types.go` | `NodeOperation` -- dual-path |
| `clustermaintenance_types.go` | `ClusterMaintenance` day-2 CR |
| `hardeningprofile_types.go` | `HardeningProfile` day-2 CR |
| `maintenancebundle_types.go` | `MaintenanceBundle` day-2 CR |

### CAPI provider types (`api/infrastructure/v1alpha1/`)

| File | Type |
|------|------|
| `seaminfrastructurecluster_types.go` | `SeamInfrastructureCluster` -- platform's CAPI InfrastructureCluster implementation |
| `seaminfrastructuremachine_types.go` | `SeamInfrastructureMachine` -- holds `spec.address` (pre-provisioned node IP); `status.ready=true` after machineconfig applied |

### Controllers (`internal/controller/`)

#### `taloscluster_controller.go`

`TalosClusterReconciler` at L46. `Reconcile()` L83 with deferred status patch at L110.

`machineApplyAttemptsHaltThreshold = 3` (L24) -- number of consecutive port-50000 ApplyConfiguration failures before TalosClusterReconciler raises `ControlPlaneUnreachable` condition.

`reconcileDirectBootstrap()` L209 -- management cluster path. Submits bootstrap Job for new clusters, handles `mode=import` kubeconfig import, calls `ensureManagementOnboarding()` when complete.

`reconcileCAPIPath()` L449 -- target cluster path. Creates SeamInfrastructureCluster, CAPI Cluster, TalosControlPlane (CACPPT), TalosConfigTemplate (CABPT), MachineDeployments, SeamInfrastructureMachineTemplates.

`checkMachineReachability()` L662 -- lists SeamInfrastructureMachine nodes in `seam-tenant-{cluster}`, checks for port-50000 ApplyConfiguration failures. Halts control-plane reconcile after `machineApplyAttemptsHaltThreshold` consecutive failures (L696).

`ensureConductorReadyAndTransition()` L590 -- waits for RunnerConfig capabilities non-empty + remote conductor bootstrap complete.

`EnsureRemoteConductorBootstrap()` L732 -- creates conductor Deployment + RBAC on target cluster via direct kubeconfig. Calls `ensureRemoteNamespace()` L831, `ensureRemoteConductorServiceAccount()` L842, `EnsureRemoteConductorRBAC()` L860.

`EnsureRemoteTalosClusterCopy()` L965 -- copies InfrastructureTalosCluster CR to `ont-system` on target cluster via dynamic client. Non-fatal on NotFound (seam-core enable bundle may not yet be applied).

#### `taloscluster_helpers.go`

`handleTalosClusterDeletion()` L1073 -- **current implementation**:
- Step 1 (L1078): Delete RunnerConfig in `bootstrapRunnerConfigNamespace` + kubeconfig/talosconfig Secrets in `seam-tenant-{cluster}`. Gated by `finalizerRunnerConfigCleanup`.
- Step 2 (L1121): Delete tenant namespace `seam-tenant-{cluster}`. Gated by `finalizerTenantNamespaceCleanup`. Comment: `PLATFORM-BL-TENANT-GC`.

**What is NOT covered** (Decision H order violation -- T-24 open): PackInstance deletion, PackExecution deletion, RBACProfile deletion, PermissionSet deletion, RBACPolicy deletion, PermissionSnapshot deletion. Decision H requires wrapper components first, guardian components second, TalosCluster CR last. Current implementation skips all of these.

`ensureTenantOnboarding()` L1254 -- called on new tenant cluster registration:
1. L1256: Append cluster to `seam-platform-rbac-policy` spec.allowedClusters via `appendToUnstructuredStringSlice()`.
2. L1263: Append cluster to targetClusters for profiles: `rbac-wrapper`, `rbac-conductor`, `rbac-platform`, `rbac-seam-core`.
3. L1274: Create LocalQueue `pack-deploy-queue` in tenant namespace for Kueue.
4. L1279: Call `ensureExecutorTalosconfig()` -- copies talosconfig Secret to `ont-system` and `seam-tenant-{cluster}`.
5. L1283: Call `ensureTenantExecutorResources()` -- creates executor SA/Role/RoleBinding for day-2 Jobs.
6. L1292: Call `ensureWrapperRunnerResources()` L1469 -- creates wrapper-runner SA/Role/RoleBinding/ClusterRoleBinding for pack-deploy Jobs.

`ensureManagementOnboarding()` L1303 -- called for management cluster: appends "management" to rbac-policy allowedClusters, copies talosconfig, creates executor resources.

`appendToUnstructuredStringSlice()` L1151 -- reads object via GVK/namespace/name, appends value to string slice field at fieldPath via MergePatch. Returns nil on NotFound (non-fatal for test environments).

`ensureWrapperRunnerResources()` L1469 -- creates `wrapper-runner-{cluster}` SA + `wrapper-runner` Role + `wrapper-runner-{cluster}` RoleBinding + `wrapper-runner-{cluster}` ClusterRoleBinding. **Not deleted on TalosCluster deletion** (PLATFORM-BL-WRAPPER-RUNNER-RBAC-LIFECYCLE open).

#### `seaminfrastructuremachine_reconciler.go`

`SeamInfrastructureMachineReconciler` -- the ONLY reconciler permitted talos goclient access outside `SeamInfrastructureClusterReconciler` (CP-INV-001). Delivers machineconfig to Talos node on port 50000 via `ApplyConfiguration`. Sets `status.ready=true` after node exits maintenance mode.

`port50000RetryBase = 10 * time.Second` (L36). `port50000RetryCap` (L39) -- max retry interval.

#### `operational_job_base.go`

`operationalJobBackoffLimit = int32(0)` (L45) -- no retries on gate failures (INV-018). Applied at `jobSpec()` L61 via `BackoffLimit: &backoff` L76.

`jobSpec()` L61 -- builds conductor execute-mode Job manifest for a named capability in a namespace.

`jobSpecWithExclusions()` L234 -- same as `jobSpec()` but adds node affinity exclusions.

`getClusterRunnerConfig()` L337 -- reads RunnerConfig for `clusterName` from `ont-system`.

`hasCapability()` L344 -- checks if RunnerConfig `status.capabilities` contains named capability.

`readOperationRecord()` L156 -- reads PackOperationResult (TCOR) for Job completion status.

`ensureTCOR()` L180 -- creates TalosClusterOperationResult for a cluster.

`bumpTCORRevision()` L209 -- increments TCOR revision on version upgrade.

`resolveOperatorLeaderNode()` L265 -- identifies the node hosting the current operator leader Pod (for node exclusion in day-2 ops).

`buildNodeExclusions()` L307 -- builds list of node names to exclude from Job scheduling.

#### `s3_env_secret.go`

Cross-namespace S3 credential projection for executor Jobs. Source secret lives in `seam-system`; executor Job runs in `seam-tenant-{cluster}`. Direct `envFrom` across namespaces is not possible in Kubernetes, so this file manages a projected copy.

`ensureS3EnvSecret(ctx, c, scheme, sourceName, sourceNS string, em) (string, error)` -- reads source secret, normalizes keys via `NormalizeS3SecretData`, creates/updates `{em.Name}-s3-env` Secret in `em.Namespace` with an ownerReference to `em`. Returns the projected secret name.

`NormalizeS3SecretData(data map[string][]byte) (map[string][]byte, error)` (exported) -- accepts both provider key conventions and outputs canonical AWS SDK env var names. See platform-schema.md §10 for the full key contract.

`appendS3EnvFrom(job *batchv1.Job, envSecretName string)` -- appends an `envFrom` entry for `envSecretName` to the first container of the Job's pod template. No-op when `envSecretName` is empty (non-backup operations).

`resolveS3CredentialsForRestore(ctx, c, em) (string, string, bool, error)` -- resolves S3 credentials for restore: first checks `spec.s3SnapshotPath.credentialsSecretRef`, then falls back to `seam-etcd-backup-config` in `seam-system`.

---

## 3. Primary Data Flows

**Management cluster bootstrap**: `reconcileDirectBootstrap()` L209 reads RunnerConfig, submits bootstrap Conductor Job if capabilities empty, polls Job completion via `readOperationRecord()` L156, calls `ensureConductorReadyAndTransition()` L590 when bootstrap complete.

**CAPI cluster creation**: `reconcileCAPIPath()` L449 creates SeamInfrastructureCluster + CAPI Cluster + TalosControlPlane + TalosConfigTemplate + MachineDeployments. CABPT renders machineconfigs into bootstrap Secrets per Machine. `SeamInfrastructureMachineReconciler` picks up each Secret, delivers machineconfig to node port 50000, sets `status.ready=true`.

**Mode=import path**: `reconcileDirectBootstrap()` for import path imports existing kubeconfig (no machineconfig delivery). Cluster is governed but not bootstrapped by platform.

**Day-2 op path (direct)**: Human creates day-2 CR (e.g., EtcdMaintenance) --> reconciler calls `getClusterRunnerConfig()` + `hasCapability()` --> for backup/restore operations: `resolveEtcdBackupS3Secret()` or `resolveS3CredentialsForRestore()` resolves the source Secret, then `ensureS3EnvSecret()` projects a normalized copy into `em.Namespace` -- if no S3 secret is found, `EtcdBackupDestinationAbsent` condition is set and reconcile stops --> builds Job spec via `jobSpec()` or `jobSpecWithExclusions()` --> `appendS3EnvFrom()` mounts the projected secret via `envFrom` --> submits Conductor executor Job --> polls `readOperationRecord()` --> updates CR status.

---

## 4. Invariants

| ID | Rule | Location |
|----|------|----------|
| CP-INV-001 | Talos goclient restricted to SeamInfrastructureClusterReconciler and SeamInfrastructureMachineReconciler | `seaminfrastructuremachine_reconciler.go`, `seaminfrastructurecluster_reconciler.go` |
| CP-INV-004 | Platform is sole namespace creation authority for `seam-tenant-{cluster}` | `taloscluster_helpers.go:278` `ensureTenantNamespace()` |
| CP-INV-006 | TalosClusterReset requires `ontai.dev/reset-approved=true` before reconciliation | `clusterreset_reconciler.go` |
| CP-INV-007 | Leader election required; lease: `platform-leader` in `seam-system` | `cmd/main.go` |
| CP-INV-008 | TalosCluster owns all CAPI objects via ownerReference | `taloscluster_controller.go:449` |
| CP-INV-009 | Every TalosConfigTemplate includes `cluster.network.cni.name: none` and BPF kernel params | `taloscluster_helpers.go:420` `ensureTalosConfigTemplate()` |
| CP-INV-010 | Kueue not used in platform; operational runner Jobs submit directly | `operational_job_base.go:61` |
| CP-INV-013 | CiliumPending on TalosCluster is not a degraded state | `isCiliumPackInstanceReady()` L683 |
| INV-006 | No Jobs on delete path -- deletion triggers events only | `handleTalosClusterDeletion()` L1073 |

---

## 5. Open Items

**T-24 (design session required)**: `handleTalosClusterDeletion()` L1073 only covers RunnerConfig + Secrets + namespace deletion. Decision H order not implemented: wrapper components (PackInstance, PackExecution) must be deleted first, then guardian components (RBACProfile, PermissionSet, RBACPolicy, PermissionSnapshot), then TalosCluster CR last. Mode=import vs mode=bootstrap distinction (divorce vs decommission) also absent.

**PLATFORM-BL-WRAPPER-RUNNER-RBAC-LIFECYCLE**: `ensureWrapperRunnerResources()` L1469 creates `ClusterRoleBinding wrapper-runner-{cluster}` but `handleTalosClusterDeletion()` L1073 does not delete it. Required: delete `ClusterRoleBinding wrapper-runner-{cluster}` on TalosCluster deletion.

---

## 6. Test Contract

| Package | Coverage |
|---------|----------|
| `test/unit/controller` | TalosClusterReconciler (bootstrap, CAPI, import paths), handleTalosClusterDeletion, ensureTenantOnboarding, operational job base (jobSpec, hasCapability) |
| `test/unit/controller` (s3) | `NormalizeS3SecretData`: required-key validation, camelCase input, AWS SDK env var input, mixed keys, optional endpoint omission |
| `test/integration/day2` | EtcdMaintenance reconciler (backup with S3, S3-absent condition, etcd defrag, restore path); verifies SSA status patch, Job creation with capability label, S3 projected secret creation via `ensureS3EnvSecret` |
| `test/e2e` | Stub files; all skip when `MGMT_KUBECONFIG` absent; skip reasons reference backlog item IDs |
