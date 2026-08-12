# Changelog

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
