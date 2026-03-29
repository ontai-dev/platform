# Development Standard

> This document defines the development standard for ont-platform.
> Every agent reads this before beginning implementation work.

---

## 1. Operator Pattern Contract

Every reconciler in ont-platform follows this exact pattern. Deviation requires
an Architecture Decision Record approved by the Platform Governor.

Watch → GenerateRunnerConfig (shared library) → ConfirmCapability →
BuildJobSpec → SubmitToKueue → WatchJob → ReadOperationResult → UpdateCRStatus

No step may be skipped. No additional steps may be inserted without an ADR.

---

## 2. Controller Structure

One controller per CRD. Each controller file is named {crd-name}_controller.go.
Each controller implements exactly one reconciler struct with one Reconcile method.
No shared reconciliation logic between controllers except through the shared
runner library.

Leader election is implemented at the manager level using controller-runtime's
built-in lease-based election. Lease name: ont-platform-leader. Lease namespace:
platform-system.

---

## 3. RunnerConfig Generation

The operator generates RunnerConfig using the shared runner library imported from
ont-runner. The library function GenerateFromTalosCluster(spec) produces the
RunnerConfig spec. The controller applies it using server-side apply. The
controller never hand-constructs RunnerConfig fields.

RunnerConfig is the source of truth for which runner image to stamp into Job specs.
The controller reads runnerImage from RunnerConfig spec — never from a hardcoded
constant or a controller flag.

---

## 4. Job Spec Construction

Every Job spec includes:
- Image: from RunnerConfig.spec.runnerImage
- Env: CAPABILITY={named-capability}, CLUSTER_REF={cluster-name},
  OPERATION_RESULT_CM={configmap-name}
- Volumes: secrets mounted read-only from ont-system (talosconfig, kubeconfig)
- ServiceAccount: platform-system/ont-platform-runner with minimum permissions
- TTL: 600 seconds post-completion (OperationResult must be read before then)
- Kueue labels: kueue.x-k8s.io/queue-name from RunnerConfig QueueProfile reference

No Job is submitted without first confirming the capability exists in
RunnerConfig.status.capabilities. If absent, the reconciler sets
CapabilityUnavailable on the operational CR and requeues with exponential backoff.

---

## 5. OperationResult Reading

After Job completion, the controller reads the OperationResult ConfigMap using
kube goclient. It parses the structured JSON. On success: advance CR status to
Succeeded, record artifacts in status. On failure: advance to Failed, record
failureReason in status message, do not requeue automatically — require human
intervention via CR deletion and recreation.

Gate failures are permanent. INV-018. The controller never auto-retries a failed
operation. Every retry is a deliberate human action.

---

## 6. Namespace Management

The ont-platform controller creates tenant namespaces synchronously before
projecting any resources into them. Namespace creation uses server-side apply
with labels ontai.dev/tenant={customerID} and ontai.dev/cluster={cluster-name}.

The controller watches for namespace deletion events on tenant namespaces and
raises a Degraded condition on the corresponding TalosCluster if deletion is
detected without a corresponding TalosClusterReset completion.

---

## 7. Deletion Handling

Deletion of any platform.ontai.dev CR emits a Kubernetes event and updates
status to Terminating. No Job is submitted on the delete path. INV-006.

Finalizer removal order for TalosCluster teardown:
ClusterAssignment finalizer removed first → PackInstance deletion confirmed →
RBACProfile deletion confirmed → TalosCluster finalizer removed.

The controller monitors these conditions but does not submit Jobs to effect them.
Destructive operations are only possible through TalosClusterReset with human gate.

---

## 8. TalosClusterReset Human Gate

The TalosClusterReset reconciler enters a hold loop on PendingApproval status
immediately on CR creation. It requeues every 30 seconds checking for the
ontai.dev/reset-approved=true annotation. It does not advance until the annotation
is present. The annotation is set only by the runner in compile mode after
interactive cluster name confirmation — not by the controller itself and not by
any automated process.

---

## 9. Version Compatibility Gate

Before submitting any Talos upgrade Job, the controller verifies:
- RunnerConfig.spec.runnerImage tag Talos version component matches
  TalosCluster.spec.talosVersion.
- If mismatch: set VersionMismatch condition on TalosUpgrade CR. Do not submit Job.
  The RunnerConfig must be updated to a compatible runner version first.

This implements INV-012.

---

## 10. Testing Standard

Unit tests: every reconciler function has unit test coverage using envtest.
Integration tests: every named capability has an integration test that submits
a mock Job and verifies OperationResult handling.
e2e tests: full bootstrap of a test cluster using ccs-test. Gate: 5/5 nodes
healthy, all operators running, ClusterAssignment in Ready state.

---

*ont-platform development standard*
*Amendments appended below with date and rationale.*