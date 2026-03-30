# Development Standard

> Architecture, patterns, and constraints only.
> Amended: 2026-03-30 — CAPI adopted for target cluster lifecycle.
>   ONT Infrastructure Provider introduced. ONT InfrastructureMachine pattern.
>   Kueue scoped to ont-infra. Operational runner Jobs retained where CAPI has no equivalent.

---

## 1. What Changed and Why

Previous versions of this document described ont-platform as submitting Kueue Jobs
for all cluster lifecycle operations. This is replaced by CAPI for target clusters.

CAPI provides battle-tested cluster lifecycle reconciliation. The ONT Infrastructure
Provider is the thin layer that makes CAPI work with Talos nodes in maintenance
mode on port 50000. ONT's unique value — security governance, signed pack delivery,
operational audit CRDs — layers on top of CAPI primitives rather than reimplementing
what CAPI already does well.

The governing principle is preserved: every operation is declarative, versioned,
and auditable. CAPI delivers this through its own status conditions and event model.
Kueue Job-based auditing for cluster lifecycle is no longer needed.

Kueue remains for ont-infra pack-deploy Jobs only. Operational runner Jobs
(etcd-backup, etcd-maintenance, pki-rotate, etcd-restore, hardening-apply,
node-patch, credential-rotate, cluster-reset) use direct Job submission without
Kueue admission control — they are infrequent, targeted, and operator-gated.

---

## 2. Controller Structure

### 2.1 ont-platform Controllers

**TalosClusterReconciler** — the primary reconciler. Watches TalosCluster CRs.
For management cluster (capi.enabled=false): follows the direct bootstrap path
unchanged from previous design. For target clusters (capi.enabled=true): creates
and owns all CAPI objects. Watches CAPI Cluster status and transitions
TalosCluster status accordingly. Handles Cilium ClusterPack deployment trigger
when cluster reaches Running state.

**ONTInfrastructureClusterReconciler** — implements the CAPI InfrastructureCluster
contract. Sets status.ready=true when all control plane ONTInfrastructureMachine
objects are ready. Writes controlPlaneEndpoint back to the CAPI Cluster object.

**ONTInfrastructureMachineReconciler** — implements the CAPI InfrastructureMachine
contract. This is the Talos-specific infrastructure layer. It watches for Machine
objects that reference an ONTInfrastructureMachine, reads the CABPT-rendered
machineconfig bootstrap data from the Machine's bootstrap data secret, calls
talos goclient ApplyConfiguration against the declared node IP on port 50000,
and sets status.ready=true with the providerID when the node transitions out of
maintenance mode.

**OperationalJobReconciler** — a shared reconciler base covering thirteen CRDs:
the seven operational CRDs with no CAPI equivalent (TalosBackup, TalosEtcdMaintenance,
TalosPKIRotation, TalosRecovery, TalosHardeningApply, TalosNodePatch,
TalosCredentialRotation) and the six lifecycle CRDs that retain a direct runner Job
path for capi.enabled=false clusters (TalosUpgrade, TalosKubeUpgrade, TalosStackUpgrade,
TalosNodeScaleUp, TalosNodeDecommission, TalosReboot). TalosClusterReset is handled
by TalosClusterResetReconciler which extends this base with the human approval gate.

Each of the thirteen CRDs has its own reconciler that embeds this base. The base
handles Job creation, status watching, and OperationResult reading.

Base routing rule: before submitting any runner Job, the base reads the owning
TalosCluster spec.capi.enabled field. If true, the reconciler sets a CAPIDelegated
condition on the operational CR and returns without submitting any Job — CAPI handles
this operation natively for target clusters. If false, the reconciler proceeds with
direct runner Job submission as normal.

**TalosClusterResetReconciler** — extends OperationalJobReconciler with the human
approval gate. Holds at PendingApproval. When approved: deletes the CAPI Cluster
object first (which triggers graceful machine deprovisioning through the
ONT Infrastructure Provider), then submits the cluster-reset runner Job.

**TalosNoMaintenanceReconciler** — watches TalosNoMaintenance CRs and reconciles
the CAPI Cluster pause state. When no active window and blockOutsideWindows=true:
sets cluster.x-k8s.io/paused=true on the CAPI Cluster. When window opens: removes
the pause annotation.

**TenantReconcilers** — PlatformTenantReconciler, ClusterAssignmentReconciler,
QueueProfileReconciler, LicenseKeyReconciler. These are unchanged from previous
design. ClusterAssignmentReconciler now additionally watches CAPI Cluster
status.phase before triggering the Cilium ClusterPack PackExecution via
bootstrapFlag.

Leader election: implemented at the manager level. Lease name: ont-platform-leader.
Lease namespace: platform-system.

---

## 3. ONT Infrastructure Provider Implementation

The ONT Infrastructure Provider consists of the two reconcilers described in
Section 2.1 above plus the talos goclient dependency. The provider binary is
distroless — it requires only talos goclient and kube goclient. No helm, no
kustomize, no shell.

### 3.1 ONTInfrastructureMachineReconciler Detail

The reconciliation loop for each ONTInfrastructureMachine:

Step 1 — Read the owning CAPI Machine object. If Machine has no bootstrap data
secret reference yet (CABPT has not rendered the machineconfig), requeue and wait.
CABPT renders the machineconfig into a Secret in the tenant namespace. The Machine
object's spec.bootstrap.dataSecretName is set by CAPI core when CABPT is ready.

Step 2 — Read the bootstrap data Secret. Extract the machineconfig YAML. This is
the CABPT-rendered machineconfig including the CNI=none patch and any
TalosHardeningProfile patches merged in by the TalosClusterReconciler when it
created the TalosConfigTemplate.

Step 3 — Call talos goclient ApplyConfiguration against spec.address:spec.port
(default port 50000) with the machineconfig bytes. Use the talosconfig secret from
ont-system for authentication context.

Step 4 — Poll the Talos API on the node until it transitions out of maintenance
mode. When the node is reachable via its normal Talos API endpoint (not port 50000),
set status.machineConfigApplied=true.

Step 5 — Set status.providerID=talos://{cluster-name}/{node-ip}. Write providerID
back to the owning Machine object's spec.providerID. This satisfies the CAPI
InfrastructureMachine contract and causes CAPI core to begin treating the machine
as provisioned.

Step 6 — Set status.ready=true. CAPI core now considers this Machine as
infrastructure-ready and proceeds with its own bootstrap sequence.

Idempotency: if status.machineConfigApplied=true, skip steps 2-4 and proceed
directly to verifying the providerID and ready status. ApplyConfiguration is
idempotent for Talos but skipping it when already applied avoids unnecessary
API calls.

### 3.2 TalosConfigTemplate Construction

When TalosClusterReconciler creates the TalosConfigTemplate for CABPT, it merges:
- Base machineconfig with cluster.network.cni.name: none
- Cilium-required BPF kernel parameters
- Any TalosHardeningProfile patches referenced in TalosCluster.spec
- The cluster endpoint from ONTInfrastructureCluster.spec.controlPlaneEndpoint
- The Talos installer image derived from TalosCluster.spec.capi.talosVersion

CABPT reads this template and renders a per-Machine machineconfig that the ONT
Infrastructure Provider then delivers.

---

## 4. Cilium Deployment Integration

Cilium deployment is the bridge between CAPI (cluster exists, nodes Running) and
ont-infra (Cilium pack deployed, nodes Ready).

The TalosClusterReconciler watches the CAPI Cluster object's status.phase. When
phase transitions to Running, the reconciler sets the CiliumPending condition on
TalosCluster and signals the ClusterAssignment to trigger its bootstrapFlag
PackExecution for the Cilium ClusterPack.

The ClusterAssignmentReconciler creates a PackExecution for the Cilium ClusterPack
when both conditions are met: CAPI Cluster phase=Running AND bootstrapFlag=true.

The TalosClusterReconciler watches PackInstance status for the Cilium pack. When
PackInstance reaches Ready (nodes have transitioned to Ready, Cilium is up), the
CiliumPending condition is cleared and TalosCluster transitions to Ready.

The Cilium ClusterPack must be pre-compiled on the workstation before the cluster
is bootstrapped. It is cluster-specific because it contains the cluster endpoint
address and L2 announcement configuration. The pack reference is declared in
TalosCluster.spec.capi.ciliumPackRef before GitOps apply.

---

## 5. Management Cluster Bootstrap Path (Unchanged)

The management cluster bootstrap path does not use CAPI. It is executed by
ont-runner directly, either via local invocation or as a bootstrap Job.

When TalosCluster.spec.capi.enabled=false:
1. TalosClusterReconciler reads the bootstrap secrets from ont-system.
2. Submits a bootstrap runner Job directly (not via Kueue).
3. Runner pushes machineconfigs to seed nodes on port 50000.
4. Runner waits for Kubernetes API, extracts kubeconfig.
5. OperationResult is written, controller sets TalosCluster status to Ready.

The enable phase then installs all prerequisites and operators including CAPI.
From this point, CAPI is operational on the management cluster for target clusters.

---

## 6. Operational Job Pattern

For CRDs that still use runner Jobs (TalosBackup, TalosEtcdMaintenance, etc.),
the reconciler pattern follows this sequence without Kueue:

Watch CR → read cluster credentials from ont-system secrets → build Job spec →
submit Job directly to the cluster's default namespace → watch Job completion →
read OperationResult ConfigMap → update CR status → clean up Job and ConfigMap.

These Jobs run in platform-system on the management cluster. They mount the
target cluster's kubeconfig and talosconfig secrets from ont-system. They use
the ont-runner executor image (debian-slim) because some operational capabilities
require talos goclient.

The Job TTL is 600 seconds. The reconciler reads the OperationResult before expiry.
Gate failures are permanent — INV-018 applies unchanged.

The TalosClusterReset Job additionally deletes the CAPI Cluster object before
submitting the reset Job. The sequence:
1. Reconciler verifies ontai.dev/reset-approved=true annotation.
2. Deletes the CAPI Cluster object in the tenant namespace.
3. Waits for all CAPI Machine objects to reach Deleted phase (ONT Infrastructure
   Provider handles clean machine deprovisioning during CAPI deletion).
4. Submits cluster-reset runner Job.
5. Runner calls Talos reset API on each node.
6. Controller deletes tenant namespace after Job completion.
7. TalosCluster status in ont-system updated to ResetComplete (never deleted).

---

## 7. Namespace Management (Unchanged)

ont-platform creates tenant namespaces synchronously before creating any CAPI or
ONT resources in them. Namespace creation uses server-side apply with labels
ontai.dev/tenant={customerID} and ontai.dev/cluster={cluster-name}.

These labels are the authoritative namespace identity markers for all other
operators and agents.

---

## 8. CAPI RBAC Intake Protocol

All CAPI components (core, CABPT, CACPPT) install ClusterRoles, ClusterRoleBindings,
ServiceAccounts, and webhook configurations. All of these must pass through
ont-security's third-party RBAC intake protocol before any CAPI controller starts.

The enable phase order is enforced by OperatorManifest installOrder fields.
ont-security is installOrder 1. CAPI core is installOrder 5. CABPT is installOrder 6.
No CAPI controller starts until ont-security's admission webhook is operational and
each provider's RBACProfile has reached provisioned=true.

The ont-runner enable phase splits each provider's Helm chart output into RBAC
resources and workload resources. RBAC resources go through ont-security intake.
Workload resources apply after RBACProfile provisioned=true is confirmed.

---

## 9. Testing Standard

Unit tests: TalosClusterReconciler CAPI object creation and ownership, ONT
InfrastructureMachineReconciler machineconfig delivery logic with mock talos
goclient, TalosNoMaintenanceReconciler pause/unpause behavior, Cilium deployment
trigger logic.

Integration tests: full CAPI cluster lifecycle against envtest with mock
InfrastructureMachine provider. Verify pause mechanism blocks Machine reconciliation.
Verify Cilium deployment gate. Verify CAPI Cluster deletion triggers machine
deprovisioning sequence in correct order.

e2e tests: full bootstrap of ccs-test using CAPI path. Gate: all Machines in
Running phase, Cilium PackInstance Ready, all nodes Ready, ClusterAssignment
status.conditions.Ready=true. Bootstrap-to-Ready time recorded in test report.

Regression: management cluster bootstrap path (capi.enabled=false) must pass all
existing bootstrap e2e tests unchanged.

---

*ont-platform development standard*
*Amendments:*
*2026-03-30 — CAPI adopted for target cluster lifecycle. Controller structure*
*  revised: TalosClusterReconciler, ONTInfrastructureCluster/MachineReconciler,*
*  OperationalJobReconciler base, TalosNoMaintenanceReconciler. Kueue removed*
*  from cluster lifecycle operations. Cilium deployment integration documented.*
*  Management cluster bootstrap path confirmed unchanged. CAPI RBAC intake*
*  protocol added. TalosClusterReset updated for CAPI object deletion sequence.*
*2026-03-30 — OperationalJobReconciler scope expanded from seven to thirteen CRDs.*
*  Six lifecycle CRDs added: TalosUpgrade, TalosKubeUpgrade, TalosStackUpgrade,*
*  TalosNodeScaleUp, TalosNodeDecommission, TalosReboot. Base routing rule added:*
*  read owning TalosCluster spec.capi.enabled before Job submission. If true:*
*  set CAPIDelegated condition and return. If false: proceed with runner Job.*
*  TalosClusterReset clarified as extension of base, not in base CRD list.*
