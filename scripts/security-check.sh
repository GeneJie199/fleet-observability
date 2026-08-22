#!/usr/bin/env bash
set -euo pipefail

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"

mapfile -t packages < <(go list -f '{{.Dir}}' ./...)
go run github.com/securego/gosec/v2/cmd/gosec@v2.28.0 \
  -quiet -severity high -confidence high "${packages[@]}"
go run github.com/zricethezav/gitleaks/v8@v8.30.1 git . \
  --redact --no-banner --log-opts="--all"
