# NaiveFox transport for Caddy

NaiveFox Transport is a research project created entirely with AI. This covers all
project-specific code and documentation; upstream dependencies retain their
original authorship and licenses.

This module is the server for the single current NaiveFox transport. Update
client and server together. Only the current matching pair is supported;
explicit delivery adapters share the current wire contract without version
fallbacks or old-client compatibility. CONNECT is not prohibited
by the architecture; it is simply not needed for this carrier.

Firefox clients use native Necko/NSS/Neqo. The server uses Caddy's HTTP/TLS/QUIC
stack. https:// is the default: finite HTTP/2 POSTs, a resumable streaming GET
and pinned inner TLS, directly or through a compatible CDN. wss:// explicitly
selects WebSocket after H2 startup. quic:// keeps startup and sustained delivery
on HTTP/3, with one downstream GET and at most eight finite upload POSTs.
There is no automatic fallback.

## Build and configure

Build Caddy with this module:

~~~sh
xcaddy build --with github.com/incident201/naivefox-transport
~~~

The module owns its authentication, destination policy and transport.

~~~caddyfile
proxy.example {
    route {
        naivefox_transport {
            application_root /srv/naivefox-site
            basic_auth username password
            packet_tls /etc/naivefox/inner.crt /etc/naivefox/inner.key
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
| packet_tls CERT KEY | Inner certificate/key required by the default https:// packet delivery |

Explicit access rules precede the default private-network exclusions and final
allow rule. Domain matching is case insensitive and accepts a terminal DNS dot.
A configured upstream owns destination resolution and policy. Unknown options
and duplicate singleton options are errors. Credentials are never optional.

The JSON handler has application_root, access, max_sessions, stats_path and
diagnostics, packet_certificate and packet_key fields. Its access object contains credentials (username/password
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

For WSS and QUIC delivery, only one persistent carrier may attach to an authenticated
session after its twenty-pair startup. Unauthenticated requests fall through to ordinary site
handling. TLS protects credentials and payload; unused cell suffixes use fresh
cryptographic randomness.

The default client URI is https://username~PIN:password@proxy.example:443.
PIN is the 64-hex SHA-256 SPKI fingerprint of the dedicated inner certificate.
It is mandatory for HTTPS even on a direct connection. On wss:// the client
strips and ignores the optional final username tilde suffix; quic:// retains
ordinary username semantics. The server basic_auth username never includes the
HTTPS/WSS PIN suffix.

Create the inner identity and distribute its pin as described in
[docs/HTTPS.md](docs/HTTPS.md). packet_tls may be omitted only when serving WSS
or QUIC exclusively. CDN production acceptance is a separate deployment gate;
see [docs/CDN.md](docs/CDN.md).

See [docs/PROTOCOL.md](docs/PROTOCOL.md) for wire details and
[docs/SITE.md](docs/SITE.md) for the public site. Tests cover authentication,
destination policy, cells, mux, bounded queues, half-close and H3 upload ordering.
Run go test ./... and the matching native client integration checks before
acceptance. Keep generated certificates, profiles, captures and reports outside
the source tree in a dedicated temporary catalog.
