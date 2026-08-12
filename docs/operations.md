# FleetScope Operations

## Topology

Run one center and one Agent per monitored node. Agents push metrics, InfraScout inventory/drift, and optional service probes; the center never logs in to a node.

## Center

For a single administrator, keep the center on loopback and use an SSH tunnel:

```bash
export FLEET_TOKEN='replace-with-a-long-random-token'
fleetctl serve --addr 127.0.0.1:8770 --data /var/lib/fleetscope
ssh -L 8770:127.0.0.1:8770 admin@fleet-center
```

For a network listener, FleetScope requires both `FLEET_TOKEN` and TLS:

```bash
fleetctl serve --addr 10.0.0.10:8770 --data /var/lib/fleetscope \
  --tls-cert /etc/fleetscope/server.pem \
  --tls-key /etc/fleetscope/server-key.pem \
  --client-ca /etc/fleetscope/client-ca.pem
```

`--client-ca` enables mutual TLS. Non-loopback listeners automatically require the management bearer token for read APIs. Loopback deployments can opt into the same behavior with `--protect-reads`.

## Agent

```bash
export FLEET_TOKEN='same-token-as-center'
fleetctl agent \
  --center https://fleet.example.internal:8770 \
  --node host-01 \
  --infrascout /usr/local/bin/infrascout \
  --state-dir /var/lib/infrascout \
  --probes /etc/fleetscope/probes.json \
  --applications /etc/fleetscope/applications.json \
  --spool-dir /var/lib/fleetscope-agent \
  --ca /etc/fleetscope/ca.pem \
  --label environment=production \
  --label version=1.2.3
```

The first Agent run creates an InfraScout baseline when none exists. Later runs use `infrascout check --fail-on never`, so drift is reported without stopping the Agent.

The first Agent run also exchanges the management token for a node-bound credential stored in its spool directory. Normal reports no longer use the management token. If the credential is lost, run once with `--reenroll`; this is an explicit rotation, not an automatic replacement.

Use `--collector-timeout`, `--collector-concurrency`, and `--collector-jitter` to bound slow collectors and avoid synchronized fleet spikes. Tune independent schedules with `--system-interval`, `--probe-interval`, `--log-interval`, and `--application-interval`; each falls back to `--interval` when omitted. Native application collection is enabled with `--applications`; file logs use `--logs`. Start from the checked-in examples for both files.

Database DSNs are named through `dsn_env`; Redis secrets and HTTP authentication headers use environment references as well. Credentials never belong in JSON configuration or the node report. Use read-only monitoring identities.

## systemd

The repository includes separate center and Agent units. Review the environment examples before installing them:

```bash
sudo sh ./scripts/install.sh ./fleetctl
sudo install -m 0644 deploy/fleet-center.service /etc/systemd/system/
sudo install -m 0600 deploy/center.env.example /etc/fleetscope/center.env
sudo systemctl daemon-reload
sudo systemctl enable --now fleet-center
```

For the Agent, install `fleet-agent.service`, `agent.env`, `applications.json`, and any probe/log configuration on each node. The installer creates `/var/lib/fleetscope-agent` for its durable queue and node credential. If the Agent does not run InfraScout, remove `/var/lib/infrascout` from `ReadWritePaths` or create that directory first.

## Storage and backup

- `nodes/*.json` contains the latest atomic report for each node.
- `history/*.ndjson` contains bounded-query metric history.
- `telemetry/*.ndjson` and `events/*.ndjson` contain daily native store segments.
- `agents.json` contains node metadata and credential hashes, never plaintext Agent tokens.
- `alerts.json` and `changes.json` preserve operator state.
- `groups.json` contains resource groups and their node membership.
- `topology-reviews.json` contains relationship confirmations and reviewer notes.
- Back up `/var/lib/fleetscope`; restore it only while the center is stopped. Preserve permissions because the directory contains operational metadata and credential hashes.
- Set explicit retention and capacity flags for the expected node, series, sample, event, and report-history volume. `--history-max-entries` and `--history-max-bytes` bound each node's NDJSON report history independently. Monitor rejected ingest responses and Agent spool growth as hard-capacity signals.
