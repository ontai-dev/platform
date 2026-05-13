# platform-schema
> API Group: seam.ontai.dev/v1alpha1 (TalosCluster, ClusterLog -- cross-operator CRDs)
> API Group: platform.ontai.dev/v1alpha1 (all day-2 operational CRDs)
> API Group: infrastructure.cluster.x-k8s.io (CAPI types -- frozen, out of scope)
> Operator: platform
> Schema authority: this file (primary). ~/ontai/seam/docs/seam-schema.md (RunnerConfig). ~/ontai/conductor/docs/conductor-schema.md (capabilities). ~/ontai/guardian/docs/guardian-schema.md (RBACProfile gate). ~/ontai/dispatcher/docs/dispatcher-schema.md (PackInstalled gate for Cilium).

---

## 1. Domain Boundary

Platform owns the complete lifecycle of Talos clusters and all day-2 operational coordination. It does this by composing CAPI primitives for target cluster lifecycle while preserving Seam governing principles: declarative, versioned, auditable, and security-first.

Platform is the CAPI management plane operator. It creates and owns CAPI objects (SeamInfrastructureCluster, cluster.x-k8s.io/Cluster, TalosControlPlane, MachineDeployment, TalosConfigTemplate, SeamInfrastructureMachineTemplate) as children of TalosCluster for target clusters. CAPI controllers reconcile those objects to actual cluster state through the Seam Infrastructure Provider and CABPT.

What does not change from the pre-CAPI model:

- Management cluster bootstrap remains Seam-native. CAPI cannot bootstrap the cluster it runs on.
- TalosCluster is still the Seam root CR for every cluster. CAPI objects are children of TalosCluster, not the other way around.
- Operational CRDs with no CAPI equivalent remain and use Conductor capabilities via direct controller reconciliation.
- Platform creates tenant namespaces. CP-INV-004 applies without exception.
- Guardian deploys before platform. Platform starts only after Guardian RBACProfile reaches provisioned=true (CP-INV-012).

---

## 2. Master GVK Reference

### seam.ontai.dev/v1alpha1

These types are defined in platform/api/seam/v1alpha1/ and are schema-shared across the platform and seam modules. Platform reconciles them; seam is the canonical source of the type definitions for cross-operator consumption.

| Kind | Short | Scope | Namespace |
|------|-------|-------|-----------|
| TalosCluster | tc | Namespaced | seam-system (management), seam-tenant-{cluster-name} (target) |
| ClusterLog | clog | Namespaced | seam-tenant-{cluster-name} |

### platform.ontai.dev/v1alpha1

All day-2 operational CRDs are owned exclusively by platform.

| Kind | Short | Scope | Conductor capabilities |
|------|-------|-------|------------------------|
| EtcdMaintenance | em | Namespaced | etcd-backup, etcd-restore, etcd-defrag |
| TalosEtcdBackupSchedule | etcdbs | Namespaced | (schedule controller; creates EtcdMaintenance CRs) |
| NodeMaintenance | nm | Namespaced | node-patch, hardening-apply, credential-rotate |
| NodeOperation | nop | Namespaced | node-scale-up, node-decommission, node-reboot (non-CAPI path only) |
| PKIRotation | pkir | Namespaced | pki-rotate |
| ClusterReset | crst | Namespaced | cluster-reset |
| ClusterMaintenance | cmaint | Namespaced | (no Job; CAPI pause or Conductor gate) |
| UpgradePolicy | upgp | Namespaced | talos-upgrade, kube-upgrade, stack-upgrade (non-CAPI path only) |
| HardeningProfile | hp | Namespaced | (configuration CR; no Job submission) |
| MaintenanceBundle | mb | Namespaced | drain, upgrade, etcd-backup, machineconfig-rotation |
| TalosMachineConfigBackup | mcb | Namespaced | machineconfig-backup |
| TalosMachineConfigBackupSchedule | mcbs | Namespaced | (schedule controller; creates TalosMachineConfigBackup CRs) |
| TalosMachineConfigRestore | mcr | Namespaced | machineconfig-restore |

### infrastructure.cluster.x-k8s.io (CAPI -- frozen)

| Kind | Short | Purpose |
|------|-------|---------|
| SeamInfrastructureCluster | sic | Cluster-level CAPI InfrastructureCluster implementation |
| SeamInfrastructureMachine | sim | Per-node CAPI InfrastructureMachine implementation |

CAPI types are frozen. Platform implements the CAPI contracts for these types through the Seam Infrastructure Provider but does not modify their schemas.

---

## 3. TalosCluster (seam.ontai.dev/v1alpha1)

Scope: Namespaced -- seam-system (management cluster) or seam-tenant-{cluster-name} (target clusters)
Short name: tc
Print columns: Mode, Role, Ready, Age

The Seam root CR for every cluster. For target clusters, TalosCluster owns all CAPI objects as children via ownerReference (CP-INV-008). For the management cluster, TalosCluster has no CAPI children.

Deletion of a TalosCluster CR never triggers physical cluster destruction (INV-015). ClusterReset is the only destruction path.

### spec fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| mode | string (bootstrap, import) | yes | bootstrap: cluster is formed from scratch. import: existing cluster brought under Seam governance. |
| role | string (management, tenant) | required when mode=import | Declares cluster role in Seam topology. |
| talosVersion | string | no | Talos OS version for this cluster. Must match RunnerConfig.agentImage tag (INV-012). |
| kubernetesVersion | string | no | Kubernetes version for this cluster. When versionUpgrade=true, drives an UpgradeTypeKubernetes policy. |
| versionUpgrade | bool | no | When true, triggers a cluster-level rolling upgrade. Upgrade type derived from which version fields are set: talosVersion only = UpgradeTypeTalos; kubernetesVersion only = UpgradeTypeKubernetes; both = UpgradeTypeStack. |
| clusterEndpoint | string | no | Cluster VIP or primary API endpoint IP. |
| nodeAddresses | []string | no | Node IPs for DNS A-record population. |
| capi | CAPIConfig | no | CAPI integration settings. When absent, direct bootstrap path is used. |
| infrastructureProvider | string (native, capi, screen) | no | Default: native. screen is reserved (INV-021). |
| kubeconfigSecretRef | string | no | Name of the Secret containing the kubeconfig. Required on mode=import. Not used when CAPI manages lifecycle. |
| talosconfigSecretRef | string | no | Name of the Secret containing the talosconfig. |
| lineage | SealedCausalChain | no | Sealed causal chain record. Immutable after creation (Decision 1). |
| pkiRotationThresholdDays | int32 | no | Days before cert expiry at which a PKIRotation CR is auto-created. Default 30, minimum 1. |
| hardeningProfileRef | LocalObjectRef | no | HardeningProfile CR to apply at bootstrap. |

### spec.capi fields (CAPIConfig)

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| enabled | bool | yes (within capi block) | True for all target clusters. False for management cluster. |
| talosVersion | string | no | Talos version for TalosConfigTemplate generation. |
| kubernetesVersion | string | no | Kubernetes version for TalosControlPlane. |
| controlPlane | CAPIControlPlaneConfig | no | Control plane configuration. Required when enabled=true. |
| controlPlane.replicas | int32 | no | Desired number of control plane nodes. |
| workers | []CAPIWorkerPool | no | Worker node pools. |
| workers[].name | string | yes | Pool identifier. Used as MachineDeployment name suffix. |
| workers[].replicas | int32 | no | Desired number of worker nodes in this pool. |
| workers[].seamInfrastructureMachineNames | []string | no | SeamInfrastructureMachine CR names pre-provisioned for this pool. |
| ciliumPackRef | CAPICiliumPackRef | no | PackDelivery name and version for the Cilium pack. Platform triggers a PackExecution for this pack when the CAPI Cluster reaches Running state. |

### status fields

| Field | Type | Description |
|-------|------|-------------|
| observedGeneration | int64 | Generation most recently reconciled. |
| origin | string (bootstrapped, imported) | How this cluster came under Seam governance. |
| observedTalosVersion | string | Talos version last confirmed running. |
| capiClusterRef | LocalObjectRef | Reference to the owned CAPI Cluster object. Only set for capi.enabled=true. |
| conditions | []metav1.Condition | Status conditions. |
| pkiExpiryDate | *metav1.Time | Earliest certificate expiry across talosconfig and kubeconfig Secrets. |

### Status condition types

| Condition | Meaning |
|-----------|---------|
| Ready | Cluster is fully operational. |
| Bootstrapping | Bootstrap Job submitted and running. |
| Bootstrapped | Bootstrap sequence complete. |
| Importing | Import sequence in progress. |
| Degraded | Cluster has entered a degraded state. |
| CiliumPending | CAPI cluster Running but Cilium PackInstance not yet Ready. Not a degraded state (CP-INV-013). |
| ControlPlaneUnreachable | Control plane API is not responding. |
| PartialWorkerAvailability | One or more worker nodes are not Ready. |
| ConductorReady | Conductor agent Deployment is running on the tenant cluster. |
| VersionUpgradePending | versionUpgrade=true and upgrade is queued. |
| VersionRegressionBlocked | A version downgrade was attempted and blocked. |
| HardeningApplied | HardeningProfile has been applied at bootstrap. |

---

## 4. ClusterLog (seam.ontai.dev/v1alpha1)

Scope: Namespaced -- seam-tenant-{clusterRef}
Short name: clog
Print columns: Cluster, TalosVersion, Revision, Ops, Age

Accumulates the day-2 operation history for one cluster, scoped to the current talosVersion revision. One CR per cluster. Created by platform when the cluster tenant namespace is provisioned. Named by the cluster name.

When the cluster talosVersion is upgraded, the current revision is archived to the GraphQuery DB and a new revision begins: Revision increments, TalosVersion is updated, and Operations is cleared.

Operations are appended by the Conductor execute-mode Job. The platform reconciler uses the JobRef field to correlate each record with the Job it submitted.

### spec fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| clusterRef | string | yes | Name of the TalosCluster this log accumulates. |
| talosVersion | string | yes | Talos version for the current active revision. Matches TalosCluster.spec.talosVersion at revision start. |
| revision | int64 | yes | Monotonic revision counter. Starts at 1. Increments on each talosVersion upgrade. |
| operations | map[string]OperationRecord | no | Day-2 operation records for the current revision, keyed by Kubernetes Job name. |
| operationCount | int64 | no | Count of records in operations. Maintained alongside operations for kubectl display. |

### OperationRecord fields

| Field | Type | Description |
|-------|------|-------------|
| capability | string | Conductor capability that produced this record. |
| jobRef | string | Kubernetes Job name that produced this record. |
| status | string (Succeeded, Failed) | Terminal status of the capability execution. |
| message | string | Human-readable summary of the outcome. |
| startedAt | *metav1.Time | Time the capability execution began. |
| completedAt | *metav1.Time | Time the capability execution finished. |
| failureReason | *OperationFailureReason | Populated when status is Failed. |

### OperationFailureReason fields

| Field | Values | Description |
|-------|--------|-------------|
| category | ValidationFailure, CapabilityUnavailable, ExecutionFailure, ExternalDependencyFailure, InvariantViolation | Failure domain classification. |
| reason | string | Human-readable failure description. |

---

## 5. Operational CRD Catalog (platform.ontai.dev/v1alpha1)

All operational CRDs live in seam-tenant-{cluster-name} namespaces. All Conductor capabilities referenced here must be verified against conductor-schema.md before any implementation work begins.

### EtcdMaintenance (shortName: em)

Covers all etcd lifecycle operations. CAPI has no etcd concept. Always submits a direct Conductor executor Job regardless of the owning TalosCluster's capi.enabled.

Named Conductor capabilities: etcd-backup, etcd-restore, etcd-defrag.

Key spec fields:

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| clusterRef | LocalObjectRef | yes | TalosCluster this operation targets. |
| operation | string (backup, restore, defrag) | yes | Etcd lifecycle operation to perform. |
| etcdBackupS3SecretRef | *corev1.SecretReference | no | S3 credentials Secret. Takes precedence over cluster-wide seam-etcd-backup-config. Required for backup when no cluster default is configured. See S3 resolution hierarchy in section 8. |
| s3Destination | *S3Ref | no | S3 location to write the snapshot to. Required when operation=backup. |
| s3SnapshotPath | *S3Ref | no | S3 location of snapshot to restore from. Required when operation=restore. |
| targetNodes | []string | no | Nodes to target for restore. All etcd members when empty. |
| pvcFallbackEnabled | bool | no | Instructs reconciler to proceed with PVC-backed backup when no S3 destination is configured (degraded mode). See section 8. |
| schedule | string | no | Cron expression for recurring backup operations. |

Status condition types: Ready, Running, Degraded.

---

### TalosEtcdBackupSchedule (shortName: etcdbs)

Schedule controller. Creates EtcdMaintenance CRs with operation=backup on a repeating interval. The schedule field accepts Go duration strings (e.g. "24h", "6h").

No Conductor Job submitted directly. All actual work is delegated to the EtcdMaintenance CRs this controller creates.

Key spec fields: clusterRef, schedule, s3Destination, etcdBackupS3SecretRef.

Status fields: nextRunAt, lastRunAt, lastBackupName.

---

### NodeMaintenance (shortName: nm)

Targeted node-level operations that CAPI has no equivalent for. Applies to both management and target clusters via direct Conductor executor Job regardless of capi.enabled.

Named Conductor capabilities: node-patch, hardening-apply, credential-rotate.

Key spec fields:

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| clusterRef | LocalObjectRef | yes | TalosCluster this operation targets. |
| operation | string (patch, hardening-apply, credential-rotate) | yes | Node-level operation to perform. |
| targetNodes | []string | no | Node names or IPs to target. All nodes when empty. |
| patchSecretRef | *SecretRef | no | Secret containing the machine config patch YAML. Required when operation=patch. |
| hardeningProfileRef | *LocalObjectRef | no | HardeningProfile CR to apply. Required when operation=hardening-apply. |
| rotateServiceAccountKeys | bool | no | Rotate service account signing keys. Applies when operation=credential-rotate. |
| rotateOIDCCredentials | bool | no | Rotate OIDC credentials. Applies when operation=credential-rotate. |

Status condition types: Ready, Degraded.

---

### NodeOperation (shortName: nop)

Node lifecycle operations. Dual-path CRD governed by capi.enabled on the owning TalosCluster.

CAPI path (capi.enabled=true): modifies MachineDeployment replicas for scale-up, deletes specific Machine objects for decommission, or sets the Machine reboot annotation. All handled natively by CAPI. No Conductor Job submitted.

Non-CAPI path (capi.enabled=false): submits node-scale-up, node-decommission, or node-reboot Conductor executor Job.

Named Conductor capabilities (non-CAPI path only): node-scale-up, node-decommission, node-reboot.

Key spec fields:

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| clusterRef | LocalObjectRef | yes | TalosCluster this operation targets. |
| operation | string (scale-up, decommission, reboot) | yes | Node lifecycle operation to perform. |
| targetNodes | []string | no | Node names for decommission or reboot. Required when operation=decommission or reboot. |
| replicaCount | int32 | no | Desired worker replicas after scale-up. Required when operation=scale-up. |

Status condition types: Ready, Degraded, CAPIDelegated.

---

### PKIRotation (shortName: pkir)

Cluster PKI certificate rotation. Always submits a direct Conductor executor Job via the pki-rotate named capability regardless of capi.enabled. CAPI has no PKI rotation equivalent.

Named Conductor capability: pki-rotate.

Key spec fields: clusterRef.

Status fields: jobName, operationResult.
Status condition types: Ready, Degraded.

PKI rotation automation: TalosCluster reconciler monitors pkiExpiryDate and auto-creates a PKIRotation CR when expiry is within pkiRotationThresholdDays days. On-demand rotation is triggered by applying the `platform.ontai.dev/rotate-pki=true` annotation to the TalosCluster CR.

---

### ClusterReset (shortName: crst)

Destructive factory reset. HUMAN GATE REQUIRED: the `ontai.dev/reset-approved=true` annotation must be present before any reconciliation proceeds (CP-INV-006, INV-007). The reconciler holds at PendingApproval and emits an event if the annotation is absent.

CAPI path (capi.enabled=true): deletes CAPI Cluster object first, waits for all Machine objects to reach Deleted phase through the Seam Infrastructure Provider, then submits the cluster-reset Conductor Job.

Non-CAPI path (capi.enabled=false): submits cluster-reset Conductor Job directly.

Named Conductor capability: cluster-reset.

Key spec fields:

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| clusterRef | LocalObjectRef | yes | TalosCluster to reset. |
| drainGracePeriodSeconds | int32 | no | Seconds to wait for node drain before forcing the reset. Default 300. |
| wipeDisks | bool | no | Whether to call the Talos reset API with wipeDisks=true. Default false. |

Status condition types: PendingApproval, Ready, Degraded.

---

### ClusterMaintenance (shortName: cmaint)

Maintenance window gate. Dual-path CRD governed by capi.enabled on the owning TalosCluster.

CAPI path (capi.enabled=true): sets `cluster.x-k8s.io/paused=true` on the CAPI Cluster when no active window exists and blockOutsideWindows=true. Pause halts all CAPI reconciliation until the window opens and the annotation is lifted.

Non-CAPI path (capi.enabled=false): blocks Conductor Job admission gate for the cluster during restricted periods.

No Conductor Job is submitted by this CRD.

Key spec fields:

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| clusterRef | LocalObjectRef | yes | TalosCluster this maintenance gate controls. |
| windows | []MaintenanceWindow | no | Maintenance windows during which operations are permitted. |
| windows[].name | string | no | Optional label for this window. |
| windows[].start | string | yes | Window start time in cron format (e.g. "0 2 * * 6" for 02:00 every Saturday UTC). |
| windows[].durationMinutes | int32 | yes | Length of the maintenance window in minutes. |
| windows[].timezone | string | no | IANA timezone for interpreting the cron schedule. Default UTC. |
| blockOutsideWindows | bool | no | Block operations when no active window exists. Default false. |

Status fields: activeWindowName.
Status condition types: Paused, WindowActive.

---

### UpgradePolicy (shortName: upgp)

Governs Talos OS, Kubernetes, or combined stack upgrades. Dual-path CRD governed by capi.enabled on the owning TalosCluster.

CAPI path (capi.enabled=true): updates TalosControlPlane version and MachineDeployment rolling upgrade settings natively through CAPI machinery. No Conductor Job submitted.

Non-CAPI path (capi.enabled=false): submits talos-upgrade, kube-upgrade, or stack-upgrade Conductor executor Job.

Named Conductor capabilities (non-CAPI path only): talos-upgrade, kube-upgrade, stack-upgrade.

Key spec fields:

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| clusterRef | LocalObjectRef | yes | TalosCluster this upgrade targets. |
| upgradeType | string (talos, kubernetes, stack) | yes | Type of upgrade to perform. |
| targetTalosVersion | string | no | Target Talos version. Required when upgradeType=talos or stack. |
| targetKubernetesVersion | string | no | Target Kubernetes version. Required when upgradeType=kubernetes or stack. |
| rollingStrategy | string (sequential, parallel) | no | Order in which nodes are upgraded. Default sequential. |
| healthGateConditions | []string | no | Kubernetes condition types that must be True on each node before upgrade proceeds to the next node. |

Status condition types: Ready, Degraded, CAPIDelegated.

---

### HardeningProfile (shortName: hp)

Reusable hardening ruleset. Configuration CR only. Does not directly trigger a Conductor Job. Jobs are submitted by NodeMaintenance (operation=hardening-apply) when it references this profile. Referenced by TalosCluster.spec.hardeningProfileRef for bootstrap-time hardening application.

Key spec fields:

| Field | Type | Description |
|-------|------|-------------|
| machineConfigPatches | []string | JSON Patch operations applied to the rendered machineconfig. |
| sysctlParams | map[string]string | Sysctl key/value pairs merged into the machineconfig sysctl section. |
| description | string | Human-readable description. |

Status condition types: Valid.

---

### MaintenanceBundle (shortName: mb)

Pre-compiled scheduling artifact produced by `compiler maintenance`. Carries pre-resolved scheduling context so neither Platform nor Conductor need to perform cluster queries at execution time.

The reconciler is a stub (F-P5 milestone). The type definition is delivered; reconciler implementation is deferred.

Named Conductor capabilities: drain, upgrade, etcd-backup, machineconfig-rotation.

Key spec fields:

| Field | Type | Description |
|-------|------|-------------|
| clusterRef | LocalObjectRef | TalosCluster this bundle targets. |
| operation | string (drain, upgrade, etcd-backup, machineconfig-rotation) | Maintenance operation type. |
| maintenanceTargetNodes | []string | Pre-resolved list of target nodes, validated against the live cluster at compile time. |
| operatorLeaderNode | string | Node hosting the platform operator leader pod at compile time. |
| s3ConfigSecretRef | *corev1.SecretReference | Pre-resolved S3 configuration Secret. Never absent when the operation requires it. |

Status condition types: Ready, Pending, Degraded.

---

### TalosMachineConfigBackup (shortName: mcb)

Triggers a machine config backup for all nodes of a cluster. The Conductor executor reads each node's running config via GetMachineConfig and uploads it to S3 at `{cluster}/machineconfigs/{TIMESTAMP}/{hostname}.yaml`.

Named Conductor capability: machineconfig-backup.

Key spec fields: clusterRef, s3BackupSecretRef, s3Destination.

Status condition types: Ready, Running, Degraded, S3DestinationAbsent.

---

### TalosMachineConfigBackupSchedule (shortName: mcbs)

Schedule controller. Creates TalosMachineConfigBackup CRs on a repeating interval. The schedule field accepts Go duration strings (e.g. "24h").

No Conductor Job submitted directly. All actual work is delegated to the TalosMachineConfigBackup CRs this controller creates.

Key spec fields: clusterRef, schedule, s3Destination, s3BackupSecretRef.

Status fields: nextRunAt, lastRunAt, lastBackupName.

---

### TalosMachineConfigRestore (shortName: mcr)

Triggers a machine config restore for target nodes of a cluster. The Conductor executor downloads each node's config from S3 at `{cluster}/machineconfigs/{backupTimestamp}/{hostname}.yaml` and applies it via ApplyConfiguration.

Named Conductor capability: machineconfig-restore.

Key spec fields:

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| clusterRef | LocalObjectRef | yes | TalosCluster whose nodes will be restored. |
| backupTimestamp | string | yes | Timestamp of the backup to restore from. Format: 20060102T150405Z (UTC). Must match the timestamp component in the S3 path written by a prior machineconfig-backup. |
| targetNodes | []string | no | Node hostnames to restore. All nodes when empty. |
| s3SourceBucket | string | yes | S3 bucket containing the backup objects. |
| s3BackupSecretRef | *corev1.SecretReference | no | S3 credentials Secret. Falls back to seam-etcd-backup-config in seam-system. |

Status fields: phase (Pending, Running, Succeeded, Failed, PartiallyFailed), restoredNodes.
Status condition types: Ready, Running, Degraded, S3SourceAbsent.

---

## 6. CAPI Integration Model

### Seam Infrastructure Provider

The Seam Infrastructure Provider is a purpose-built platform component that implements the CAPI InfrastructureCluster and InfrastructureMachine contracts. It does not call any cloud API. It watches SeamInfrastructureCluster and SeamInfrastructureMachine objects and delivers machineconfigs to pre-provisioned Talos nodes on port 50000 using the talos goclient.

The talos goclient is restricted to SeamInfrastructureClusterReconciler and SeamInfrastructureMachineReconciler only (CP-INV-001). All other reconcilers observe cluster state through CAPI Machine status conditions and Kubernetes node labels only (CP-INV-002).

### CAPI object ownership

Platform's TalosCluster reconciler creates and owns:

- SeamInfrastructureCluster (infrastructure reference for the CAPI Cluster)
- cluster.x-k8s.io/Cluster (owns TalosControlPlane and MachineDeployments)
- TalosControlPlane (CABPT control plane management)
- MachineDeployment per node role
- TalosConfigTemplate (CABPT machineconfig generation template)
- SeamInfrastructureMachineTemplate (template for SeamInfrastructureMachine per node)

All created in seam-tenant-{cluster-name} and owned by TalosCluster via ownerReference (CP-INV-008).

### Cilium CNI integration

Every TalosConfigTemplate created by platform includes `cluster.network.cni.name: none` and Cilium BPF kernel parameters (CP-INV-009). After CAPI bootstraps the cluster, platform triggers a PackExecution for the Cilium PackDelivery referenced by spec.capi.ciliumPackRef. Nodes transition to Ready only after Cilium is up.

CiliumPending on TalosCluster is not a degraded state (CP-INV-013). It is the expected state between CAPI cluster Running and Cilium PackInstance Ready.

### SeamInfrastructureCluster fields

Cluster-level CAPI infrastructure reference. One per cluster.

Key spec fields: controlPlaneEndpoint.host (VIP or first control plane IP), controlPlaneEndpoint.port (default 6443).

Status: ready=true after all control plane SeamInfrastructureMachine objects have status.ready=true.

### SeamInfrastructureMachine fields

Per-node CAPI infrastructure reference. One per node.

Key spec fields: address (pre-provisioned node IP reachable on port 50000), port (default 50000), talosConfigSecretRef, nodeRole (controlplane or worker).

Status fields: ready (true after machineconfig applied and node exits maintenance mode), machineConfigApplied, providerID (format: talos://{cluster-name}/{node-ip}).

---

## 7. Tenant Namespace Model

Platform is the sole namespace creation authority for seam-tenant-{cluster-name} namespaces (CP-INV-004). No other operator or component creates these namespaces.

Namespace provisioning by mode:

- mode=bootstrap and capi.enabled=true: Platform creates the namespace in the reconcile path. No bootstrap bundle assist needed.
- mode=import: Platform creates the namespace as part of the two-site onboarding sequence. The namespace creation is idempotent. The Compiler bootstrap bundle for import clusters includes a seam-tenant-namespace.yaml manifest so the admin can apply Secrets and the TalosCluster CR in a single kubectl apply run. Platform's ensureTenantNamespace call in the import reconcile path is an idempotent safety net.

When the ClusterLog CR is created, it is placed in seam-tenant-{cluster-name} and named by the cluster name.

MachineConfig Secrets for native and imported clusters follow the naming convention `seam-mc-{cluster-name}-{node-name}` in seam-tenant-{cluster-name}. Platform is the sole owner of these Secrets. No other operator or Conductor capability handler may modify them.

---

## 8. Etcd Backup S3 Resolution

Platform resolves the S3 backup destination at RunnerConfig creation time. Conductor and the etcd-backup Job receive the resolved Secret reference via RunnerConfig and perform no S3 resolution themselves.

Resolution order:

1. Explicit reference on the EtcdMaintenance CR (spec.etcdBackupS3SecretRef): if present, use this Secret.
2. Platform-wide default Secret (seam-etcd-backup-config in seam-system): if no explicit reference is present and this Secret exists, use it.
3. Absent condition: if neither exists, platform sets EtcdBackupDestinationAbsent on the EtcdMaintenance CR with status=True and does not emit a RunnerConfig. Silent failure is never permitted.

Local PVC fallback: permitted only as a visible degraded mode when spec.pvcFallbackEnabled=true. Platform sets EtcdBackupLocalFallback condition with status=True and the CR status explicitly states the backup is non-durable.

S3 path structure within the bucket: `etcd-backup/{cluster-uid}/` where cluster-uid is the TalosCluster UID. UIDs are immutable and globally unique across clusters.

S3 Secret key contract: Both MinIO/Scality camelCase keys (accessKeyID, secretAccessKey, region, endpoint) and AWS SDK env var names (AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, S3_REGION, S3_ENDPOINT) are accepted. The reconciler normalizes to AWS SDK env var form and writes a projected Secret named `{em.Name}-s3-env` in em.Namespace owned by the EtcdMaintenance CR. The executor Job mounts this projected Secret via envFrom. Cross-namespace secret projection: source Secret may reside in seam-system while the executor Job runs in seam-tenant-{cluster}.

---

## 9. Conductor Capability Dispatch

Platform generates a RunnerConfig using the shared runner library (CP-INV-003) and submits a Conductor executor Job. The RunnerConfig targets a named capability. The mapping from CRD to capability is:

| CRD | Operation | Conductor capability |
|-----|-----------|---------------------|
| EtcdMaintenance | backup | etcd-backup |
| EtcdMaintenance | restore | etcd-restore |
| EtcdMaintenance | defrag | etcd-defrag |
| NodeMaintenance | patch | node-patch |
| NodeMaintenance | hardening-apply | hardening-apply |
| NodeMaintenance | credential-rotate | credential-rotate |
| NodeOperation | scale-up | node-scale-up (non-CAPI path) |
| NodeOperation | decommission | node-decommission (non-CAPI path) |
| NodeOperation | reboot | node-reboot (non-CAPI path) |
| PKIRotation | (any) | pki-rotate |
| ClusterReset | (any) | cluster-reset |
| UpgradePolicy | talos | talos-upgrade (non-CAPI path) |
| UpgradePolicy | kubernetes | kube-upgrade (non-CAPI path) |
| UpgradePolicy | stack | stack-upgrade (non-CAPI path) |
| TalosMachineConfigBackup | (any) | machineconfig-backup |
| TalosMachineConfigRestore | (any) | machineconfig-restore |
| MaintenanceBundle | drain | drain |
| MaintenanceBundle | upgrade | upgrade |
| MaintenanceBundle | etcd-backup | etcd-backup |
| MaintenanceBundle | machineconfig-rotation | machineconfig-rotation |

Dual-path CRDs (UpgradePolicy, NodeOperation, ClusterMaintenance) do NOT submit a Conductor Job on the CAPI path. Platform must check capi.enabled on the owning TalosCluster before deciding which path to take.

Kueue is not used for any platform Job submission (CP-INV-010).

---

## 10. RunnerConfig Generation Protocol

RunnerConfig is generated by platform using the shared runner library for all operational Job CRDs. It is never hand-coded (CP-INV-003). It is not generated for CAPI-managed lifecycle operations.

The RunnerConfig carries the pre-resolved capability name, S3 configuration (for operations that require it), target cluster kubeconfig and talosconfig Secret references, target node list, operator leader node, and any operation-specific parameters.

Platform reads the RunnerConfig.agentImage field to determine the Conductor image tag to use for the executor Job. The Conductor image tag must match the cluster's Talos version (INV-012).

For import-mode clusters, Platform drives a two-site onboarding sequence that includes deploying Conductor agent mode (role=tenant) in ont-system on the tenant cluster. See guardian-schema.md for the full handshake protocol.

---

## 11. Conductor Deployment Contract

Platform is exclusively responsible for deploying Conductor agent mode onto every tenant cluster it forms. This happens after TalosCluster formation reaches the readiness threshold and before marking the cluster fully Ready.

- Platform creates exactly one Conductor Deployment per tenant cluster, in ont-system on that cluster.
- The Deployment must carry role=tenant as a first-class field. An absent or incorrect role causes Conductor to exit with InvariantViolation.
- Platform does not deploy Conductor to the management cluster. `compiler enable` is the sole authority for the management cluster Conductor Deployment (role=management).
- If the Conductor Deployment is deleted from a tenant cluster's ont-system, Platform must recreate it on the next TalosClusterReconciler reconcile cycle.
- The Conductor image tag must match RunnerConfig.agentImage for this cluster.

---

## 12. MachineConfig Storage Contract

For native and imported clusters (capi.enabled=false), Platform is the sole owner of machineconfig generation and storage.

Naming convention: `seam-mc-{cluster-name}-{node-name}` in seam-tenant-{cluster-name}.

mode=bootstrap: Platform generates machineconfig Secrets from TalosClusterSpec at bootstrap time. Platform applies HardeningProfile patches on top of the base config when spec.hardeningProfileRef is set.

mode=import: Platform captures machineconfig Secrets from the running cluster via the Talos COSI API (/system/state/config.yaml) immediately after kubeconfig Secret generation. Platform uses the talosconfig Secret to authenticate, lists nodes via kubeconfig, and reads the machineconfig from each node.

No other operator or Conductor capability handler owns these Secrets. A machineconfig Secret owned by Platform must never be modified by any other component.

---

## 13. PKI Rotation Contract

Imported Talos clusters carry two sets of short-lived certificates stored in Secrets: admin kubeconfig (Kubernetes client cert) and talosconfig client cert.

Spec fields on TalosCluster: pkiRotationThresholdDays (int32, default 30, minimum 1).

Status fields on TalosCluster: pkiExpiryDate (*metav1.Time) -- earliest certificate expiry across both Secrets.

Auto-rotation: when pkiExpiryDate is within pkiRotationThresholdDays days, the reconciler creates a PKIRotation CR with label pki-trigger=auto. Idempotent: skips if a PKIRotation CR already exists for this cluster and is not yet complete or failed.

On-demand rotation: annotation `platform.ontai.dev/rotate-pki=true` on TalosCluster. Reconciler creates a PKIRotation CR with label pki-trigger=manual, then clears the annotation via Patch.

PKI expiry check runs only for stable-Ready clusters. Stable-Ready clusters are requeued every 24 hours for daily expiry monitoring.

---

## 14. Cross-Domain Rules

Platform reads: guardian.ontai.dev/RBACProfile status (gate check before starting).
Platform reads: dispatcher PackInstalled status (gate Cilium PackExecution on Ready).
Platform owns: cluster.x-k8s.io/Cluster and all CAPI child objects for target clusters.
Platform owns: SeamInfrastructureCluster, SeamInfrastructureMachine in tenant namespaces.
Platform creates: seam-tenant-{cluster-name} namespaces (sole authority, CP-INV-004).
Platform never writes to guardian.ontai.dev CRDs.
Platform never writes to dispatcher.ontai.dev CRDs.

---

## 15. Decision Records

**Decision H -- TalosCluster is the Seam root CR.** All CAPI objects for target clusters are children of TalosCluster via ownerReference. CAPI objects do not exist without a TalosCluster parent.

**Decision I -- Deletion of TalosCluster never destroys a cluster.** Kubernetes garbage collection cascades to owned CAPI objects, which triggers CAPI's own deletion reconciliation, but this does not factory reset nodes. ClusterReset is the only destruction path (INV-015).

**Decision J -- CiliumPending is not degraded.** The window between CAPI cluster Running and Cilium PackInstance Ready is expected. Nodes are NotReady during this window. The MachineHealthCheck tolerance window must be configured to avoid spurious remediation during Cilium installation (CP-INV-013).

**Decision K -- Kueue is not used for any platform operation.** Operational runner Jobs submit directly. Kueue governs dispatcher pack-deploy Jobs exclusively. This applies permanently; the decision is locked (CP-INV-010).

**Decision L -- S3 destination is resolved at RunnerConfig creation time.** Conductor never performs S3 resolution. A Conductor execute-mode Job that independently resolves an S3 destination is an invariant violation.

**Decision M -- Platform is the sole Conductor deployer for tenant clusters.** No other component deploys Conductor to tenant clusters. Role must be stamped as role=tenant. Incorrect or absent role is an InvariantViolation (platform-schema.md §11).

---

*platform.ontai.dev schema -- platform operator*
*Amended 2026-05-13: Full rewrite. seam-core references corrected to seam. TalosCluster and ClusterLog placed under seam.ontai.dev. All platform.ontai.dev types documented from current Go sources. Kueue scope corrected (dispatcher, not platform). wrapper references corrected to dispatcher. All stale type names removed.*
