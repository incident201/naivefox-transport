# Default HTTPS packet delivery

https:// is the default NaiveFox delivery, directly to the origin or through a
compatible CDN. Client and server are updated together; only the current
matching implementation is supported. wss:// explicitly selects WebSocket
and quic:// selects direct HTTP/3.

## Configuration and identity

One current NaiveFox application protocol has explicit delivery adapters.
The server supports explicit WSS and QUIC as well as default HTTPS packet
delivery. HTTPS requires
a dedicated inner-TLS certificate/key:

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

Keep the inner private key on the origin, outside the CDN and public site.
It must be different from a key entrusted to an edge. For example:

~~~sh
umask 077
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes   -days 3650 -keyout inner.key -out inner.crt -subj "/CN=NaiveFox Origin"
openssl x509 -in inner.crt -pubkey -noout |
  openssl pkey -pubin -outform DER |
  openssl dgst -sha256
~~~

Distribute the printed 64-hex SPKI hash directly with the credentials. The
existing client proxy field becomes
https://username~PIN:password@proxy.example:443. The client extracts the final
tilde suffix from the decoded username; it sends the original username inside
inner TLS. No new client JSON field is required. Android wrappers must pass
through https:// and the username suffix. The PIN is mandatory even when no
CDN is used. For wss://, the client removes and ignores the final tilde suffix,
without validating it; the server receives only the authentication username.
quic:// retains ordinary username semantics. Pin/key rotation requires
coordinated client configuration updates. The inner certificate must remain
time-valid.

Outer H2/HTTPS remains native Necko/NSS with normal edge-certificate validation.
Inner TLS 1.3 is NSS on the client and Go crypto/tls on the origin, with pinned
SPKI authentication, forward-secret handshakes, no tickets and no early data.
The inner handshake completes before AUTH. The TLS exporter uses label
EXPORTER-NaiveFox-packet and context naivefox/https. Its material authenticates
operations, counters, exact ciphertext and responses. It is never sent in the
clear. The visible opaque routing ID and ordinary HTTP cookies are not
authentication. Packet sessions are independent of source and forwarded IPs.

The CDN can see request names, lengths and timing or deny delivery. It cannot
read credentials, destination OPEN frames or payload, or forge an accepted
frame/acknowledgement. The WSS and QUIC adapters protect their outer
connection with TLS; selecting wss:// through a TLS-terminating intermediary
does not add this packet adapter's inner protection.

## Delivery contract

Public HTML and selected resources are still consumed and hashed. Encrypted
HELLO binds the exact snapshot, https selection and session ID before OPEN.
Packet startup uses two finite POST exchanges for inner TLS and AUTH/HELLO.
WSS and QUIC startup keep their twenty ordered pairs.

- POST /api/packet: a 32-byte client nonce and TLS ClientHello. The finite
  response carries the origin-issued ID and server flight. Exact retries
  return the retained response; a conflicting nonce/body is rejected.
- POST /api/packet/{id}/auth: TLS Finished and the framed AUTH cell. The
  exporter MAC binds the ID, acknowledged server-flight cursor and body.
  The finite response includes an authenticated cursor and encrypted HELLO.
- POST /api/packet/{id}/upload/{sequence}: one completed ciphertext block,
  at most 64 KiB, with known Content-Length. NaiveFox-Cursor and NaiveFox-MAC
  authenticate the request. The response acknowledges the contiguous
  accepted prefix with an authenticated counter; HTTP status alone is not ACK.
- GET /api/packet/{id}/download?generation=N&cursor=N: a signed streaming GET.
  Each item contains an eight-byte sequence, four-byte length, 32-byte MAC and
  at most 64 KiB of ciphertext. HTTP chunks are arbitrary. A new signed
  generation replaces the previous download.

Interrupted or timed-out finite POST body reads return HTTP 503. No partial
body is accepted and no cursor advances, so the client retries the exact signed
operation. Authentication or MAC failures remain fatal; a read failure is not
an authentication rejection.

Inside TLS, a four-byte length precedes each unchanged NFOX cell. Shared
OPEN/DATA/CREDIT/FIN/RESET handling multiplexes at most 32 target streams.
The packet adapter uses 1-MiB per-stream credit, replenished after local delivery.

Uploads have eight slots and a 512-KiB byte bound. A successful authenticated
reply confirms storage of the named block as well as the cumulative accepted
prefix. The client retains a receipted out-of-order block until that prefix
advances, without retransmitting its body or freeing its window slot. Original
ciphertext is retained for missing-receipt retries; out-of-order arrival never
duplicates a TLS byte.
Downstream replay holds at most 2 MiB and 128 blocks per session and applies
backpressure when full. Only an authenticated cursor releases retained data.
If a reconnect needs data older than the retained floor, the session ends.
It never silently recreates a target connection or skips bytes.

There are at most 32 packet sessions, sixteen provisional sessions, 64 aggregate
HTTP handlers and twelve active handlers per packet session. max_sessions can
further restrict admission. Setup has a 30-second deadline, client retries and
download recovery a 30-second bound, and sessions expire after 90 seconds
without fresh authenticated client progress. Old request replays cannot renew
that lease. The downlink emits an encrypted heartbeat every five seconds.

## Direct and CDN deployment

Use normal verified HTTPS to the origin. The same https:// client URI and
mandatory inner SPKI pin apply to direct and CDN endpoints. Configure the
separate outer certificate normally through Caddy.

For intermediary requirements and provider acceptance, see [CDN.md](CDN.md).
Correctness, native Linux/Windows/Android verification and short
speed/latency/five-window screens precede a long comparison campaign.

The client always completes each POST with a known body length. An intermediary
may remove Content-Length or reframe the finite origin request as HTTP/1.1
chunked. The origin still reads a bounded body to successful EOF before
accepting it; size and read deadlines reject unfinished or oversized uploads.

Packet upload cell selection accounts for framing and includes an 8-KiB
capacity, so a 4-KiB application write does not need a second POST merely for
its headers. The packet downlink batches ready ACK, CREDIT and payload for a
fixed interval of at most 2 ms; a full cell bypasses that wait. This prevents
small control writes from consuming the idle TCP congestion window before the
reply. The cell length and body are written to inner TLS together. Direct
carrier scheduling remains unchanged.

The configured max_sessions is shared across direct and packet admission.
The public unauthenticated visitor slot is transferred to packet setup when
possible. Expiry and module shutdown release reservations; setup after shutdown
is rejected.
