# Native no-connect contract (NFC1)

This document describes the `native-stream-v2` profile shared by the
Caddy module and the native lean NaiveFox client. Only its current implementation
is supported. Upgrade both peers together; there is no negotiation or compatibility
with earlier releases. Historical variants remain in Git history.

## Origin, session and authentication

1. Connect to the configured HTTPS origin with ordinary certificate validation,
   using native strict HTTP/2 or HTTP/3. The later WebSocket phase uses H1 TLS/TCP.
2. GET / returns actual nonempty UTF-8 index.html and an ordinary random
   Secure/HttpOnly session cookie. No public response contains X-App-* metadata.
   HEAD is supported without creating a session. The cookie is retained on
   carrier requests and bound to the client IP; it is not authentication.
3. Discover all supported directly declared resources from that HTML and complete
   their GETs, at most six at a time. Names, number and body sizes come from the
   site; there is no site-byte budget or space padding. Bodies are streamed.
   Each body is hashed while streaming. Client caching remains inhibited;
   there are no inter-carrier cache hits or shared requests.
4. The first upload contains exactly one AUTH frame and no other frames. Stream and frame sequence
   are zero; its body is ASCII Basic followed by standard padded Base64 of the
   URL-decoded username, colon and password. Credentials and forward-proxy policy
   are the same as classic. AUTH must fit its first 4096-byte cell.
5. The first authenticated GET returns one HELLO frame (type 9, stream and
   sequence zero), containing ASCII native-stream-v2, one LF and the lowercase
   64-character snapshot digest. The client validates the complete response
   and matching digest before OPEN. The server rejects proxy frames until
   that first response completes. There is no version negotiation,
   compatibility alias or separate transport secret.

The [site contract](SITE.md) specifies supported markup, URL and MIME rules,
advisory sizes, immutable in-memory public snapshots, ordinary extra files and
reload behavior. It requires no operator manifest or injected JavaScript.
CSS/script/image bodies are leaves; the client neither recursively crawls
dependencies nor executes a browser application.

Snapshot identity is lowercase SHA-256. Hash length-prefixed fields in discovery
order: the domain string naivefox-site-v2, the 32 raw SHA-256 digest bytes of
the root body, then each deduplicated resource's normalized request URI
(including query, without fragment), kind (style/script/image), MIME type
without parameters and 32 raw SHA-256 digest bytes of its body. Each field is prefixed by its
unsigned 64-bit big-endian byte length. The identity detects mixed snapshots;
it is consistency metadata carried only after authentication. Reload mismatch
fails before OPEN without replay.

AUTH is accepted once per session and compared in constant time. Anonymous
carrier and WebSocket requests, including empty NFC1 cells, malformed uploads
and invalid credentials, receive normal site fallback. They do not advance
cell or startup sequences. No NFC1 response or WebSocket upgrade is available
before successful authentication. Authentication is not a separate HMAC or encryption scheme: TLS
protects the complete body, including credentials and payload. Filler comes from
`crypto/rand`; there is no custom AEAD, payload obfuscation, or key negotiation.

## Cells and frames

All integer fields are unsigned big-endian. A direction has its own cell
sequence, starting at zero and increasing by one for each complete cell,
including empty cells. HTTP 204 upload acknowledgements contain no cell and do
not advance the downstream sequence. A failed HTTP request is not retried with
the same sequence; abort the session because delivery may already have occurred.

| Cell offset | Width | Meaning |
| --- | ---: | --- |
| 0 | 4 | ASCII `NFC1` |
| 4 | 4 | Directional cell sequence |
| 8 | 4 | Used length, **including the 16-byte cell header** |
| 12 | 2 | Number of frames, at most 4096 |
| 14 | 2 | Reserved, must be zero |
| 16 | variable | Consecutive complete frames |
| used | remaining | Fresh random filler to the exact body capacity |

A cell is at most 262144 bytes. Used length is at least 16 and at most body
length. Frames must exactly occupy the used prefix; filler contains no frames.

| Frame offset | Width | Meaning |
| --- | ---: | --- |
| 0 | 1 | Type |
| 1 | 3 | Reserved, must be zero |
| 4 | 4 | Stream ID |
| 8 | 4 | Frame byte sequence |
| 12 | 4 | Payload length |
| 16 | variable | Payload |

| Type | Name | Payload and frame sequence |
| ---: | --- | --- |
| 1 | OPEN | Exact `host:port` string, 1..512 bytes; sequence zero |
| 2 | DATA | Nonempty bytes; sequence is the next byte offset in that direction |
| 3 | FIN | Empty; sequence is the final byte offset |
| 4 | RESET | Empty; sequence zero |
| 5 | CREDIT | Four-byte positive byte grant; sequence zero |
| 6 | AUTH | Basic authorization value; stream zero and sequence zero |
| 7 | OPENED | Empty; sequence zero; server confirms a successful dial |
| 9 | HELLO | Server-only first HTTP cell; stream/sequence zero; fixed contract and snapshot digest |

Stream IDs are nonzero, monotonically increasing and never reused in a session.
OPEN accepts any valid TCP `host:port`; no per-destination allowlist is required.
The single ordinary forwardproxy configuration supplies ACL, ports, upstream
and dial timeout for both transports. The no-connect module evaluates that
public policy configuration with parity tests against the original handler.
An upstream delegates DNS, ACL and port policy instead of filtering locally,
as ordinary forwardproxy does. Forwardproxy defaults to 30 seconds; set
`dial_timeout` to change it. Failed or denied dials produce RESET. A native client must wait for OPENED before
reporting local proxy success.

Both peers start each stream with 524288 bytes of send credit and receive
budget. DATA decrements those counters. CREDIT replenishes send credit only
after bytes were written to the receiving local socket, and cannot exceed the
initial window. FIN is a half-close: remaining data in the opposite direction
continues. RESET aborts the stream. Stream byte offsets wrap modulo 2^32;
exact expected offset equality and bounded credit still apply. A stream is not
limited to 4 GiB. Retired-stream frames may be
ignored, but IDs may not be reused to open a new stream.

At most 32 streams are active per session. The server has 16 queued outbound
reads of at most 16 KiB per stream. Inbound DATA frames coalesce into chunks of
at most 16 KiB under the byte-credit bound, with one ordered FIN slot and a
single wake signal. Tiny wire frames therefore do not exhaust an unrelated
frame-count quota. The default window permits at most 33 allocated inbound
data chunks including the in-flight writer chunk; payload bytes across the
queue and writer never exceed 512 KiB. Credit alone is
not a total memory bound; prefetched/in-flight cells and frame allocations are
additional. Slow readers stop credit replenishment instead of growing a queue
without a bound. The server defaults to 128 sessions (`max_sessions` is
configurable). At capacity, new visitors replace the oldest unauthenticated
session; authenticated sessions are never evicted. Sessions expire after two
minutes without traffic. Startup requests and WebSocket messages refresh this
timer, so active sessions have no fixed lifetime limit.

The round-robin scheduler retains only active stream IDs. RESET and completed
half-closes retire entries immediately; repeated short connections cannot grow
the scheduler while the client omits downstream reads. Removing an entry keeps
the next surviving stream's turn.

Upload bodies are read and decoded within their fixed size limit before taking
the session mutex. A stalled body cannot hold session expiry or global cleanup
locks. Expired or cancelled uploads are rejected without authenticating or
advancing the cell sequence; sequence validation remains atomic with dispatch.

## Startup and persistent carrier

Only current `native-stream-v2` is supported. Public resources carry no
transport declaration. The first POST authenticates and the first GET confirms
the contract and snapshot; target opening starts with the second POST. Root and all HTML-selected resources complete before the twenty ordered
POST/GET pairs. Each POST to `/api/sync` is exactly 4096 bytes; each GET uses
the next fixed response slot:

- four 8192-byte `/api/events/brief` responses;
- two 32768-byte `/api/events/state` responses;
- twelve 65536-byte `/media/chunk/{round}` responses;
- two final 8192-byte `/api/events/brief` responses.

Useful frames displace fresh cryptographic filler after the first pair. Complete HTTP status,
body capacity, encoding, cell sequence and frame bounds are mandatory.
There are no post-startup finite leases, bulk HTTP pipelines or idle long polls.

After all forty carrier requests complete successfully, the client opens
`/api/realtime` with `nfc1.stream.v1`. The endpoint requires HTTP/1.1 TLS,
the existing session cookie and matching HTTPS Origin. The client uses native
H1 WSS/TCP after H2 or H3 startup. This is an explicit phase transition, not
a fallback. No HTTP carrier requests can resume after transition, and a
second WebSocket is rejected. HTTP and WS share their sequences and mux.

## WebSocket bounds

A binary message contains exactly one complete NFC1 cell. Reserved header
bytes stay zero; peer-pressure hints are no longer part of the protocol.
Text, compression, unknown capacities, malformed frames or wrong sequences
close the carrier. Caddy/Gorilla own HTTP, TLS and WebSocket framing.

| Direction | No payload | Small payload | Medium grant | Large grant |
| --- | ---: | ---: | ---: | ---: |
| Client to server | 512 B; OPEN uses 4096 B | 4096 B | 16384 B at 8192 B ready | 131072 B at 65536 B ready |
| Server to client | 512 B | 8192 B | 65536 B at 32768 B ready | 262144 B at 131072 B ready |

Only currently sendable bytes within stream credit count. A partial payload
coalesces for 2 ms, then capacity is checked again. Full selected capacity
and control work dispatch immediately. There is one bounded writer; a
response is encoded exactly once. No data is replayed after write failure.

Idle heartbeats use 512-byte cells after 25 seconds. Input has a 75-second
deadline and writes a 30-second deadline. Empty heartbeats do not cause
acknowledgement loops. Native client PING/PONG write completions do not
consume its single NFC1 application-message budget.

WS ACK (kind 8) has stream zero, empty payload and the last fully applied
uplink cell sequence. It is cumulative and confirms FIN retirement; per-stream
CREDIT still follows actual local socket delivery. Future, decreasing,
malformed or HTTP-carried ACKs fail closed. Clients never send ACK.
AUTH is permitted only in startup, not after the transition.

Each carrier multiplexes up to 32 streams with 512-KiB per-stream credit.
The client upload buffer is at most 256 KiB per stream. Byte offsets wrap
modulo 2^32; stream byte counts have no fixed 4-GiB ceiling. Native WS ingress
is capped at 32 callbacks and 2 MiB, and its PONG queue at 32.

Both stream half-closes, resets and shutdown are propagated. Server expiry
and cleanup close WS and its logical peers. There is no transparent reconnect,
session resumption or credential replay.

## Configuration and diagnostics

Classic remains the client's default and uses the unchanged ordinary
forward-proxy implementation. No-connect is the only alternate transport.
Old finite HTTP profiles and the hybrid/asymmetric subprotocols are rejected.
Both transports share one listener and one `application_root`: the complete
public site containing index.html, its selected resources and all additional
resources. No separate site `root` or `file_server` is needed. See the
[directory layout and complete Caddyfile](../README.md#site-directory-and-caddyfile).
Omit `profile` or set `native-stream-v2`.

Diagnostics are disabled unless explicitly enabled. Private statistics
retain bounded request labels, directional cell counts, useful/filler byte
counts, startup/WS lifecycle and mux counters. Unknown method/protocol labels
are folded into bounded categories and media IDs into one wildcard label.
Counters never include credentials, cookie values or payload bytes.
