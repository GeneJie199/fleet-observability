# Scale baseline

FleetScope v1.0 is validated as a single-center deployment for up to 100 nodes. This is a measured support boundary, not a claim of unlimited horizontal scale.

## Workload

The checked-in `fleetbench` command exercises the production telemetry and event stores, including durable segment writes, sequence persistence, a cold reopen, and a cross-node query.

```text
nodes                 100
metrics per node      8
metric interval       60 seconds
simulated retention   72 hours
metric samples        3,456,000
events                43,200
```

Run it from a source checkout:

```bash
go run ./cmd/fleetbench --nodes 100 --intervals 4320 --interval-seconds 60 --events-per-node 432
```

The benchmark uses synthetic values but the real append-only files, indexes, retention logic, restart loader, and query path. It does not use a mock store.

## Results

Measured on 2026-08-23 with Go 1.26.6. Each platform ran the same candidate and workload.

| Measurement | Windows 11, 4 cores / 8 threads, 15.8 GiB RAM | Ubuntu 24.04, 2 cores, 7.6 GiB RAM |
| --- | ---: | ---: |
| End-to-end wall time | 245.51 s | 67.82 s |
| Average CPU cores used | 0.64 | 0.89 |
| Metric ingest | 21,959 samples/s | 109,166 samples/s |
| Event ingest | 3,837 events/s | 12,202 events/s |
| Cold restart load | 74.28 s | 31.53 s |
| 100-series aggregate query | 1,331 ms | 841 ms |
| Live heap after restart | 258.6 MiB | 258.3 MiB |
| Peak working set / RSS | 330 MiB | 342 MiB |
| Three-day store size | 616.6 MiB | 616.6 MiB |
| Estimated daily growth | 205.5 MiB | 205.5 MiB |

Windows filesystem and endpoint protection behavior materially affected write and reload time on the tested machine. Capacity planning should use the slower result unless the target environment has been measured.

## Recommended profiles

| Nodes | Collection profile | Metric retention | Minimum center resources | Free data disk |
| ---: | --- | ---: | --- | ---: |
| 1-25 | 8 metrics every 30 s | 7 days | 2 CPU, 2 GiB RAM | 5 GiB |
| 26-50 | 8 metrics every 60 s | 7 days | 2 CPU, 2 GiB RAM | 5 GiB |
| 51-100 | 8 metrics every 60 s | 3 days | 2 CPU, 2 GiB RAM | 5 GiB |

Use `fleetctl serve` with explicit `--telemetry-retention`, `--telemetry-max-samples`, `--event-retention`, and `--event-max-entries` values for shared centers. The 100-node profile fits the default 5,000,000-sample and 500,000-event capacity limits.

Time retention and capacity are both enforced. When the sample capacity is reached, FleetScope keeps accepting data and rolls the oldest in-memory samples out; a restart restores the newest capacity-bounded window from retained segment files. Disk files continue to follow the configured time retention.

## Support boundary

- More than 100 nodes in one center is not validated for v1.0.
- Thirty-day raw metric retention at 100 nodes is not a recommended single-center profile.
- Query latency grows with the selected time range and number of returned series.
- Disk growth varies with label length, event message size, metric count, and collection interval.
- FleetScope owns its native collector and store. Prometheus, Influx line protocol, and OTLP are optional compatibility inputs, not runtime dependencies.

For a larger estate, use multiple centers aligned to environments or regions and keep a longer-term external export if required.
