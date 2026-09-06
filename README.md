# NaiveFox no-connect transport for Caddy

This repository maintains the Caddy `http.handlers.naivefox_transport` module
used by [NaiveFox](https://github.com/incident201/naivefox)'s opt-in **no-connect**
transport. A fixed ordinary HTTPS startup carries bounded NFC1 cells, then
one shaped native WebSocket per carrier carries multiplexed TCP streams.
TLS and HTTP remain the responsibility of Caddy and the client network stack.

NaiveFox's default **classic** transport uses the Naive forwardproxy module.
One Caddy binary contains both modules. `naivefox_transport` serves its
application routes and delegates classic requests to one nested `forward_proxy`
handler. **Both transports use the same username/password and destination
policy, configured once. There is no separate key or mandatory target list.**

For deployment, see [Site directory and Caddyfile](#site-directory-and-caddyfile):
one `application_root` contains the full public site and its seven required files.

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
`quic://` for H3 startup or `https://` for H2 startup; no-connect also
requires TCP for its persistent WebSocket.

## Ready Caddy for Linux x86_64

The [releases](https://github.com/incident201/naivefox-transport/releases) contain
`caddy-linux-amd64`, the external application template, their checksums, and
`build-info.json` with exact revisions. The workflow runs the Go, JavaScript,
template-validation and actual-binary tests before publishing.

```sh
mkdir -p "$HOME/caddy-naivefox-download"
cd "$HOME/caddy-naivefox-download"
curl -fLO https://github.com/incident201/naivefox-transport/releases/latest/download/caddy-linux-amd64
curl -fLO https://github.com/incident201/naivefox-transport/releases/latest/download/caddy-linux-amd64.sha256
curl -fLO https://github.com/incident201/naivefox-transport/releases/latest/download/naivefox-application-template-v1.tar.gz
curl -fLO https://github.com/incident201/naivefox-transport/releases/latest/download/naivefox-application-template-v1.tar.gz.sha256
sha256sum -c caddy-linux-amd64.sha256
sha256sum -c naivefox-application-template-v1.tar.gz.sha256
chmod +x caddy-linux-amd64
./caddy-linux-amd64 list-modules | grep -E 'forward_proxy|naivefox_transport'
mkdir application-template
tar -xzf naivefox-application-template-v1.tar.gz -C application-template
```

The binary includes standard Caddy modules and both proxy modules. If your
current Caddy has other plugins (such as a DNS certificate plugin), add them
to the custom xcaddy build below. They are **not included automatically**.
Caddy plugins are compiled into its executable; they are not separate `.so`
files. You can replace the executable without reinstalling the service or
deleting its configuration/certificate storage.

This code began as an application-carrier experiment. Its history, optional
browser/loopback bridge, external gallery template, and experimental profiles
remain here for reproducibility. The template is a server deployment asset and
is never linked into the native lean NaiveFox client. The selected profile is
`native-stream-v1`: other experimental
profiles are not interchangeable with the native client. Historical bandwidth,
timing, and browser results are in [docs/EXPERIMENTS.md](docs/EXPERIMENTS.md).
Those results do not establish the camouflage quality of a native client.

## Native no-connect client

NaiveFox exposes two transports: `classic` (the default) and `no-connect`.
Select `"transport":"no-connect"` or `--transport no-connect` in the matching
client. No-connect completes the root, six assets and twenty ordered HTTP
POST/GET pairs, then uses one native WebSocket per carrier with subprotocol
`nfc1.stream.v1`. The profile is `native-stream-v1`; omit `profile` or use
that exact value in Caddy configuration.

The fixed startup uses H2 or H3 as selected by the client URI. The persistent
phase uses HTTP/1.1 WSS/TCP after either startup, so H3 no-connect needs TCP
as well as UDP access. Classic remains ordinary Naive CONNECT, including
strict H3/QUIC. Both transports share the existing credentials and policy.

Capacity follows actual local sendable data within the 512-KiB stream window.
The client uses 512-byte controls and 4/16/128-KiB data messages; the server
uses 512-byte controls and 8/64/256-KiB data messages. Partial payloads coalesce
for 2 ms; full selected capacities and controls dispatch immediately. The
server encodes each cell once. Heartbeats use 512 bytes after 25 seconds.
Each carrier supports 32 concurrent logical streams; additional carriers
provide further capacity.

The old finite HTTP carrier, generic hybrid, asymmetric selector names,
pressure hints and laboratory browser/bridge implementations are retired.
Upgrade client and server together. There are no aliases for old wire profiles
or subprotocols. Stream sequences, delivery-based credits, half-close and
cumulative FIN acknowledgements are preserved. A failed carrier cannot replay,
reconnect or resume HTTP work. See [the wire contract](docs/PROTOCOL.md).

## Build and test

The retained versions are Go 1.25.12 and Caddy 2.11.2. Put Go and xcaddy 0.4.6
on PATH, then run:

```sh
bash tools/go.sh go test -race ./...
node --check template/assets/app.js
bash tools/build.sh
./artifacts/bin/caddy list-modules
NAIVEFOX_CADDY_BIN="$PWD/artifacts/bin/caddy" bash tools/go.sh go test -race -run 'TestCombinedCaddy(TLS|RejectsInvalidExternalApplication)' -count=1 .
```

`tools/build.sh` builds a single Caddy binary
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

## Site directory and Caddyfile

Put the **entire public site in one directory**. Set `application_root` to the
directory containing `index.html`, not to `assets/` or to a file. For example:

```text
/etc/caddy/naivefox-applications/atlas-v1/
├── index.html                 # required
├── assets/
│   ├── site.css               # required
│   ├── app.js                 # required
│   ├── image-1.svg            # required
│   ├── image-2.svg            # required
│   ├── image-3.svg            # required
│   ├── image-4.svg            # required
│   ├── extra.js               # optional example
│   └── fonts/
│       └── body.woff2         # optional example
├── pages/
│   └── about/
│       └── index.html         # optional example
└── favicon.ico                # optional example
```

The seven marked files are required; add any other site files in this same tree.
Only the seven have the [fixed transport size and format limits](template/README.md#fixed-public-contract).
For example, `assets/extra.js` is available at `/assets/extra.js`, and
`pages/about/index.html` at `/pages/about/`. No manifest or second directory
is needed. Keep private configuration, logs and keys outside the public tree.

Save this configuration as **`/etc/caddy/Caddyfile`**, outside the site directory.
Replace `proxy.example.com`, `USER` and `PASSWORD` with your values:

```caddyfile
:443, proxy.example.com {
    route {
        naivefox_transport {
            application_root /etc/caddy/naivefox-applications/atlas-v1
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

No separate `root` or `file_server` directive is needed for this site.
`respond 404` handles requests with no matching file or transport route.

Keep both site addresses: `:443` receives classic CONNECT requests whose
authority names the destination, and the named host enables certificate
automation. If you already have a `forward_proxy` block, move its entire
contents inside `naivefox_transport`, preserving its options; remove the old
standalone block. Repeat `basic_auth` for additional accounts. Both transports
share these credentials and the existing `acl`, `ports`, `upstream` and
`dial_timeout` settings. Default protection against private/LAN destinations
still applies. A configured upstream owns destination DNS and policy.

[examples/Caddyfile](examples/Caddyfile) is the equivalent configuration using
environment variables. Set all four variables documented at its top in the
environment that starts or reloads Caddy. For a source build:

```sh
./artifacts/bin/caddy validate --adapter caddyfile --config /etc/caddy/Caddyfile
./artifacts/bin/caddy run --adapter caddyfile --config /etc/caddy/Caddyfile
```

For a downloaded release, use `./caddy-linux-amd64` in those commands.
For an existing systemd service, use the installation steps below.

### File serving and updates

| Content | Served from | When changes take effect |
| --- | --- | --- |
| Seven required files | Validated, padded snapshot in memory | Successful reload/restart |
| All additional files | Disk on each GET/HEAD request | Without reload; browsers revalidate cached responses |
| Replacement of the root directory itself | Directory opened by the running module | Successful reload/restart |

Extra files have no transport size or text-format limits. Their responses
support MIME types, Last-Modified, conditional requests and byte ranges, with
`Cache-Control: no-cache`. Nested directories with `index.html` are supported.
Directory listing and automatic SPA fallback are disabled. `/index.html`
redirects to `/`, which serves the memory snapshot.

The seven fixed URLs and existing transport/diagnostic routes take priority
over files. Extra requests do not create transport sessions. Missing files and
unsupported static methods pass through forwardproxy to the next handler.
Do not put a compression handler around transport routes.

For a consistent whole-site update, prepare a complete new directory, change
`application_root` and reload. Invalid required files reject startup/reload;
a failed reload preserves the running configuration. The
[template instructions](template/README.md) list exact file limits and reserved URLs.

If upgrading from the old split-directory setup, move all additional public
files into `application_root` at their existing relative paths and remove the
separate site `root`/`file_server` configuration. This change requires the
updated server binary; no client update or new configuration option is needed.

## Install or upgrade the systemd service

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
sudo install -d -o root -g caddy -m 0750 /etc/caddy/naivefox-applications/atlas-v1
sudo cp -a ./application-template/. /etc/caddy/naivefox-applications/atlas-v1/
sudo chown -R root:caddy /etc/caddy/naivefox-applications/atlas-v1
sudoedit /etc/caddy/Caddyfile
# Add application_root and move the existing forward_proxy block as shown above.
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

The native client requires `X-App-Profile: native-stream-v1` and
`X-App-Auth: basic` on the initial `GET /` response before it sends AUTH.
These headers are emitted only for
the root handshake, report the resolved profile even when configuration omits
it, and prevent accidental use of a different credit window. Older experimental
server binaries without the header must be upgraded for native no-connect.
See [docs/PROTOCOL.md](docs/PROTOCOL.md) for the wire contract and lifecycle.

## Configuration and limits

The JSON handler name is `naivefox_transport`. Its required
`application_root` string is an absolute path to a complete public site containing
the seven required transport files and any additional resources. Missing or
relative roots and unreadable, incomplete, symlink-escaping, concurrently changing
or oversized required files fail provisioning. Extra files are not part of that
validation; request-time reads are confined to the root and reject special files.
Its `forward_proxy` object holds the ordinary
forwardproxy options without a second `handler` field. Credentials
must be configured; a missing list or an entirely empty username/password pair
fails validation: the native classic client sends no authentication for an
entirely empty pair. One empty component in JSON is accepted for compatibility;
the Caddyfile keeps the ordinary forwardproxy parser's username rules. Use a
strong password and HTTPS with certificate validation. Keep private
configs, logs, TLS keys and captures outside Git.

For migration from old key-based transport versions, upgrade both server and
client. Remove server `key` and
`allowed_targets`, nest `forward_proxy` as above, and remove client
`no-connect-key`. Keep the proxy URL. Obsolete server settings fail explicitly,
even when empty; they are never ignored. Old key-based servers lack the new
Basic handshake and cannot accidentally receive a new client's credentials.

The omitted `profile` resolves to `native-stream-v1`, with 512 KiB of
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
two minutes without traffic; active transfers and 25-second WS heartbeats
refresh that timer. There is no fixed lifetime limit on active sessions. Byte offsets
wrap modulo 2^32, so a stream is not limited to 4 GiB. Cell sequences and stream
IDs do not wrap/reuse within a session. There is no reconnect or resume.

The supported native contract is `native-stream-v1`. Retired finite profiles
and browser/bridge implementations are available only in Git history.
Tests cover credit, authentication/policy, framing, startup retirement,
WebSocket transfer, half-close, expiry, cancellation and TLS cohosting.

## Maintenance

Keep wire-format, profile, flow-control and routing changes covered by tests.
The CI workflow runs both suites, builds the combined binary, and runs the
TLS cohosting test. That test loads the checked-in Caddyfile and external
template, validates a local certificate without insecure TLS, exchanges
no-connect frames over HTTP/2, and keeps padded classic H1 and H2 CONNECT
tunnels to a distinct target host alive through the same Caddy process. It also
serves additional site files from the single application root, checks live
updates and verifies that static routing preserves the transport snapshot.

Go race tests exercise framing, authorization, external application snapshots,
filesystem validation, replay rejection, concurrent streams, both laboratory
proxy frontends, transfers larger than the credit window, half-close and
cancellation. JavaScript tests retain the separate laboratory browser lifecycle
and response-validation coverage. They do not require Firefox.

The pre-extraction history is preserved. No new license grant is implied by
moving that existing source to a separate repository; dependency licenses remain
their respective authors' licenses.
