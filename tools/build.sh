#!/usr/bin/env bash
set -euo pipefail
transport_root=$(cd "$(dirname "$0")/.." && pwd)
output=${1:-$transport_root/artifacts/bin}
[[ $output == /* ]] || { echo "output must be an absolute directory" >&2; exit 2; }
mkdir -p "$output"
bash "$transport_root/tools/go.sh" go build -trimpath -o "$output/bridge" ./cmd/bridge
bash "$transport_root/tools/go.sh" "${XCADDY:-xcaddy}" build v2.11.2 \
  --output "$output/caddy" \
  --with github.com/caddyserver/forwardproxy=github.com/incident201/forwardproxy@7f5bad724696e153716b1c9222176ba0ba05d543 \
  --with "github.com/incident201/naivefox-transport=$transport_root"
