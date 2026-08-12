#!/usr/bin/env sh
set -eu

SOURCE=${1:-./fleetctl}
PREFIX=${PREFIX:-/usr/local}

if [ ! -f "$SOURCE" ]; then
  echo "binary not found: $SOURCE" >&2
  echo "usage: sudo ./scripts/install.sh [path-to-fleetctl]" >&2
  exit 1
fi

install -d -m 0755 "$PREFIX/bin" /etc/fleetscope /var/lib/fleetscope
install -d -m 0700 /var/lib/fleetscope-agent
install -m 0755 "$SOURCE" "$PREFIX/bin/fleetctl"
if ! id fleetscope >/dev/null 2>&1; then
  useradd --system --home /var/lib/fleetscope --shell /usr/sbin/nologin fleetscope
fi
chown fleetscope:fleetscope /var/lib/fleetscope
echo "installed $PREFIX/bin/fleetctl"
echo "examples: deploy/*.service, deploy/*.env.example, examples/{applications,probes,logs}.example.json"
