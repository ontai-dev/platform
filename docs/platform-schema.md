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

**Lifecycle:**
Platform generates these Secrets at bootstrap time from the TalosCluster and
TalosControlPlane/TalosWorkerConfig specs. When node configuration changes - for
example when a HardeningProfile is updated or a machineconfig patch is applied -
Platform regenerates and reconciles the affected Secrets.

**Design rationale:**
This mirrors the CAPI bootstrap provider secret pattern intentionally. The CAPI path
stores machineconfigs as bootstrap data Secrets managed by CABPT. The native path
stores them as Seam-named Secrets managed by Platform. The operational model is
consistent regardless of provisioning path: a named Secret per node holds the node's
current authoritative machineconfig.

No other operator or Conductor capability handler generates or owns these Secrets.
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
