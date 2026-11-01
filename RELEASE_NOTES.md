# fleet-observability v2.0.0-rc.2 Community

C1 hardens the local browser Host/Origin boundary without changing telemetry or incident contracts.

This is rc.2 preparation, not a claim that final onboarding, dual-host or 24-hour
acceptance has already passed. C1 measurements remain historical C1 evidence.
Final publication requires rc.2 exact-source CI and release preflight.

The delivery interface is the CLI binary/archive and vendored Corresponding
Source offline build. Historical Go module import paths do not include /v2;
this candidate makes no Go SDK `go install ...@v2` compatibility promise.
Native macOS CLI verification differs from the Suite full offline installation
scope, which is Windows/Linux amd64. Cross-compilation alone is not installation
acceptance. No nine-live-provider or live Kimi/DeepSeek billing claim is made.

## Continuing safety, installation and license boundaries

This is a release candidate for trusted single-user local environments. Generic
CLI adapters execute with the host user's permissions; worktrees and role path
checks are not an OS sandbox. Enforcing isolation policies fail closed. R4 is
blocked. ReleaseGuard GO grants no deployment permission. Production deployment,
untrusted multi-tenancy, billing, HA and marketplace integrations are not included.
Unmetered CLI usage is unknown, not an actual invoice; strict monetary/token
budgets reject unmetered adapters. Model access requires the user's own local CLI
configuration and source-export consent; no credentials are bundled.

Install using the Suite offline archive for the verified platform, verify its
SHA-256 against checksums.txt, extract it and follow docs/offline-install.md.
Use an isolated installation prefix for evaluation. Existing v1 installations
must back up configuration and data before upgrade; use the Suite installer with
the new manifest. Run doctor and restart after upgrade. Suite rollback restores
the saved binaries/configuration; DevCycle retains original database schema 3
and keeps new Team state in a separate sidecar. Preserve the sidecar across a
rollback and do not let old binaries manage Team state.

New six-repository source is AGPL-3.0-only. Historical v1.x remains under its
original Apache-2.0 terms. Archives include license texts, third-party notices,
SBOM and a separately checksummed Corresponding Source archive with vendored Go
dependencies. Commercial use under AGPL is allowed when its terms are met.
Commercial alternative terms remain subject to verified rights and a separate
agreement; no signed CLA, copyright transfer or third-party relicensing right
is asserted. The contributor policy is not evidence of a signed CLA.

Platform support and precise source revisions are recorded in the Suite release
manifest and final acceptance report. Cross-compilation alone is not native
installation verification. Security reports: see SECURITY.md.

Release security: affected products select edwards25519 v1.1.1 (GO-2026-4503 fix). Every published target binary must pass govulncheck binary scanning before upload.
