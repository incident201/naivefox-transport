#!/usr/bin/env bash
set -euo pipefail
transport_root=$(cd "$(dirname "$0")/.." && pwd)
# Existing development fixtures can reuse their warm toolchain and caches.
# A normal checkout only needs Go on PATH; no machine-local path is required.
if [[ -n ${NAIVEFOX_TOOLROOT:-} ]]; then
  export PATH="$NAIVEFOX_TOOLROOT/go1.25.12/bin:$NAIVEFOX_TOOLROOT/bin:$PATH"
  export GOCACHE="${GOCACHE:-$NAIVEFOX_TOOLROOT/go-build-cache}"
  export GOMODCACHE="${GOMODCACHE:-$NAIVEFOX_TOOLROOT/go-module-cache}"
fi
export GOCACHE="${GOCACHE:-$transport_root/artifacts/_work/go-build-cache}"
export GOMODCACHE="${GOMODCACHE:-$transport_root/artifacts/_work/go-module-cache}"
export TMPDIR="${NAIVEFOX_TMPDIR:-$transport_root/artifacts/_work/tmp}"
export GOTMPDIR="$TMPDIR"
mkdir -p "$GOCACHE" "$GOMODCACHE" "$TMPDIR"
cd "$transport_root"
exec "$@"
