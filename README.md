# NaiveFox no-connect transport for Caddy

This repository maintains the Caddy `http.handlers.naivefox_transport` module
used by [NaiveFox](https://github.com/incident201/naivefox)'s opt-in **no-connect**
transport. Ordinary HTTPS GET/POST requests carry bounded, multiplexed TCP data.
The original `no-connect` mode has no outer CONNECT or WebSocket. The explicit
`no-connect-hybrid` mode completes the same page and API startup, then carries
NFC1 over one shaped WebSocket. TLS and HTTP remain the responsibility of Caddy
and the client network stack.

NaiveFox's default **classic** transport uses the Naive forwardproxy module.
One Caddy binary contains both modules. `naivefox_transport` serves its
application routes and delegates classic requests to one nested `forward_proxy`
handler. **Both transports use the same username/password and destination
policy, configured once. There is no separate key or mandatory target list.**

The client keeps its existing proxy URL and changes only `transport`:

```json
{
  "listen": "socks://127.0.0.1:1080",
  "proxy": "https://USER:PASSWORD@proxy.example.com:443",
  "transport": "no-connect"
}
```

`classic` remains the default. `--transport no-connect` or `--transport classic`
overrides JSON. Percent-encode reserved characters in URL credentials. Use
`quic://` for H3 (UDP 443 must be reachable), or `https://` for H2.

## Ready Caddy for Linux x86_64

The [releases](https://github.com/incident201/naivefox-transport/releases) contain
`caddy-linux-amd64`, its checksum and `build-info.json` with exact revisions.
The workflow runs the Go, JavaScript and actual-binary tests before publishing.

```sh
mkdir -p "$HOME/caddy-naivefox-download"
cd "$HOME/caddy-naivefox-download"
curl -fLO https://github.com/incident201/naivefox-transport/releases/latest/download/caddy-linux-amd64
curl -fLO https://github.com/incident201/naivefox-transport/releases/latest/download/caddy-linux-amd64.sha256
sha256sum -c caddy-linux-amd64.sha256
chmod +x caddy-linux-amd64
./caddy-linux-amd64 list-modules | grep -E 'forward_proxy|naivefox_transport'
```

The binary includes standard Caddy modules and both proxy modules. If your
current Caddy has other plugins (such as a DNS certificate plugin), add them
to the custom xcaddy build below. They are **not included automatically**.
Caddy plugins are compiled into its executable; they are not separate `.so`
files. You can replace the executable without reinstalling the service or
deleting its configuration/certificate storage.

This code began as an application-carrier experiment. Its history, optional
browser/loopback bridge, gallery assets, and experimental profiles remain here
for reproducibility. They are not dependencies of the native lean NaiveFox
client. The selected profile is `continuous-bulk-pipeline`: other experimental
profiles are not interchangeable with the native client. Historical bandwidth,
timing, and browser results are in [docs/EXPERIMENTS.md](docs/EXPERIMENTS.md).
Those results do not establish the camouflage quality of a native client.

## Optional hybrid client

Select `"transport":"no-connect-hybrid"` in a matching native client, or pass
`--transport no-connect-hybrid`. The same `continuous-bulk-pipeline` server
profile supports both clients; existing GET/POST behavior is unchanged.
After assets and all twenty startup API pairs finish, the hybrid opens
`/api/realtime` with WebSocket subprotocol `nfc1.hybrid.v1`. It preserves the
session, authenticated mux streams, sequence numbers and 512-KiB stream credit.

The current Firefox WebSocket path uses a new TLS/TCP connection with an HTTP/1.1
upgrade, including after an H3 startup. Consequently this explicit hybrid is a
mixed HTTP protocol mode, not an H3-only transport. Caddy must permit `h1` as
well as the selected startup protocol. There is no automatic fallback to this
mode from `no-connect`, and WebSocket failure aborts the session without replay.

Binary message capacities are 64 KiB for activity, 256 KiB for bulk, and 512
bytes for controls or idle heartbeats. Fresh random filler fills unused space.
One bounded writer coalesces activity for at most 2 ms; idle heartbeats occur
after 25 seconds. Further local connections reuse the WebSocket until the
carrier's existing 32-stream limit requires another session.

Ordinary visitors can open the gallery with `#realtime` to follow the same
startup and idle WebSocket lifecycle without credentials. The fragment is not
sent in HTTP. Anonymous WebSockets accept only empty cells and cannot open
targets; proxy authentication must already have succeeded in startup AUTH.
See [the wire contract](docs/PROTOCOL.md#optional-realtime-transition) for bounds,
acknowledgements and failure handling. Functional tests do not establish a
performance or camouflage improvement.

## Build and test

The retained versions are Go 1.25.12 and Caddy 2.11.2. Put Go and xcaddy 0.4.6
on PATH, then run:

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
and default Go caches stay under ignored `artifacts/`. Caches and temporary
Go sources live in `artifacts/_work/`, which Go's `./...` package discovery
skips even after a CI cache restore. Keep custom caches outside the source tree
or in an underscore-prefixed subtree for the same reason.
The wrapper also creates an ignored module boundary at `artifacts/go.mod` so
older cache directories remain outside source discovery after an upgrade.

To reuse existing warm development caches, set `GOCACHE` and `GOMODCACHE`.
`NAIVEFOX_TOOLROOT` additionally supports the retained fixture layout
(`go1.25.12/bin`, `bin/xcaddy`, `go-build-cache`, `go-module-cache`). No local
directory is hardcoded in the scripts. `XCADDY` may select an executable and
`NAIVEFOX_TMPDIR` may select a shared temporary build directory.

For xcaddy builds elsewhere, use the published module path:

```sh
xcaddy build v2.11.2 \
  --with github.com/caddyserver/forwardproxy@v0.0.0-20250118002110-d62c80d3dd2c=github.com/klzgrad/forwardproxy@v0.0.0-20250118002110-d62c80d3dd2c \
  --with github.com/incident201/naivefox-transport
```

Install xcaddy with `go install github.com/caddyserver/xcaddy/cmd/xcaddy@v0.4.6`.
Pin this module to a reviewed release revision in deployment automation. Append
one `--with module@version` for each additional plugin in your current Caddy.
This build uses the **ordinary, unmodified** klzgrad forwardproxy `naive` branch,
pinned to `d62c80d3dd2c`. No private fork, vendored replacement or private API is
needed. The transport reads the handler's public credential and policy settings;
its small TCP policy engine and upstream dialer are covered by differential
tests against that actual module. Caddy still owns the client-facing HTTP/TLS
stack. These Go dependencies are server-only and never enter the lean C++ client
build graph.

## Serve classic and no-connect together

Use [examples/Caddyfile](examples/Caddyfile) with the combined binary. Set only
server hostname, proxy username and proxy password. Protect the environment
file and do not commit credentials. The equivalent literal configuration is:

```caddyfile
proxy.example.com {
    route {
        naivefox_transport {
            forward_proxy {
                basic_auth USER PASSWORD
                hide_ip
                hide_via
                probe_resistance
            }
        }
        respond 404
    }
}
```

If you already have `forward_proxy`, move its **entire existing block** inside
`naivefox_transport`, preserving all its options. Do not leave a duplicate
standalone handler. Repeat `basic_auth` for multiple accounts if needed; all
accounts work in both modes. Then validate with the **new** binary:

```sh
./artifacts/bin/caddy validate --adapter caddyfile --config examples/Caddyfile
./artifacts/bin/caddy run --adapter caddyfile --config examples/Caddyfile
```

Both transports use the nested handler's credentials, `acl`, `ports`, `upstream`
and `dial_timeout` configuration. Public hostnames and ports do not need individual entries.
Forwardproxy's ordinary default protection against private/LAN destinations
still applies; use its normal ACL to intentionally allow such destinations.
No no-connect-only target allowlist exists. As in ordinary forwardproxy, an
explicit `upstream` delegates destination DNS, ACL and port policy to that
upstream instead of enforcing those local destination rules. No-connect
performs cancellable dialing, preserves TCP half-close and validates HTTPS
upstream certificates, including on loopback.

The module serves `/` and its application/assets routes. An existing root page
on that site is replaced. Classic H3 startup remains supported. Other requests
pass through forwardproxy to the next handler. Do not put a compression handler
around carrier routes.

For an existing Ubuntu/Debian `caddy.service`, save the old binary and Caddyfile,
install this binary separately at `/usr/local/bin/caddy-naivefox`, and validate
using the service environment. Point the service's `ExecStart` and `ExecReload`
to that new path (preserving other arguments) with a systemd override, then run
`systemctl daemon-reload` and `systemctl restart caddy`. Restart briefly closes
active connections. Keep `/var/lib/caddy` and certificate storage unchanged.
Rollback restores the old Caddyfile and executable path. Docker deployments
replace their container image instead. Never restart after failed validation.

For the usual `/etc/caddy/Caddyfile` service, a concrete upgrade sequence is:

```sh
sudo cp -a /etc/caddy/Caddyfile /etc/caddy/Caddyfile.before-naivefox
sudo install -m 755 ./caddy-linux-amd64 /usr/local/bin/caddy-naivefox
sudoedit /etc/caddy/Caddyfile
# Move the existing forward_proxy block into naivefox_transport as shown above.
sudo -u caddy /usr/local/bin/caddy-naivefox validate --config /etc/caddy/Caddyfile --adapter caddyfile
sudo systemctl edit caddy
```

In the override editor, use the following if those are your actual config path
and service arguments. Preserve any existing environment-file settings. If the
Caddyfile uses environment placeholders, supply the same environment during
validation; an interactive shell does not inherit the service environment.

```ini
[Service]
ExecStart=
ExecStart=/usr/local/bin/caddy-naivefox run --config /etc/caddy/Caddyfile --adapter caddyfile
ExecReload=
ExecReload=/usr/local/bin/caddy-naivefox reload --config /etc/caddy/Caddyfile --adapter caddyfile --force
```

Do not retain `--environ` when credentials are in environment variables: that
flag prints the entire environment to the journal. After successful validation:

```sh
sudo systemctl daemon-reload
sudo systemctl restart caddy
sudo systemctl status caddy --no-pager
```

The packaged `/usr/bin/caddy` and certificate storage are left in place, and
package upgrades do not overwrite the custom binary under `/usr/local/bin`.
To roll back, restore the saved Caddyfile, remove only these two executable
overrides, reload systemd, and restart the service with its original executable.

The native client requires `X-App-Profile: continuous-bulk-pipeline` and
`X-App-Auth: basic` on the initial `GET /` response before it sends AUTH.
These headers are emitted only for
the root handshake, report the resolved profile even when configuration omits
it, and prevent accidental use of a different credit window. Older experimental
server binaries without the header must be upgraded for native no-connect.
See [docs/PROTOCOL.md](docs/PROTOCOL.md) for the wire contract and lifecycle.

## Configuration and limits

The JSON handler name is `naivefox_transport`; its `forward_proxy` object holds
the ordinary forwardproxy options without a second `handler` field. Credentials
must be configured; a missing list or an entirely empty username/password pair
fails validation: the native classic client sends no authentication for an
entirely empty pair. One empty component in JSON is accepted for compatibility;
the Caddyfile keeps the ordinary forwardproxy parser's username rules. Use a
strong password and HTTPS with certificate validation. Keep private
configs, logs, TLS keys and captures outside Git.

For migration, upgrade both server and client. Remove server `key` and
`allowed_targets`, nest `forward_proxy` as above, and remove client
`no-connect-key`. Keep the proxy URL. Obsolete server settings fail explicitly,
even when empty; they are never ignored. Old key-based servers lack the new
Basic handshake and cannot accidentally receive a new client's credentials.

The omitted `profile` resolves to `continuous-bulk-pipeline`, with 512 KiB of
receive credit per stream. An explicit profile must match the client. The
experimental `append_mode` and other profiles are for historical tests, not
native no-connect configuration. `stats_path` optionally writes counters on
cleanup. All `/__lab/*` HTTP routes return 404 by default. The optional
`diagnostics` flag enables only authenticated `GET /__lab/stats` for private
fixtures; do not enable it on public multi-user deployments. It uses HTTP
`Authorization: Basic ...` with the same proxy credentials and exposes aggregate
counters, never credentials or target addresses. The session-deletion HTTP API
was removed entirely: proxy users are not administrators. Use Caddy's protected
admin/config lifecycle to close sessions. Reload Caddy to rotate credentials; old module
instances close their sessions and new sessions use the new list.

The server binds random, Secure/HttpOnly session cookies to the client IP.
Only authenticated cells can open targets. `max_sessions` defaults to 128 and
can be increased for available memory/file descriptors. At capacity, the oldest
unauthenticated visitor is replaced; authenticated sessions are never evicted.
If all slots are authenticated, new sessions are rejected until capacity is free.
There are 32 streams per session; clients can use additional sessions for more
concurrent streams. Queues and credit remain bounded. Sessions expire after
two minutes without requests; active transfers and 30-second idle polls refresh
that timer. There is no fixed lifetime limit on active sessions. Byte offsets
wrap modulo 2^32, so a stream is not limited to 4 GiB. Cell sequences and stream
IDs do not wrap/reuse within a session. There is no reconnect or resume.

The supported native contract is `continuous-bulk-pipeline` with `append_mode`
disabled. Historical profile variants and the optional browser/bridge worker
remain research tools, outside that native contract. Tests cover byte-window
backpressure, small-frame coalescing, bounded scheduler and metric storage,
stalled-upload isolation, cancellation, half-close and dual-transport routing.

Deployment requires operator-managed TLS, private credentials and appropriate
network/resource limits. The tests do not establish resistance to every denial-of-service
attack, performance parity, or traffic indistinguishability from web browsing.
Missing credentials never enable anonymous proxy access.

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
