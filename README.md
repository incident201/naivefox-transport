# NaiveFox no-connect transport for Caddy

This repository maintains the Caddy `http.handlers.naivefox_transport` module
used by [NaiveFox](https://github.com/incident201/naivefox)'s opt-in **no-connect**
transport. Ordinary HTTPS GET/POST requests carry bounded, multiplexed TCP data.
There is no outer HTTP CONNECT or WebSocket. TLS and HTTP/2 or HTTP/3 remain the
responsibility of Caddy and the client network stack.

NaiveFox's default **classic** transport uses the Naive forwardproxy module.
One Caddy binary can contain both modules: `naivefox_transport` handles its
application routes and delegates CONNECT and unrelated paths to the next
handler. Each transport has separate credentials. The transport key and target
allowlist never authorize classic forwardproxy access.

This code began as an application-carrier experiment. Its history, optional
browser/loopback bridge, gallery assets, and experimental profiles remain here
for reproducibility. They are not dependencies of the native lean NaiveFox
client. The selected profile is `continuous-bulk-pipeline`: other experimental
profiles are not interchangeable with the native client. Historical bandwidth,
timing, and browser results are in [docs/EXPERIMENTS.md](docs/EXPERIMENTS.md).
Those results do not establish the camouflage quality of a native client.

## Build and test

The retained versions are Go 1.25.12, Caddy 2.11.2, and the Naive forwardproxy
revision `d62c80d3dd2c`. Put Go and xcaddy on PATH, then run:

```sh
bash tools/go.sh go test -race ./...
node --test test/*.test.js
bash tools/build.sh
./artifacts/bin/caddy list-modules
NAIVEFOX_CADDY_BIN="$PWD/artifacts/bin/caddy" bash tools/go.sh go test -race -run TestCombinedCaddyTLS -count=1 .
```

`tools/build.sh` builds the optional laboratory bridge and a single Caddy binary
containing both `http.handlers.forward_proxy` and
`http.handlers.naivefox_transport`. It does not build Firefox. Its optional
argument is an absolute output directory. Build output, temporary xcaddy work,
and default Go caches stay under ignored `artifacts/`.

To reuse existing warm development caches, set `GOCACHE` and `GOMODCACHE`.
`NAIVEFOX_TOOLROOT` additionally supports the retained fixture layout
(`go1.25.12/bin`, `bin/xcaddy`, `go-build-cache`, `go-module-cache`). No local
directory is hardcoded in the scripts. `XCADDY` may select an executable and
`NAIVEFOX_TMPDIR` may select a shared temporary build directory.

For xcaddy builds elsewhere, use the published module path:

```sh
xcaddy build v2.11.2 \
  --with github.com/caddyserver/forwardproxy=github.com/klzgrad/forwardproxy@v0.0.0-20250118002110-d62c80d3dd2c \
  --with github.com/incident201/naivefox-transport
```

Pin the module to a reviewed commit in deployment automation. The module's own
Go dependencies follow the selected Caddy version; they do not enter the lean
C++ client build graph.

## Serve classic and no-connect together

Use [examples/Caddyfile](examples/Caddyfile) with the combined binary. Set the
five environment variables shown there: server hostname, no-connect key, exact
target allowlist, classic username, and classic password. Generate a separate
random no-connect key, for example with `openssl rand -hex 32`; protect the
environment file and do not commit it. Then validate and start the configuration:

```sh
./artifacts/bin/caddy validate --adapter caddyfile --config examples/Caddyfile
./artifacts/bin/caddy run --adapter caddyfile --config examples/Caddyfile
```

The explicit `route` order matters. The first handler serves `/` and its
application routes, while classic CONNECT passes through to `forward_proxy`.
The last handler returns 404 for other paths. The root HTML also remains
available to the classic client's H3 startup request. Forwardproxy's own ACL
and credentials still apply to classic. Do not put a compression handler around
the no-connect carrier routes.

The native client requires `X-App-Profile: continuous-bulk-pipeline` on the
initial `GET /` response before it sends AUTH. This header is emitted only for
the root handshake, reports the resolved profile even when configuration omits
it, and prevents accidental use of a different credit window. Older experimental
server binaries without the header must be upgraded for native no-connect.
See [docs/PROTOCOL.md](docs/PROTOCOL.md) for the wire contract and lifecycle.

## Configuration and limits

The JSON handler name is `naivefox_transport`. Set `key` to a private string of
at least 32 bytes and `allowed_targets` to an exact list of `host:port` strings.
Use only HTTPS with certificate validation at the client. Keep the key, private
Caddy configurations, logs, TLS keys and captures outside Git. Do not enable
response compression on carrier routes.

The omitted `profile` resolves to `continuous-bulk-pipeline`, with 512 KiB of
receive credit per stream. An explicit profile must match the client. The
experimental `append_mode` and other profiles are for historical tests, not
native no-connect configuration. `stats_path` optionally writes counters on
cleanup; the authenticated `/__lab/stats` and `/__lab/sessions` diagnostic
routes are retained for fixtures.

The server binds random, Secure/HttpOnly session cookies to the client IP.
Only authenticated cells can open targets. Limits include 128 sessions,
32 simultaneous streams per session, bounded frame queues, a five-second dial
timeout, and two-minute session expiry without requests. A session has one
ordered cell sequence per direction; there is no reconnect or resume. Each
stream's byte sequence is 32-bit and fails closed beyond its range.

The implementation is experimental. It does not promise production deployment
hardening, denial-of-service resistance, managed key rotation, or that its
traffic is indistinguishable from normal web browsing. Exact target allowlisting
is intentional; do not expose an unrestricted dialer to solve configuration
errors.

## Maintenance

Keep wire-format, profile, flow-control and routing changes covered by tests.
The CI workflow runs both suites, builds the combined binary, and runs the
TLS cohosting test. That test loads the checked-in Caddyfile, validates a local
certificate without insecure TLS, exchanges no-connect frames over HTTP/2,
and keeps a classic CONNECT tunnel alive through the same Caddy process.

Go race tests exercise framing, authorization, replay rejection, concurrent
streams, both laboratory proxy frontends, transfers larger than the credit
window, half-close and cancellation. JavaScript tests retain browser lifecycle
and response-validation coverage; they do not require Firefox.

The pre-extraction history is preserved. No new license grant is implied by
moving that existing source to a separate repository; dependency licenses remain
their respective authors' licenses.
