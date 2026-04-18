# Contributing to platform

Thank you for your interest in contributing to the Seam platform.

---

## Before you begin

Read the Seam Platform Constitution (`CLAUDE.md` in the ontai root repository)
and the platform component constitution (`CLAUDE.md` in this repository) before
opening a Pull Request. All contributions must respect the platform invariants
defined in those documents.

Key invariants for this repository:

- The talos goclient is restricted exclusively to `SeamInfrastructureClusterReconciler`
  and `SeamInfrastructureMachineReconciler`. Any other file importing the talos
  goclient is an invariant violation of CP-INV-001.
- `TalosCluster` deletion never triggers cluster destruction. `TalosClusterReset`
  is the only destruction path and requires the `ontai.dev/reset-approved=true` annotation.
- Kueue is not used for any operation in platform.
- `TalosConfigTemplate` objects must always include `cluster.network.cni.name: none`
  and the Cilium-required BPF kernel parameters.
- Platform installs after guardian. Its enable phase installOrder enforces this.

---

## Development setup

```sh
git clone https://github.com/ontai-dev/platform
cd platform
go build ./...
go test ./test/unit/...
```

Integration tests require a running management cluster with CAPI and the Seam
Infrastructure Provider configured. Unit tests run without a cluster.

---

## Three-category routing

Before implementing any reconciler, determine which category the target CRD belongs to:

1. **CAPI-managed lifecycle** (`TalosCluster`, `SeamInfrastructureCluster`,
   `SeamInfrastructureMachine`): interact with CAPI objects, no RunnerConfig.
2. **Operational runner Job CRDs** (TalosBackup, TalosEtcdMaintenance, etc.):
   verify the named capability exists in `conductor-schema.md` before implementing
   Job submission.
3. **Tenant coordination** (`UpgradePolicy`, `HardeningProfile`, `MaintenanceBundle`):
   no runner capability, no CAPI objects, pure record-keeping.

---

## Schema changes

All changes to CRD types in `api/` must be accompanied by a `docs/platform-schema.md`
update in the same Pull Request. Schema amendments require Platform Governor approval.

---

## Pull Request checklist

- [ ] `go build ./...` passes with no errors
- [ ] `go test ./test/unit/...` passes
- [ ] No em dashes in any new documentation
- [ ] No shell scripts added (Go only, per INV-001)
- [ ] Talos goclient access bounded to the two named reconcilers
- [ ] `docs/platform-schema.md` updated if CRD types changed

---

## Reporting issues

Open an issue at: https://github.com/ontai-dev/platform/issues

For security vulnerabilities, contact the maintainers directly rather than
opening a public issue.

---

## License

By contributing, you agree that your contributions will be licensed under the
Apache License, Version 2.0. See `LICENSE` for the full text.

---

*platform - Seam Platform Operator*
