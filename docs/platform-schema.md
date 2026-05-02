# platform-schema
> API Group: platform.ontai.dev (operational CRDs: TalosControlPlane, TalosWorkerConfig, EtcdMaintenance, NodeMaintenance, PKIRotation, ClusterReset, HardeningProfile, UpgradePolicy, NodeOperation, ClusterMaintenance, PlatformTenant, QueueProfile, MaintenanceBundle)
> InfrastructureTalosCluster: infrastructure.ontai.dev/v1alpha1 -- schema owned by seam-core (Decision G). Platform reconciles it; does not define it.
> Operator: Platform
> CAPI Providers: cluster.x-k8s.io, bootstrap.cluster.x-k8s.io, infrastructure.cluster.x-k8s.io
> Amended: 2026-03-30 - CAPI adopted for target cluster lifecycle. Management cluster
>   bootstrap unchanged. SeamInfrastructureMachine CRD introduced. Kueue scoped to
>   Wrapper quota profile. Operational CRDs retained where CAPI has no equivalent.

---

## 1. Domain Boundary

Platform owns the complete lifecycle of Talos clusters and all tenant
coordination. It does this by composing CAPI primitives for target cluster
lifecycle while preserving Seam's governing principles - declarative, versioned,
auditable, and security-first.

Platform is the CAPI management plane operator. It creates and owns CAPI
objects (Cluster, TalosControlPlane, MachineDeployment, SeamInfrastructureMachine)
as children of the ONT TalosCluster CR. CAPI controllers reconcile those objects
to actual cluster state through the Seam Infrastructure Provider and CABPT.

**What changes with CAPI adoption:**
- Target cluster lifecycle (bootstrap, upgrade, scale, health) is delegated to CAPI.
- The Seam Infrastructure Provider (part of Platform) delivers machineconfigs
  to nodes on port 50000 - it is the Talos-specific infrastructure layer.
- Kueue Jobs are no longer used for cluster lifecycle operations.
- Kueue is retained as a prerequisite exclusively for Wrapper pack-deploy Jobs.
- CAPI provides the observability (Machine status, Cluster conditions, events)
  that Kueue Jobs previously provided for cluster operations.

**What does not change:**
- Management cluster bootstrap remains Seam-native. CAPI cannot bootstrap the
  cluster it runs on. See Section 3 for the unchanged management cluster path.
- All Seam security plane rules. CAPI's RBAC goes through Guardian intake.
- Guardian deploys before CAPI. CAPI is installed in the enable phase after
  Guardian is operational.
- TalosCluster is still the Seam root CR for every cluster. CAPI objects are
  children of TalosCluster, not the other way around.
- Operational CRDs with no CAPI equivalent remain and use Conductor capabilities
  invoked via direct controller reconciliation.
- Platform creates tenant namespaces. Sole namespace authority unchanged.

---

## 2. CAPI Provider Architecture

### 2.1 Providers Installed on Management Cluster

**CAPI Core** (cluster.x-k8s.io) - Cluster, Machine, MachineDeployment,
MachineSet, MachineHealthCheck controllers. These are the battle-tested cluster
lifecycle primitives. Installed via OperatorManifest in the enable phase, after
Guardian.

**CABPT** (bootstrap.cluster.x-k8s.io) - Cluster API Bootstrap Provider Talos.
Generates TalosConfig and renders machineconfigs per Machine. Patches TalosConfig
with cluster-specific CNI=none and kernel parameters needed for Cilium. CABPT is
the source of rendered machineconfig data that the Seam Infrastructure Provider
delivers to nodes.

**Seam Infrastructure Provider** - a purpose-built Platform component that
implements the CAPI InfrastructureCluster and InfrastructureMachine contracts.
It does not call any cloud API. It watches SeamInfrastructureCluster and
SeamInfrastructureMachine objects and delivers machineconfigs to pre-provisioned
Talos nodes on port 50000 using the talos goclient embedded in the provider binary.
This is the only place in Platform that uses the talos goclient after bootstrap.
The provider is a distroless Go binary - talos goclient + kube goclient only.

### 2.2 CAPI Object Ownership

Platform's TalosCluster reconciler creates and owns:
- SeamInfrastructureCluster (infra reference for the CAPI Cluster)
- cluster.x-k8s.io/Cluster (owns TalosControlPlane and MachineDeployments)
- TalosControlPlane (CACPPT - control plane management)
- MachineDeployment per node role (control plane, worker)
- TalosConfigTemplate (CABPT - machineconfig generation template with CNI patches)
- SeamInfrastructureMachineTemplate (template for SeamInfrastructureMachine per node)

These are all created in the tenant-{cluster-name} namespace and owned by the
TalosCluster CR via ownerReference. Deleting TalosCluster cascades to all owned
CAPI objects through Kubernetes garbage collection, which triggers CAPI's own
deletion reconciliation. Seam finalizers on TalosCluster gate this to ensure
security plane cleanup happens before cascade.

### 2.3 Cilium CNI Integration

Every TalosConfigTemplate created by Platform includes:
- cluster.network.cni.name: none (disables default CNI, required for Cilium)
- BPF kernel parameters in machine config patches
- Cilium-required sysctl values

After CAPI bootstraps the cluster (nodes reach Running state but are NotReady
because no CNI is present), Platform triggers a PackExecution for the Cilium
ClusterPack referenced by spec.capi.ciliumPackRef. This is the first pack deployed
to every cluster. Nodes transition to Ready only after Cilium is up.

The CAPI MachineHealthCheck is configured with a tolerance window for the CNI
installation period - nodes are not remediated during this window.

The Cilium ClusterPack is compiled per-cluster on the workstation with values
specific to the cluster endpoint, IPAM mode, L2 announcement configuration, MTU,
and routing mode. It is not a generic pack - it carries the cluster endpoint
address at compile time.

---

## 3. Management Cluster Bootstrap - Unchanged

Management cluster bootstrap does not use CAPI. CAPI cannot bootstrap the cluster
it runs on. The management cluster bootstrap path is:

Human runs Compiler compile mode → generates machineconfigs, SOPS-encrypts
secrets → secrets committed to git → TalosCluster CR (mode: bootstrap) committed
to git → GitOps applies to a temporary Kubernetes context (or direct kubectl) →
Platform generates a bootstrap Job using compiler directly → conductor pushes
machineconfigs to seed nodes on port 50000 → etcd initializes → Kubernetes API
comes up → enable phase installs Guardian first, then CAPI providers and
remaining prerequisites, then other operators.

After the management cluster exists, CAPI is installed and manages only target
clusters. The management cluster's own TalosCluster CR in seam-system has
mode: bootstrap and no CAPI children - management cluster lifecycle is not
CAPI-managed.

---

## 4. Seam Infrastructure CRDs

### SeamInfrastructureMachine

Scope: Namespaced - tenant-{cluster-name}
Short name: sim
API group: infrastructure.cluster.x-k8s.io (CAPI infrastructure contract)

Wraps a pre-provisioned node IP address and its connection parameters. This is the
Seam-native implementation of the CAPI InfrastructureMachine contract. One
SeamInfrastructureMachine per node in the cluster.

The human (or GitOps) declares the available node IPs as SeamInfrastructureMachine
objects in the tenant namespace before the cluster is bootstrapped. The Seam
Infrastructure Provider watches for CAPI Machine objects that reference these and
delivers the CABPT-rendered machineconfig to the declared IP on port 50000.

Key spec fields:
- address: the pre-provisioned node IP address reachable on port 50000.
- port: Talos maintenance API port. Default 50000.
- talosConfigSecretRef: reference to the talosconfig secret in ont-system that
  the provider uses to authenticate the ApplyConfiguration call.
- nodeRole: controlplane or worker. Must match the MachineDeployment role.

Status fields (set by the Seam Infrastructure Provider):
- ready: bool. Set to true after machineconfig is applied and the node transitions
  out of maintenance mode.
- machineConfigApplied: bool.
- providerID: the provider ID string written back to the CAPI Machine object.
  Format: talos://{cluster-name}/{node-ip}

CAPI contract compliance: SeamInfrastructureMachine implements the InfrastructureMachine
contract by setting status.ready=true when the machine is provisioned, and writing
spec.providerID back to the owning Machine object.

---

### SeamInfrastructureCluster

Scope: Namespaced - tenant-{cluster-name}
Short name: sic
API group: infrastructure.cluster.x-k8s.io

The cluster-level CAPI infrastructure reference. Holds the cluster endpoint and
any cluster-wide infrastructure parameters. One per cluster. Owned by the CAPI
Cluster object.

Key spec fields:
- controlPlaneEndpoint.host: the VIP or first control plane IP. Written into
  the CAPI Cluster object and into all generated machineconfigs via CABPT.
- controlPlaneEndpoint.port: Kubernetes API port. Default 6443.

Status fields:
- ready: bool. Set to true after all control plane SeamInfrastructureMachine
  objects have status.ready=true.

---

### TalosControlPlane

Scope: Namespaced - tenant-{cluster-name}
Short name: tcpl
API group: platform.ontai.dev

Dual-mode CRD. At compile time it serves as a command contract: Compiler reads
TalosControlPlane spec to generate management cluster bootstrap configuration
before any live cluster exists. At cluster runtime it is a live CR reconciled by
Platform. Carries the admin's complete control plane configuration intent.

Must never be merged with TalosWorkerConfig - they evolve independently and a
combined CRD would risk CRD size limits.

Key spec fields: replicas, talosVersion, kubernetesVersion, machineConfigPatches,
hardeningProfileRef, endpointVIP, installerImage.

TalosCluster references TalosControlPlane by name in its spec.

---

### TalosWorkerConfig

Scope: Namespaced - tenant-{cluster-name}
Short name: twc
API group: platform.ontai.dev

Dual-mode CRD. Same dual-mode pattern as TalosControlPlane - compile-time command
contract and live cluster CR. Carries worker node machine configuration intent per
pool. Must never be merged with TalosControlPlane.

Key spec fields: pools (each with name, replicas, machineConfigPatches, nodeLabels,
nodeTaints), talosVersion, installerImage, hardeningProfileRef.

TalosCluster references TalosWorkerConfig by name in its spec.

---

**Dual-mode pattern:** Both TalosControlPlane and TalosWorkerConfig operate in two
modes. In compile mode, Compiler reads them as command contracts to generate bootstrap
artifacts before any cluster exists. In runtime mode, Platform reconciles them as live
CRs on running clusters, creating TalosConfigTemplate and CABPT objects from their
specs.

---

## 5. CRDs - Platform-Owned

These CRDs are owned by Platform. They are not delegated to CAPI because CAPI has
no equivalent concept, or because they represent dual-path operations where the
management cluster path requires a direct conductor Job while CAPI handles the target
cluster path natively.

### InfrastructureTalosCluster

Kind: InfrastructureTalosCluster. API group: infrastructure.ontai.dev/v1alpha1. Schema owned by seam-core (Decision G). Supersedes platform.ontai.dev/TalosCluster (Phase 2B, 2026-04-25).
Platform reconciles this type but does not own its CRD definition. Condition constants are imported from seam-core/pkg/conditions, not defined locally in platform.
Scope: Namespaced - seam-system (management), seam-tenant-{cluster-name} (target)
Short name: tc
Lives in: git and management cluster.

The Seam root CR for every cluster. For target clusters, InfrastructureTalosCluster owns all
CAPI objects as children. For the management cluster, InfrastructureTalosCluster has no CAPI
children - it is the bootstrap record and operational anchor.

spec.mode (v1alpha1 only): bootstrap or import. As before.

Fields introduced with CAPI adoption:
- capi.enabled: bool. True for all target clusters. False for management cluster.
  When true, the TalosCluster reconciler creates CAPI objects. When false, it
  follows the direct bootstrap path.
- capi.talosVersion: Talos version to pass to TalosConfigTemplate and CABPT.
- capi.kubernetesVersion: Kubernetes version for TalosControlPlane.
- capi.controlPlane.replicas: number of control plane nodes.
- capi.workers: list of worker pools, each with a name, replica count, and
  list of SeamInfrastructureMachine names pre-provisioned for that pool.
- capi.ciliumPackRef: the ClusterPack name and version for Cilium. Platform
  triggers a PackExecution for this pack when the cluster reaches CAPI Running
  state, before marking the cluster Ready.

status.origin: bootstrapped or imported. Unchanged.
status.capiClusterRef: reference to the owned CAPI Cluster object.
Status conditions: Ready, Bootstrapping, Importing, Degraded, CiliumPending.

CiliumPending is set when the cluster reaches CAPI Running state but the Cilium
ClusterPack has not yet reached PackInstance.Ready. Nodes are NotReady during
this window. This is expected and not a degraded state.

---

### EtcdMaintenance

Scope: Namespaced - tenant-{cluster-name}
Short name: em
Named conductor capabilities: etcd-backup, etcd-restore, etcd-defrag

Absorbs TalosBackup, TalosEtcdMaintenance, TalosRecovery. Covers all etcd
lifecycle operations for both management and target clusters. CAPI has no etcd
concept. Always a direct conductor (mode: execute) Job regardless of spec.capi.enabled on TalosCluster.

Key spec fields: clusterRef, operation (backup, restore, defrag), s3Destination (for
backup), s3SnapshotPath (for restore), targetNodes (for restore), schedule (for
recurring backup).

---

### NodeMaintenance

Scope: Namespaced - tenant-{cluster-name}
Short name: nm
Named conductor capabilities: node-patch, hardening-apply, credential-rotate

Absorbs TalosNodePatch, TalosHardeningApply, TalosCredentialRotation. Covers
targeted node-level operations CAPI has no equivalent for. Applies to both
management and target clusters via direct conductor(mode: execute) Job regardless of spec.capi.enabled.

Key spec fields: clusterRef, operation (patch, hardening-apply, credential-rotate),
targetNodes, patchSecretRef (for patch), hardeningProfileRef (for hardening-apply),
rotateServiceAccountKeys and rotateOIDCCredentials (for credential-rotate).

---

### PKIRotation

Scope: Namespaced - tenant-{cluster-name}
Short name: pkir
Named Conductor capability: pki-rotate

Absorbs TalosPKIRotation. Single-purpose. Applies to both management and target
clusters via direct conductor(mode: execute) Job. CAPI has no PKI rotation equivalent.

Key spec fields: clusterRef.

---

### ClusterReset

Scope: Namespaced - tenant-{cluster-name}
Short name: crst
Named conductor capability: cluster-reset

Absorbs TalosClusterReset. Destructive factory reset. Human gate required:
ontai.dev/reset-approved=true annotation must be present before any reconciliation
proceeds.

For CAPI-managed clusters (spec.capi.enabled=true): deletes CAPI Cluster object
first, waits for all Machine objects to reach Deleted phase through the Seam
Infrastructure Provider, then submits cluster-reset conductor(mode: execute) Job.

For management cluster (spec.capi.enabled=false): submits cluster-reset Conductor(mode: execute) Job directly.

Key spec fields: clusterRef, drainGracePeriodSeconds, wipeDisks.

---

### HardeningProfile

Scope: Namespaced
Short name: hp

Absorbs TalosHardeningProfile. Configuration CR only - not an operational Job
trigger. Reusable hardening ruleset referenced by NodeMaintenance at runtime and
by TalosControlPlane and TalosWorkerConfig at compile time.

Key spec fields: machineConfigPatches, sysctlParams, description.

---

### UpgradePolicy

Scope: Namespaced - tenant-{cluster-name}
Short name: upgp

Absorbs TalosUpgrade, TalosKubeUpgrade, TalosStackUpgrade. Dual-path CRD governed
by spec.capi.enabled on the owning TalosCluster.

For CAPI-managed clusters (spec.capi.enabled=true): updates TalosControlPlane
version field and MachineDeployment rolling upgrade settings natively through CAPI
machinery - no conductor(mode: execute) Job submitted.

For management cluster (spec.capi.enabled=false): submits talos-upgrade, kube-upgrade,
or stack-upgrade conductor(mode: execute) Job via OperationalJobReconciler routing.

Key spec fields: clusterRef, upgradeType (talos, kubernetes, stack),
targetTalosVersion, targetKubernetesVersion, rollingStrategy, healthGateConditions.

---

### NodeOperation

Scope: Namespaced - tenant-{cluster-name}
Short name: nop

Absorbs TalosNodeScaleUp, TalosNodeDecommission, TalosReboot. Dual-path CRD
governed by spec.capi.enabled on the owning TalosCluster.

For CAPI-managed clusters (spec.capi.enabled=true): modifies MachineDeployment
replicas for scale-up, deletes specific Machine objects for decommission, or sets
Machine reboot annotation - all handled natively by CAPI.

For management cluster (spec.capi.enabled=false): submits node-scale-up,
node-decommission, or node-reboot conductor(mode: execute) Job.

Key spec fields: clusterRef, operation (scale-up, decommission, reboot),
targetNodes, replicaCount (for scale-up).

---

### ClusterMaintenance

Scope: Namespaced - tenant-{cluster-name}
Short name: cmaint

Absorbs TalosNoMaintenance. Maintenance window gate.

For CAPI-managed clusters (spec.capi.enabled=true): sets cluster.x-k8s.io/paused=true
on the CAPI Cluster object when no active window exists and blockOutsideWindows=true.
Pause halts all CAPI reconciliation until the window opens and the annotation is lifted.

For management cluster (spec.capi.enabled=false): blocks conductor(mode: execute) Job admission gate for
the cluster during restricted periods.

Key spec fields: clusterRef, windows, blockOutsideWindows.

---

## 6. Tenant Coordination CRDs

### PlatformTenant, QueueProfile

PlatformTenant and QueueProfile semantics, namespace placement, and gate conditions
are unchanged. ClusterAssignment has been removed -- it was a pre-seam binding record
with no role in the current seam operator family. Cilium bootstrap is now triggered
directly by Platform via spec.capi.ciliumPackRef when the CAPI Cluster reaches
Running state.

QueueProfile is scoped to Wrapper's quota profile only. The ClusterQueue and
ResourceFlavor resources provisioned by Guardian from QueueProfile govern
pack-deploy Job admission - cluster lifecycle operations no longer go through Kueue.

**LicenseKey has been removed.** Seam has no licensing tier, no JWT enforcement,
and no cluster count limits.

---

## 7. Kueue Scope

Kueue remains a management cluster prerequisite exclusively because Wrapper's
pack-deploy Jobs require it. The ClusterQueue and ResourceFlavor resources
provisioned by Guardian from QueueProfile govern pack-deploy Job admission.

Cluster lifecycle operations (bootstrap, upgrade, scale, decommission) do not use
Kueue. They are reconciled by CAPI controllers directly. The observability
previously provided by Kueue Jobs is now provided by CAPI Cluster and Machine
status conditions and events.

Operational Jobs (etcd-backup, etcd-maintenance, pki-rotate, etcd-restore,
hardening-apply, node-patch, credential-rotate, cluster-reset) submit directly to
the default JobQueue without Kueue admission control. They are targeted, infrequent,
and operator-gated operations that do not require Kueue's quota and scheduling machinery.

---

## 8. CAPI RBAC and Guardian

CAPI installs substantial RBAC: ClusterRoles and ClusterRoleBindings for each
provider controller, ServiceAccounts, and webhook configurations. All of this
must pass through Guardian's third-party RBAC intake protocol before CAPI
controllers start.

The enable phase order is:
1. Guardian (CRD-only phase, webhook operational)
2. cert-manager (RBAC via Guardian intake)
3. Kueue (RBAC via Guardian intake)
4. CNPG (RBAC via Guardian intake, Guardian transitions to phase 2)
5. CAPI core (RBAC via Guardian intake)
6. CABPT (RBAC via Guardian intake)
7. metallb (RBAC via Guardian intake)
8. local-path-provisioner (RBAC via Guardian intake)
9. Platform (RBACProfile provisioned by Guardian, then controller starts)
10. Wrapper (RBACProfile provisioned, then controller starts)

No CAPI component starts until Guardian has processed its RBACProfile and
set provisioned=true.

---

## 9. MachineConfig Storage Contract

**LOCKED INVARIANT - Platform Governor directive 2026-04-05.**

For native and imported Seam clusters (`spec.capi.enabled=false`), Platform operator
is the sole owner of machineconfig generation and storage. This applies to the
management cluster and to any cluster onboarded via the import path.

**Secret naming convention:**
Machineconfigs are stored as Kubernetes Secrets in the management cluster, one Secret
per node, using the naming convention:

    seam-mc-{cluster-name}-{node-name}

in the `seam-tenant-{cluster-name}` namespace.

**Provisioning by mode:**

`spec.mode=bootstrap`: Platform generates machineconfig Secrets from `InfrastructureTalosCluster`
spec at bootstrap time. The Compiler emits only the `InfrastructureTalosCluster` CR; it does
not emit machineconfig Secrets for bootstrap clusters. Platform applies `HardeningProfile` patches
on top of the generated base config when `spec.hardeningProfileRef` is set (PLATFORM-BL-HARDENINGPROFILE-MERGE,
pending schema amendment to add node topology fields to `InfrastructureTalosClusterSpec`).
Until that schema amendment lands, the Compiler bootstrap subcommand continues to emit machineconfig
Secrets for management-cluster bootstrap to preserve the existing bootstrap Job path.

`spec.mode=import`: Platform captures machineconfig Secrets from the running cluster via the Talos
COSI API (`/system/state/config.yaml`) immediately after the kubeconfig Secret is generated. Platform
uses the talosconfig Secret (compiler-emitted, admin-applied before the TalosCluster CR) to authenticate,
lists nodes from the running cluster via kubeconfig, and reads the machineconfig from each node.
The Compiler emits only the `InfrastructureTalosCluster` CR and the talosconfig Secret for import clusters.
It does not emit machineconfig Secrets for import clusters. (PLATFORM-BL-MACHINECONFIG-IMPORT-CAPTURE tracks
the platform-side implementation.)

**Lifecycle:**
After initial capture (import mode) or generation (bootstrap mode), Platform reconciles
these Secrets when node configuration changes -- for example when a HardeningProfile is
updated or a machineconfig patch is applied via NodeMaintenance.

**Namespace authority:**
CP-INV-004: Platform is the sole namespace creation authority for `seam-tenant-{cluster-name}`
for bootstrap and CAPI-managed cluster modes. For mode=import, the Compiler bootstrap bundle
includes a `seam-tenant-namespace.yaml` manifest so the admin can apply the namespace (and
Secrets that live in it) before the TalosCluster CR in a single `kubectl apply -f` run.
Platform's `ensureTenantNamespace` call in the import reconcile path is idempotent -- it
creates the namespace if absent (handles re-reconcile or manual deletion) but does not race
with the bootstrap bundle application. For mode=bootstrap and CAPI: Platform creates the
namespace in the reconcile path with no bootstrap bundle assist needed.

**Design rationale:**
This mirrors the CAPI bootstrap provider secret pattern intentionally. The CAPI path
stores machineconfigs as bootstrap data Secrets managed by CABPT. The native path
stores them as Seam-named Secrets managed by Platform. The operational model is
consistent regardless of provisioning path: a named Secret per node holds the node's
current authoritative machineconfig.

No other operator or Conductor capability handler owns these Secrets.
A machineconfig Secret owned by Platform must never be modified by any other component.
This invariant has no exceptions and requires a Platform Governor constitutional
amendment to change.

---

## 10. Etcd Backup Destination Contract

**LOCKED INVARIANT - Platform Governor directive 2026-04-05.**

Platform operator resolves the S3 backup destination at RunnerConfig creation time -
never deferred to Conductor or the Job. Resolution is deterministic, ordered, and
fails fast with a structured condition rather than silently proceeding without a
destination.

**Resolution order (evaluated at RunnerConfig creation time):**

1. **Explicit reference on TalosCluster CR**: if the TalosCluster spec carries an
   explicit S3 config Secret reference (`spec.etcdBackupS3SecretRef`), Platform uses
   that Secret. No further lookup is performed.

2. **Platform-wide default Secret**: if no explicit reference is present, Platform
   looks for a Secret named `seam-etcd-backup-config` in the `seam-system` namespace.
   If found, it is used as the S3 configuration source.

3. **Absent condition**: if neither the explicit reference nor the platform-wide
   default Secret exists, Platform sets the condition `EtcdBackupDestinationAbsent`
   on the EtcdMaintenance CR with `status=True` and does not emit a RunnerConfig.
   The EtcdMaintenance CR remains in a pending state until a valid Secret is provided.
   Silent failure is never permitted - the condition must always be set and observable.

**Local PVC fallback (non-durable degraded mode):**
A local PVC fallback is permitted as a last-resort, non-durable mode only. If the
operator configuration explicitly enables PVC fallback, Platform sets the condition
`EtcdBackupLocalFallback` on the EtcdMaintenance CR with `status=True`. The CR status
must explicitly state: "Backup is non-durable - PVC-backed storage does not survive
node failure or cluster destruction." PVC fallback is not a substitute for S3. It is
a visible degraded mode, not a transparent default.

**S3 path structure within the bucket:**

    etcd-backup/{cluster-id}/

where `{cluster-id}` is the TalosCluster UID, not the name. UIDs are immutable and
globally unique across clusters. This ensures backup paths survive cluster rename and
remain unambiguous when multiple clusters write to the same bucket.

**Invariant boundary:**
Conductor and the etcd-backup Job receive the resolved Secret reference via RunnerConfig.
They perform no S3 destination resolution themselves. A Conductor execute-mode Job that
independently resolves an S3 destination is an invariant violation.

**S3 secret key contract (admin responsibility):**

The admin creates the S3 credentials Secret before any EtcdMaintenance CR is submitted.
The Secret may use either of two key naming conventions; both are accepted and normalized
by the Platform reconciler before the executor Job is created:

| Provider style | Key name | Normalized to |
|---|---|---|
| MinIO / Scality (camelCase) | `accessKeyID` | `AWS_ACCESS_KEY_ID` |
| MinIO / Scality (camelCase) | `secretAccessKey` | `AWS_SECRET_ACCESS_KEY` |
| MinIO / Scality (camelCase) | `region` | `S3_REGION` |
| MinIO / Scality (camelCase) | `endpoint` | `S3_ENDPOINT` (optional) |
| AWS SDK env var | `AWS_ACCESS_KEY_ID` | `AWS_ACCESS_KEY_ID` |
| AWS SDK env var | `AWS_SECRET_ACCESS_KEY` | `AWS_SECRET_ACCESS_KEY` |
| AWS SDK env var | `S3_REGION` | `S3_REGION` |
| AWS SDK env var | `S3_ENDPOINT` | `S3_ENDPOINT` (optional) |

`accessKeyID`, `secretAccessKey`, and `region` (or their AWS SDK equivalents) are
required. `endpoint` / `S3_ENDPOINT` is optional and must be omitted for native AWS S3.
If any required key is absent, reconcile halts with `EtcdBackupDestinationAbsent`.

**Cross-namespace secret projection:**

The source Secret may reside in `seam-system` while the executor Job runs in
`seam-tenant-{cluster}`. Kubernetes does not permit `envFrom` across namespaces.
The reconciler reads the source Secret, normalizes its keys to the canonical AWS SDK
env var names listed above, and writes a projected copy named `{em.Name}-s3-env`
into `em.Namespace`. The projected Secret carries an ownerReference to the
EtcdMaintenance CR and is garbage-collected automatically when the CR is deleted.
The executor Job mounts the projected Secret via `envFrom` so the Conductor binary
reads `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `S3_REGION`, and optionally
`S3_ENDPOINT` from its environment.

---

## 11. Cross-Domain Rules

Reads: security.ontai.dev/RBACProfile status (gate check).
Reads: infrastructure.ontai.dev/InfrastructureClusterPack (validate Cilium pack reference in InfrastructureTalosCluster).
Reads: infrastructure.ontai.dev/InfrastructurePackInstance (gate Cilium PackExecution on Ready).
Owns: cluster.x-k8s.io/Cluster and all CAPI child objects for target clusters.
Owns: SeamInfrastructureCluster, SeamInfrastructureMachine in tenant namespaces.
Creates: tenant namespaces - sole authority.
Never writes to security.ontai.dev or infrastructure.ontai.dev CRDs outside InfrastructureTalosCluster and InfrastructureRunnerConfig.

---

## 12. Conductor Deployment Contract

**LOCKED INVARIANT - Platform Governor directive 2026-04-05.**

Platform operator is responsible for deploying Conductor agent mode onto every tenant
cluster it forms, as part of cluster formation reconciliation. This responsibility is
exclusive - no other component deploys Conductor to tenant clusters.

**When Platform deploys Conductor to a tenant cluster:**
After TalosCluster formation reaches the readiness threshold and before marking the
cluster fully Ready, Platform's TalosClusterReconciler creates a Conductor agent
Deployment in ont-system on the target cluster. This Deployment is constructed using
the target cluster's kubeconfig mounted from the tenant's kubeconfig Secret.

**Role stamp requirement:**
The Conductor Deployment created by Platform for any tenant cluster **must** carry
`role=tenant` as a first-class field on the Deployment. This is not an annotation,
not an environment variable, and not a label. It is a named field.

Conductor reads this field at startup to determine which loops to activate. An absent
or incorrect role causes Conductor to exit with InvariantViolation. Platform is
solely responsible for correct role stamping. See conductor-schema.md §15.

**Invariants:**
- Platform creates exactly one Conductor Deployment per tenant cluster, in ont-system
  on that cluster, using the cluster's kubeconfig Secret.
- The Deployment is created with role=tenant. Any other value is a programming error.
- Platform does not deploy Conductor to the management cluster. `compiler enable`
  is the sole authority for the management cluster Conductor Deployment (role=management).
- If the Conductor Deployment is deleted from a tenant cluster's ont-system, Platform
  must recreate it on the next TalosClusterReconciler reconcile cycle.
- The Conductor image tag used must match the RunnerConfig.agentImage for this cluster.
  Platform reads agentImage from RunnerConfig before creating the Deployment.

**Import-mode cluster specifics:**
For clusters with `spec.mode: import`, Platform drives an additional two-site onboarding
sequence beyond the Conductor Deployment. The complete sequence is specified in
guardian-schema.md §20 and includes:

1. Create seam-tenant-{clusterName} namespace on the management cluster (CP-INV-004).
2. Store tenant kubeconfig Secret in seam-tenant-{clusterName}.
3. Create ont-system namespace on the tenant cluster.
4. Create conductor ServiceAccount in ont-system on the tenant cluster.
5. Create conductor Deployment (role=tenant) in ont-system on the tenant cluster.
6. Create conductor RBACProfile in ont-system on the tenant cluster (Seam operator
   profile, rbacPolicyRef: management-policy, permissionSetRef: management-maximum).
7. Observe PermissionSnapshotReceipt acknowledgement from the management conductor
   (written to InfrastructureTalosCluster.status.conductorHandshake).
8. Advance InfrastructureTalosCluster.status.phase to Operational on acknowledgement.

Platform sets InfrastructureTalosCluster.status.phase: ConductorPending when the
Deployment is created. Phase does not advance until the gRPC handshake completes.
See guardian-schema.md §20 for the full handshake protocol and PermissionSnapshot
delivery sequence.

---

## 13. PKI Rotation Contract

**PKI rotation automation -- session/17, 2026-05-02.**

Imported Talos clusters carry two sets of short-lived certificates stored in Secrets:
- Admin kubeconfig (Kubernetes client cert, ~1 year TTL): `seam-mc-{cluster}-kubeconfig` in `seam-tenant-{cluster}`, key `value`.
- Talosconfig client cert: `seam-mc-{cluster}-talosconfig` in `seam-tenant-{cluster}`, key `talosconfig`.

When these expire, the platform operator and Conductor executor lose the ability to connect to the cluster.

**Spec fields (InfrastructureTalosCluster, seam-core):**
- `spec.pkiRotationThresholdDays` (int32, default 30, minimum 1): days before cert expiry to auto-trigger PKI rotation.

**Status fields (InfrastructureTalosCluster, seam-core):**
- `status.pkiExpiryDate` (*metav1.Time): earliest certificate expiry across both Secrets. Written by TalosCluster reconciler.

**Triggers:**
1. Annotation `platform.ontai.dev/rotate-pki=true` on InfrastructureTalosCluster: on-demand rotation. The reconciler creates a PKIRotation CR with label `pki-trigger=manual` in `seam-tenant-{cluster}`, then clears the annotation via Patch.
2. Auto-rotation: when `status.pkiExpiryDate` is within `spec.pkiRotationThresholdDays` days of the current time, the reconciler creates a PKIRotation CR with label `pki-trigger=auto`. Idempotent: skips if a PKIRotation CR for this cluster already exists and is not yet complete or failed.

**Reconcile loop integration:**
PKI expiry check runs in Step F of `Reconcile()` only for stable-Ready clusters (clusters that had `Ready=True` before the current reconcile pass). Step F does NOT run during the first-pass Ready transition to avoid overriding the clean result returned by routing functions.

Stable-Ready clusters are requeued every 24 hours for daily expiry monitoring.

**Conductor execute-mode behavior (pkiRotateHandler):**
After the staged machine config apply succeeds, `pkiRotateHandler.Execute()` calls `TalosClient.Kubeconfig()` to generate a fresh kubeconfig and writes it to both `seam-mc-{cluster}-kubeconfig` and `target-cluster-kubeconfig` in `seam-tenant-{cluster}` via the dynamic client. Kubeconfig refresh is best-effort: if it fails, the operation result is still `Succeeded` because the staged config apply is the critical step. The failure is recorded in the step results with a note.

**Implementation files:**
- `platform/internal/controller/pki_cert_helpers.go`: cert expiry detection, Secret reading, PKIRotation CR creation.
- `conductor/internal/capability/platform_security.go`: `pkiRotateHandler.Execute()` with kubeconfig refresh.
- `conductor/internal/capability/clients.go`: `TalosNodeClient.Kubeconfig()` interface method.
- `conductor/internal/capability/adapters.go`: `TalosClientAdapter.Kubeconfig()` adapter.

---

*platform.ontai.dev schema - Platform*
*Amendments:*
*2026-03-30 - CAPI adopted for target cluster lifecycle. Seam Infrastructure Provider*
*  introduced. SeamInfrastructureMachine and SeamInfrastructureCluster CRDs added.*
*  TalosUpgrade, TalosKubeUpgrade, TalosStackUpgrade, TalosNodeScaleUp,*
*  TalosNodeDecommission, TalosReboot replaced by CAPI equivalents.*
*  Kueue scoped to Wrapper pack-deploy Jobs only.*
*  TalosNoMaintenance integrated with CAPI pause mechanism.*
*  Cilium CNI integration documented. CiliumPending condition added to TalosCluster.*
*  Management cluster bootstrap unchanged - CAPI not applicable.*

*2026-03-30 - Section 6 retitled "CRDs Delegated to CAPI for Target Clusters"*
*  (Path B ruling). Six lifecycle CRDs retained with dual-path semantics:*
*  CAPI-native for spec.capi.enabled=true (target clusters), direct conductor(mode: execute) Job via*
*  OperationalJobReconciler for spec.capi.enabled=false (management cluster).*
*  Named conductor capability references restored for all six entries.*

*2026-04-03 - Operator rename: Platform (formerly platform), Guardian (formerly*
*  guardian), Wrapper (formerly wrapper), Conductor [Compiler, Conductor (formerly conductor).*]
*  CAPI infrastructure CRDs renamed: SeamInfrastructureCluster (formerly*
*  SeamInfrastructureCluster), SeamInfrastructureMachine (formerly*
*  SeamInfrastructureMachine). API group infrastructure.cluster.x-k8s.io unchanged.*
*  TalosControlPlane and TalosWorkerConfig added as dual-mode CRDs with explicit*
*  compile-time and runtime semantics documented. Sixteen day-two CRDs consolidated*
*  into eight: EtcdMaintenance, NodeMaintenance, PKIRotation, ClusterReset,*
*  HardeningProfile, UpgradePolicy, NodeOperation, ClusterMaintenance. LicenseKey*
*  removed - Seam is fully open source with no licensing tier.*

*2026-04-05 - Section 9 "MachineConfig Storage Contract" added: locked invariant.*
*  Platform is sole owner of machineconfig Secrets for native/imported clusters.*
*  Naming convention seam-mc-{cluster-name}-{node-name} in seam-tenant-{cluster-name}.*
*  Mirrors CAPI bootstrap provider pattern. No other component may modify these Secrets.*
*  Section 10 "Etcd Backup Destination Contract" added: locked invariant.*
*  S3 resolution hierarchy: explicit TalosCluster ref → seam-etcd-backup-config in*
*  seam-system → EtcdBackupDestinationAbsent condition (no RunnerConfig emitted).*
*  Local PVC fallback permitted only as visible degraded mode (EtcdBackupLocalFallback*
*  condition, non-durable status explicit). S3 path: etcd-backup/{cluster-uid}/.*
*  Conductor never performs S3 destination resolution. Section 11 renumbered from 9.*

*2026-04-05 - Section 12 "Conductor Deployment Contract" added: locked invariant.*
*  Platform operator is exclusively responsible for deploying Conductor agent mode*
*  onto every tenant cluster it forms. Deployment created in ont-system on target*
*  cluster using cluster's kubeconfig Secret. role=tenant must be stamped as a*
*  first-class field. Absent/incorrect role causes InvariantViolation exit in Conductor.*
*  Platform does not deploy Conductor to the management cluster - compiler enable owns*
*  that Deployment (role=management). Platform must recreate Deployment on deletion.*
*  Conductor image tag must match RunnerConfig.agentImage for the cluster.*

*2026-04-26 - Section 12 extended: import-mode cluster specifics added. For*
*  spec.mode=import clusters, Platform drives a two-site onboarding sequence including*
*  namespace creation on both clusters, conductor RBACProfile creation in ont-system*
*  (Seam operator profile referencing management-policy/management-maximum), and*
*  phase advancement on PermissionSnapshotReceipt acknowledgement. Full sequence*
*  specified in guardian-schema.md §20.*

*2026-04-26 - Section 9 corrected: mode-specific machineconfig provisioning contract*
*  added. mode=import: Platform captures machineconfigs from running cluster via Talos*
*  COSI API after kubeconfig generation (PLATFORM-BL-MACHINECONFIG-IMPORT-CAPTURE).*
*  mode=bootstrap: Platform generates machineconfigs from InfrastructureTalosCluster*
*  spec (pending schema amendment PLATFORM-BL-HARDENINGPROFILE-MERGE for node topology).*
*  Namespace authority corrected: CP-INV-004 applies to bootstrap/CAPI modes.*
*  For mode=import, Compiler bootstrap bundle includes seam-tenant-namespace.yaml so*
*  the admin can apply Secrets and TalosCluster CR in a single kubectl apply run.*
*  ensureTenantNamespace in the import reconcile path is idempotent safety net only.*

*2026-05-02 - Section 10 extended: S3 secret key contract and cross-namespace projection*
*  added. Admin creates seam-etcd-backup-config in seam-system before submitting any*
*  EtcdMaintenance CR. Both provider key conventions accepted: MinIO/Scality camelCase*
*  (accessKeyID, secretAccessKey, region, endpoint) and AWS SDK env var names*
*  (AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, S3_REGION, S3_ENDPOINT). Reconciler*
*  normalizes to AWS SDK env var form and writes a projected secret {em.Name}-s3-env*
*  in em.Namespace owned by the EtcdMaintenance CR. Executor Job mounts via envFrom.*
*  s3_env_secret.go added to platform/internal/controller.*
