# fleet-observability v2.0.0-rc.3 Community

FleetScope rc.3 reduces the Center's memory use when a node report crosses the history retention limit.

## Changes in this candidate

- The Center retains the offsets and lengths of records selected for compaction, then streams those records into the replacement file. It preserves the configured limits, record order, newline handling and atomic replacement.
- In an isolated Linux amd64 comparison with 2,001 synthetic reports totaling 46.6 MB, cumulative Go allocation fell from 154.3 MB to 0.45 MB per compaction. Peak memory for the test cgroup, including file cache, fell from 254.5 MiB to 102.4 MiB. The measurements apply to this synthetic workload.

## Included from rc.2

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

GA qualification requires functional evidence from both hosts and an uninterrupted 24-hour observation after this candidate is deployed. Live GPT/Kimi/DeepSeek collaboration has a separate acceptance scope.

The Suite generic CLI runtime remains for trusted single-user local environments. Worktrees and role path checks do not provide an OS sandbox for untrusted users. Enforcing isolation policies fail closed, R4 remains blocked, and a ReleaseGuard GO grants no production deployment permission. No model credentials, subscription/API entitlement, automatic purchase, HA or untrusted multi-tenancy are added here. Optional AI analysis retains its existing explicit preview/confirmation and credential boundaries.

New v2 source is AGPL-3.0-only; historical v1.x keeps its original Apache-2.0 terms. Archives retain full license texts, third-party notices and the Suite-wide license inventory and inventory SBOM. The release workflow also generates a FleetScope Go module graph SBOM. Corresponding Source is distributed separately with vendored Go dependencies and checksums. Commercial alternatives remain subject to verified rights and a separate agreement; no signed CLA, copyright transfer or third-party relicensing right is asserted. Published target binaries must pass the configured reachable-vulnerability scans. See SECURITY.md for reporting.
