#!/usr/bin/env bash
set -euo pipefail
toolroot=/home/zubastik/naivefox-refresh-20260830.fJHfmY/full-linux/naivefox-fixture/tools
export PATH="$toolroot/go1.25.12/bin:$PATH"
export GOCACHE="$toolroot/go-build-cache"
export GOMODCACHE="$toolroot/go-module-cache"
cd /home/zubastik/naivefox-transport
exec "$@"
