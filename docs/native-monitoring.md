# Native monitoring architecture

FleetScope owns the default collection, transport, storage, query, rule, event, and UI path. Prometheus text, Influx Line Protocol, and OTLP/HTTP JSON are compatibility inputs only; no external collector or TSDB is required at runtime.

## Data paths

1. Each Agent collector runs on its own interval with a bounded concurrency pool, context timeout, and node-specific scheduling jitter.
2. Numeric points enter the telemetry spool. Logs and structured events enter a separate event spool, so one channel cannot discard the other.
3. Spool files are written atomically, retain a monotonic sequence across restarts, and drain FIFO. Center deduplicates by `node_id`, `source`, and sequence.
4. Center appends accepted batches to daily NDJSON segments and maintains an in-memory series/event index rebuilt on startup.
5. Query, catalog, alert rules, and the Web console read the embedded stores directly.

Public JSON contracts are `telemetry.batch/v1` and `event.batch/v1` in the lifecycle-spec repository.

## Native application collectors

`fleetctl agent --applications FILE` loads one strict JSON configuration for native application targets. The checked-in `examples/applications.example.json` covers all fields.

| Collector | Direct data source | Main metric families |
|---|---|---|
| system | Linux procfs/syscalls | `cpu_*`, `memory_*`, `disk_*`, `load_*`, `network_*` |
| Agent runtime | Go runtime | `agent_memory_bytes`, `agent_goroutines` on every supported platform |
| process | Linux procfs | `process_processes`, `process_memory_rss_bytes`, `process_threads`, `process_cpu_seconds_total` |
| Nginx | `stub_status` HTTP endpoint | `nginx_connections_*`, `nginx_http_requests_total` |
| Redis | RESP `INFO ALL` | `redis_connected_clients`, `redis_used_memory_bytes`, keyspace and traffic counters |
| PostgreSQL | read-only `pg_stat_database` | connection, transaction, row, block, deadlock, and temporary-byte metrics |
| MySQL | read-only global status | connections, threads, questions, bytes, slow queries, and uptime |
| Docker | Engine API over Unix socket | aggregate container state plus per-container CPU, memory, network, PID, and availability |

Every target carries `application`, `application_kind`, and `required` labels. A failed target produces `application_target_up=0` and a structured collector event while successful sibling targets in the same cycle are retained. Application configuration never accepts inline credentials: Redis passwords, database DSNs, and Nginx header values reference environment-variable names.

## Identity

`FLEET_TOKEN` is a bootstrap and administration secret. `POST /api/v1/agents/enroll` returns a random node credential once; Center stores only its SHA-256 hash. The Agent persists the secret as `agent-credential.json` with mode `0600` and uses it for native telemetry, events, and reports. A node credential receives `403` if it attempts to write another `node_id`.

Existing enrollment is not silently replaced. Use `fleetctl agent --reenroll` with the bootstrap token to rotate it, or revoke it with `DELETE /api/v1/agents/{node}`.

## Metric query

`GET /api/v1/telemetry/query` accepts:

- `metric` (required), `node`, `source`, and repeated `label=key=value` selectors.
- `start` and `end` as RFC3339 or Unix milliseconds; the maximum range is 31 days.
- `step` as a Go duration and `aggregate` as `avg`, `min`, `max`, `sum`, `last`, or `rate`.

`GET /api/v1/telemetry/catalog` reports series/sample counts, nodes, sources, type, unit, and last sample. `GET /api/v1/telemetry/sources` reports adapter and source freshness.

## Event query

`GET /api/v1/events` filters by node, source, kind, severity, service, search terms, time range, and cursor (`before`). Results are reverse chronological and page sizes are bounded to 1,000.

The file collector accepts JSON or text lines. Recognized JSON fields include `timestamp`/`time`, `level`/`severity`, `service`, `message`/`msg`, and arbitrary attributes. Partial lines remain unread until complete; truncation and rotation reset offsets safely.

## Limits and retention

Center defaults are 30 days, 50,000 metric series, 5,000,000 retained metric samples, 10,000 points per batch, 500,000 events, and 5,000 events per batch. Configure them with `--telemetry-retention`, `--telemetry-max-series`, `--telemetry-max-samples`, `--telemetry-max-batch`, `--event-retention`, `--event-max-entries`, and `--event-max-batch`.

Retention pruning runs at startup and periodically while stores are receiving data. Exceeding a hard capacity limit rejects new input with an explicit error; it never silently evicts an unrelated active series or event.
