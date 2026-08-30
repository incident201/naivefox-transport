#!/usr/bin/env bash
set -euo pipefail
transport_root=$(cd "$(dirname "$0")/.." && pwd)
output=${1:?absolute output directory required}
[[ $output == /* ]] || exit 2
mkdir -p "$output"
bash "$transport_root/tools/go.sh" go build -trimpath -o "$output/bridge" ./cmd/bridge
cd "$output"
bash "$transport_root/tools/go.sh" /home/zubastik/naivefox-refresh-20260830.fJHfmY/full-linux/naivefox-fixture/tools/bin/xcaddy build v2.11.2 \
  --output "$output/caddy" \
  --with github.com/caddyserver/forwardproxy=github.com/klzgrad/forwardproxy@v0.0.0-20250118002110-d62c80d3dd2c \
  --with "naivefox.local/transport=$transport_root"
