# NaiveFox transport for Caddy

This module is the server for the single current NaiveFox transport. Update
client and server together. Classic NaiveProxy, older wire versions, profile
selection and migration fallbacks are not supported. CONNECT is not prohibited
by the architecture; it is simply not needed for this carrier.

Firefox clients use native Necko/NSS/Neqo. The server uses Caddy's HTTP/TLS/QUIC
stack. Strict H2 startup is followed by native WSS/TCP. Strict H3 continues over
QUIC using one persistent downstream GET and at most eight concurrent finite
upstream POSTs. H3 never switches to WebSocket/TCP.

## Build and configure

Build Caddy with this module:

~~~sh
xcaddy build --with github.com/incident201/naivefox-transport
~~~

The module has no dependency on the classic forwardproxy handler.

~~~caddyfile
proxy.example {
    route {
        naivefox_transport {
            application_root /srv/naivefox-site
            basic_auth username password
        }
    }
}
~~~

Provide a real public HTML site with supported stylesheet, script and image
resources. The client consumes those files without executing JavaScript, then
authenticates inside the encrypted carrier. Credentials come from its proxy
URI. The public site exposes no transport metadata or credentials.

Supported handler options:

| Option | Meaning |
| --- | --- |
| application_root | Absolute public application directory |
| basic_auth USER PASSWORD | One or more credential pairs |
| allow SUBJECT... | Ordered allow rule: domain, wildcard, IP, CIDR or all |
| deny SUBJECT... | Ordered deny rule using the same subjects |
| ports PORT... | Optional destination port restriction |
| dial_timeout DURATION | Positive dial timeout; default 30s |
| upstream URL | Optional ordinary HTTP/HTTPS CONNECT or SOCKS5 upstream |
| max_sessions N | Positive session bound; default 128 |
| stats_path PATH | Private aggregate statistics written on cleanup |
| diagnostics | Enable authenticated /__lab/stats for controlled diagnostics |

Explicit access rules precede the default private-network exclusions and final
allow rule. Domain matching is case insensitive and accepts a terminal DNS dot.
A configured upstream owns destination resolution and policy. Unknown options
and duplicate singleton options are errors. Credentials are never optional.

The JSON handler has application_root, access, max_sessions, stats_path and
diagnostics fields. Its access object contains credentials (username/password
objects), acl (subjects/allow rules), allowed_ports, dial_timeout and upstream.

## Bounds and lifecycle

The client and server share one NFOX cell contract, bounded stream credit and
32 streams per carrier. Credit is returned after actual local socket delivery.
Additional carriers permit more streams. FIN closes one direction and RESET
aborts a stream; offsets wrap modulo 2^32.

H3 downstream cells carry a four-byte length prefix. Upload requests may arrive
out of order but finish successfully only after in-order application. The
server bounds active bodies and retained sequences. A failed or canceled
request ends the carrier without application replay.

Only one persistent carrier may attach to an authenticated session after its
twenty-pair startup. Unauthenticated requests fall through to ordinary site
handling. TLS protects credentials and payload; unused cell suffixes use fresh
cryptographic randomness.

See [docs/PROTOCOL.md](docs/PROTOCOL.md) for wire details and
[docs/SITE.md](docs/SITE.md) for the public site. Tests cover authentication,
destination policy, cells, mux, bounded queues, half-close and H3 upload ordering.
Run go test ./... and the matching native client integration checks before
acceptance. Keep generated certificates, profiles, captures and reports outside
the source tree in a dedicated temporary catalog.
