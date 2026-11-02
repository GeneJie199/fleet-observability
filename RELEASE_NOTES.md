# fleet-observability v2.0.0-rc.2 Community

FleetScope applies the shared Suite design to the existing monitoring, metrics, events, databases, alerts, changes, topology, evidence, notifications, coverage, rules and data-source views. Center/Agent telemetry and incident contracts remain unchanged.

## Changes in this candidate

- Shared Light / Dark / System appearance, responsive sidebar/mobile menu and consistent cards, forms and dialogs. The mobile credential-management entry stays visible as an accessible compact control.
- Resource bars, metric series and evidence-confidence visuals use actual loaded values with SVG geometry and external theme classes. The strict Content Security Policy remains in place; empty and failed queries remain visible.
- Theme CSS and JavaScript are embedded in the product binary and served locally with the other static assets. The redesigned appearance requires no external CDN and supports independent product use.
- Read-only `GET /api/v1/diagnostics/runtime` exposes seven aggregate process counters: goroutines, heap allocation, heap system/in-use bytes, total runtime system bytes, GC count and uptime. It exposes no profiles, configuration, environment, process arguments or credentials.
- Diagnostics retain the local Host/Origin checks and existing optional protected-read authentication. The UI and diagnostics add no credential, acknowledgement, remediation or production-operation authority. Cross-module links retain the current Suite hosts, ports and theme.

## Installation and independent use

Use this component's [standalone CLI quick start](https://github.com/GeneJie199/fleet-observability/blob/main/README.md#quick-start) and [operations guide](https://github.com/GeneJie199/fleet-observability/blob/main/docs/operations.md). The component archive contains its binary, README, installation scripts, operations documents and license material.

For the combined Suite, follow the [online quick start](https://github.com/GeneJie199/ai-devops-open-suite/blob/main/docs/quickstart.md) or the [full offline installation guide](https://github.com/GeneJie199/ai-devops-open-suite/blob/main/docs/offline-install.md). Those Suite documents are linked online rather than assumed to exist inside a standalone component archive. Pin the requested candidate version, verify published checksums and use an isolated evaluation prefix. Back up configuration and state before an upgrade or rollback.

The delivery interface is the CLI binary/archive and vendored Corresponding Source. Historical Go module paths do not include `/v2`; this candidate makes no `go install ...@v2` compatibility promise. Native macOS CLI checks are separate from complete Suite offline installation, whose supported package scope is Windows/Linux amd64.

## Candidate and safety scope

This is an RC, not GA. These changes do not assert completed final 136/168 functional acceptance, uninterrupted 24-hour stability, or live GPT/Kimi/DeepSeek three-model collaboration. Those require their own version-bound evidence. Synthetic/demo visual fixtures do not prove live fleet health, an alert recovery or a successful AI analysis.

The Suite generic CLI runtime remains for trusted single-user local environments. Worktrees and role path checks do not provide an OS sandbox for untrusted users. Enforcing isolation policies fail closed, R4 remains blocked, and a ReleaseGuard GO grants no production deployment permission. No model credentials, subscription/API entitlement, automatic purchase, HA or untrusted multi-tenancy are added here. Optional AI analysis retains its existing explicit preview/confirmation and credential boundaries.

New v2 source is AGPL-3.0-only; historical v1.x keeps its original Apache-2.0 terms. Archives retain full license texts, third-party notices, inventory and SBOM. Corresponding Source is distributed separately with vendored Go dependencies and checksums. Commercial alternatives remain subject to verified rights and a separate agreement; no signed CLA, copyright transfer or third-party relicensing right is asserted. Published target binaries must pass the configured reachable-vulnerability scans. See SECURITY.md for reporting.
