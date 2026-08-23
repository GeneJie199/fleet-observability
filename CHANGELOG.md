# Changelog

## 1.0.0 - 2026-08-23

- Deliver the native Agent, collector, telemetry/event stores, fleet-first triage, recommendations, resource groups, topology history/diff, Change Center, alerting, delivery controls, and optional evidence-based AI.
- Remove any runtime dependence on Prometheus or another collector while preserving Prometheus, Influx, and OTLP as optional compatibility inputs.
- Enforce connect-time SSRF and header policy, compact telemetry memory, retain rolling latest bounded data, and publish a measured 100-node/3-day scale baseline.
- Add ownership-safe checksum-verifying installers, complete platform archives plus stable suite binaries, fuzz/security gates, CycloneDX SBOM, and GitHub provenance.
- Include and build-verify a platform-local binary checksum inside every standalone archive.

## 0.4.0 - 2026-08-23

- Add `fleetctl up` for a loopback Center and self-enrolling local Agent with generated persistent credentials, native discovery, optional InfraScout integration, and automatic console opening.
- Add stable native port/process discovery and safe monitoring recommendations without collecting command lines, environment values, business rows, or inline credentials.
- Add immutable topology snapshots, structural diffs, release/incident context, group-scoped history, and append-only relationship review audit.
- Add persistent multi-channel notification delivery for Webhook, Feishu, WeCom, DingTalk, Slack, Telegram, and SMTP with grouping, deduplication, suppression, deployment windows, bounded retries, and manual replay.
- Add privacy-scoped evidence packages, explicit AI payload previews, secret redaction, structured assumptions, completeness confidence, and opt-in OpenAI-compatible, Anthropic, Gemini, or custom explanation providers.
- Add automatic-discovery, delivery, topology-history, and evidence-analysis console workflows with offline Lucide icons and responsive desktop/mobile layouts.
- Add `fleetctl doctor`, Linux systemd and Windows user installers, complete release archives, cross-site mutation protection, and security regression tests.

## 0.3.2 - 2026-08-13

- Add first-run, empty-resource-group, and all-Agents-stale recovery panels with direct actions for data ingestion, credentials, group management, and refresh.
- Add domain symbols to the operational navigation, a compact refresh command, and consistent visual product marks to the suite switcher.
- Preserve expanded alert evidence across refreshes and defer background refresh while an operator is choosing or confirming a change action.

## 0.3.1 - 2026-08-12

- Treat a single high CPU sample as a warning while retaining critical health for memory or disk exhaustion, preventing transient collection spikes from incorrectly blocking release observation windows.

## 0.3.0 - 2026-08-12

- Replace external collector assumptions with the built-in FleetScope Agent, node identity, bounded disk spools, sequence deduplication, and direct Center ingestion.
- Add segmented metric and event stores with retention/capacity limits, catalog and range queries, step aggregation, and native dashboard exploration.
- Add native Linux host, process, Docker, Nginx, Redis, PostgreSQL, MySQL, probe, and file-log collection with independent intervals, timeouts, jitter, and concurrency limits.
- Add Prometheus text, Influx line, and OTLP/HTTP JSON compatibility adapters as optional input formats, without requiring those services.
- Add configurable metric rules, persisted pending/firing state, event search, Agent credential enrollment/revocation, and delivery Webhooks.
- Add the shared suite header, configurable module switcher, visibility-aware polling, prioritized mobile overview columns, and consistent touch targets.
- Gate tag releases on Windows, macOS, and Linux tests plus race, static, vulnerability, formatting, vet, and changelog checks.
- Bound retained native metric samples independently from series cardinality and batch size, with restart-time capacity validation.
- Recover automatically from a full offline Agent spool by draining accepted backlog before retrying the new metric or event batch.
- Expose independent system, probe, file-log, and application collector intervals while preserving the shared interval default.
- Bound each node's report history by entry count and bytes, compact atomically, and normalize legacy history during Center startup.
- Keep Agent self-observability metrics (`agent_memory_bytes` and `agent_goroutines`) consistent on Linux and other supported platforms.

## 0.2.0 - 2026-08-12

- Deliver Center, Agent, and push modes with host, Docker, HTTP, TCP, TLS, PostgreSQL, and MySQL observations.
- Add multi-node health, metric history, database comparison, deterministic alerts, drift classification, and webhook delivery.
- Add resource groups that scope overview, node, database, alert, change, topology, and coverage APIs.
- Add topology perspectives, stable relationship IDs, confidence and evidence, discovery timestamps, and audited confirmation.
- Add monitoring coverage analysis and explicit stale-data behavior in the responsive Web console.
- Enforce loopback defaults, authenticated writes, TLS/mTLS for remote deployments, atomic persistence, and negative security tests.
- Upgrade database probe dependencies to patched versions and enforce `govulncheck` in CI.
