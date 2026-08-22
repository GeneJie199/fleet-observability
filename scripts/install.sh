#!/usr/bin/env sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ROOT_DIR=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
PREFIX=${PREFIX:-/usr/local}
CONFIG_DIR=${CONFIG_DIR:-/etc/fleetscope}
CENTER_DATA=${CENTER_DATA:-/var/lib/fleetscope}
AGENT_DATA=${AGENT_DATA:-/var/lib/fleetscope-agent}
INFRASCOUT_DATA=${INFRASCOUT_DATA:-/var/lib/infrascout}
SYSTEMD_DIR=${SYSTEMD_DIR:-/etc/systemd/system}
ACTION=install
SOURCE=./fleetctl
PURGE_DATA=0

case "${1:-}" in
  install|uninstall|doctor)
    ACTION=$1
    shift
    ;;
esac
if [ "$ACTION" = install ] && [ "$#" -gt 0 ]; then
  SOURCE=$1
  shift
fi
if [ "${1:-}" = "--purge-data" ]; then
  PURGE_DATA=1
  shift
fi
if [ "$#" -ne 0 ]; then
  echo "usage: sudo sh scripts/install.sh [install] [path-to-fleetctl]" >&2
  echo "       sudo sh scripts/install.sh uninstall [--purge-data]" >&2
  echo "       sudo sh scripts/install.sh doctor" >&2
  exit 2
fi

require_root() {
  if [ "$(id -u)" -ne 0 ]; then
    echo "this action requires root; run with sudo" >&2
    exit 1
  fi
}

has_systemd() {
  command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]
}

install_fleetscope() {
  require_root
  if [ ! -f "$SOURCE" ]; then
    echo "binary not found: $SOURCE" >&2
    exit 1
  fi
  if ! id fleetscope >/dev/null 2>&1; then
    useradd --system --home "$CENTER_DATA" --shell /usr/sbin/nologin fleetscope
  fi
  install -d -m 0755 "$PREFIX/bin" "$CONFIG_DIR"
  install -d -m 0750 -o fleetscope -g fleetscope "$CENTER_DATA"
  install -d -m 0700 "$AGENT_DATA" "$INFRASCOUT_DATA"
  install -m 0755 "$SOURCE" "$PREFIX/bin/fleetctl"

  if [ ! -s "$CONFIG_DIR/center.env" ]; then
    umask 077
    TOKEN=$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')
    printf 'FLEET_TOKEN=fleet_admin_%s\n' "$TOKEN" > "$CONFIG_DIR/center.env"
  fi
  chmod 0600 "$CONFIG_DIR/center.env"
  TOKEN=$(sed -n 's/^FLEET_TOKEN=//p' "$CONFIG_DIR/center.env" | head -n 1)
  if [ -z "$TOKEN" ]; then
    echo "$CONFIG_DIR/center.env does not contain FLEET_TOKEN" >&2
    exit 1
  fi
  if [ ! -s "$CONFIG_DIR/agent.env" ]; then
    NODE_ID=$(hostname | tr -c 'A-Za-z0-9._-' '-' | sed 's/^[._-]*//; s/[._-]*$//')
    [ -n "$NODE_ID" ] || NODE_ID=local-node
    umask 077
    {
      printf 'FLEET_CENTER_URL=http://127.0.0.1:8770\n'
      printf 'FLEET_NODE_ID=%s\n' "$NODE_ID"
      printf 'FLEET_AGENT_ARGS=--interval 30s --report-interval 1m --spool-dir %s --state-dir %s\n' "$AGENT_DATA" "$INFRASCOUT_DATA"
    } > "$CONFIG_DIR/agent.env"
  fi
  chmod 0600 "$CONFIG_DIR/agent.env"
  NODE_ID=$(sed -n 's/^FLEET_NODE_ID=//p' "$CONFIG_DIR/agent.env" | head -n 1)
  if [ -z "$NODE_ID" ]; then
    echo "$CONFIG_DIR/agent.env does not contain FLEET_NODE_ID" >&2
    exit 1
  fi

  for name in applications probes logs; do
    example="$ROOT_DIR/examples/$name.example.json"
    destination="$CONFIG_DIR/$name.json"
    if [ -f "$example" ] && [ ! -f "$destination.example" ]; then
      install -m 0600 "$example" "$destination.example"
    fi
  done

  if [ -f "$ROOT_DIR/deploy/fleet-center.service" ] && [ -f "$ROOT_DIR/deploy/fleet-agent.service" ]; then
    install -m 0644 "$ROOT_DIR/deploy/fleet-center.service" "$SYSTEMD_DIR/fleet-center.service"
    install -m 0644 "$ROOT_DIR/deploy/fleet-agent.service" "$SYSTEMD_DIR/fleet-agent.service"
  else
    echo "service templates were not found; binary and configuration were installed" >&2
  fi

  if has_systemd && [ -f "$SYSTEMD_DIR/fleet-center.service" ]; then
    systemctl daemon-reload
    systemctl enable --now fleet-center.service
    ATTEMPT=0
    until "$PREFIX/bin/fleetctl" doctor --url http://127.0.0.1:8770 --data "$CENTER_DATA" --token-file "$CONFIG_DIR/center.env" >/dev/null 2>&1; do
      ATTEMPT=$((ATTEMPT + 1))
      if [ "$ATTEMPT" -ge 20 ]; then
        echo "FleetScope center did not become healthy; inspect systemctl status fleet-center" >&2
        exit 1
      fi
      sleep 1
    done
    if [ ! -s "$AGENT_DATA/agent-credential.json" ]; then
      FLEET_TOKEN="$TOKEN" "$PREFIX/bin/fleetctl" agent --center http://127.0.0.1:8770 --node "$NODE_ID" --once --interval 10s --report-interval 30s --spool-dir "$AGENT_DATA" --state-dir "$INFRASCOUT_DATA"
    fi
    if grep -q '^FLEET_TOKEN=' "$CONFIG_DIR/agent.env"; then
      umask 077
      TEMP_ENV="$CONFIG_DIR/.agent.env.$$"
      grep -v '^FLEET_TOKEN=' "$CONFIG_DIR/agent.env" > "$TEMP_ENV" || true
      chmod 0600 "$TEMP_ENV"
      mv "$TEMP_ENV" "$CONFIG_DIR/agent.env"
    fi
    systemctl enable --now fleet-agent.service
    echo "FleetScope services are installed and started."
    echo "Console: http://127.0.0.1:8770/"
  else
    echo "FleetScope is installed. Start it with:"
    echo "  $PREFIX/bin/fleetctl up --data $CENTER_DATA"
  fi
  echo "Configuration: $CONFIG_DIR"
  echo "Data: $CENTER_DATA and $AGENT_DATA"
}

uninstall_fleetscope() {
  require_root
  if has_systemd; then
    systemctl disable --now fleet-agent.service fleet-center.service >/dev/null 2>&1 || true
  fi
  rm -f "$SYSTEMD_DIR/fleet-agent.service" "$SYSTEMD_DIR/fleet-center.service"
  rm -f "$PREFIX/bin/fleetctl"
  if has_systemd; then
    systemctl daemon-reload
  fi
  if [ "$PURGE_DATA" -eq 1 ]; then
    rm -rf "$CONFIG_DIR" "$CENTER_DATA" "$AGENT_DATA" "$INFRASCOUT_DATA"
    echo "FleetScope and its configuration/data were removed."
  else
    echo "FleetScope was removed. Configuration and data were preserved."
    echo "Use uninstall --purge-data only when permanent deletion is intended."
  fi
}

doctor_fleetscope() {
  if [ ! -x "$PREFIX/bin/fleetctl" ]; then
    echo "fleetctl is not installed at $PREFIX/bin/fleetctl" >&2
    exit 1
  fi
  "$PREFIX/bin/fleetctl" doctor --url http://127.0.0.1:8770 --data "$CENTER_DATA" --token-file "$CONFIG_DIR/center.env"
}

case "$ACTION" in
  install) install_fleetscope ;;
  uninstall) uninstall_fleetscope ;;
  doctor) doctor_fleetscope ;;
esac
