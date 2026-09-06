# Native no-connect contract (NFC1)

This document describes the `native-stream-v1` profile shared by the
Caddy module and the native lean NaiveFox client. Other profiles are laboratory
variants documented in [EXPERIMENTS.md](EXPERIMENTS.md); they are not negotiated
or silently substituted. `append_mode` must be false for the native client.

## Origin, session and authentication

1. Connect to a configured HTTPS origin with ordinary certificate validation,
   using the client's normal HTTP/2 or HTTP/3 stack. No CONNECT, outer WebSocket,
   browser JavaScript runtime, or private loopback bridge is needed by the
   native implementation.
2. `GET /` returns 200 and exactly 4096 bytes of padded UTF-8 HTML loaded
   from the configured external `application_root`, plus
   `X-App-Profile: native-stream-v1` and `X-App-Auth: basic`. The native
   client rejects missing or different values before AUTH. The server sets a random 32-byte token,
   hex-encoded as the `app_session` cookie, with Path=/, Secure, HttpOnly and
   SameSite=Strict. Retain that cookie on all carrier requests. The server binds
   it to the peer IP, not a connection's source port. Redirecting to another
   origin or losing the cookie is not a transparent session restart.
3. Fetch the ordinary assets without executing them: `/assets/site.css`
   (12288 bytes), `/assets/app.js` (24576 bytes), and
   `/assets/image-{1,2,3,4}.svg` (8192 bytes each). With the root, cold bootstrap
   is 73728 response-body bytes. Assets are cacheable; root and carriers are not.
4. The first client upload contains AUTH as its first frame. Stream and frame
   sequence are zero; its payload is ASCII `Basic ` followed by standard padded
   Base64 of the URL-decoded proxy username, a colon, and proxy password. These
   are the same credentials as classic; no separate key exists. The complete
   AUTH body must fit the first cell (4064 bytes available after headers).
   Additional frames may follow AUTH when capacity permits.

The application directory is not part of the NFC1 codec. During provisioning,
Caddy reads and validates the fixed seven public files twice, requires two
identical complete snapshots, pads every response to the capacities above, and
retains one immutable memory snapshot. Missing, relative, unreadable, malformed,
special, symlinked, concurrently changing or oversized bundles fail
provisioning. Requests for these seven transport resources never read their
source files from disk. Additional ordinary site resources are served on
GET/HEAD directly from the same `application_root`, without creating transport
sessions or changing NFC1. They are read on each request and have ordinary
static HTTP semantics; their contents and sizes are outside the fixed transport
contract. Existing transport/diagnostic URLs retain priority over files, and
`/index.html` redirects to the snapshot at `/`. Changes to the seven required
files or replacement of the root directory require reload/restart.
The production JavaScript is served verbatim before padding and has no injected
profile, NFC1 runtime, carrier endpoint names or required markers. File contents
may be customized, while the paths, capacities, MIME types and root resource
references remain fixed. See [the template contract](../template/README.md).

AUTH is accepted once per session and compared in constant time. Empty
unauthenticated cells are permitted for ordinary visitors; they cannot open
streams. Authentication is not a separate HMAC or encryption scheme: TLS
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

The supported profile is `native-stream-v1`. The client verifies the root
profile, Basic authentication contract and WebSocket capability before AUTH
or target opening. Root and six assets complete before the twenty ordered
POST/GET pairs. Each POST to `/api/sync` is exactly 4096 bytes; each GET uses
the next fixed response slot:

- four 8192-byte `/api/events/brief` responses;
- two 32768-byte `/api/events/state` responses;
- twelve 65536-byte `/media/chunk/{round}` responses;
- two final 8192-byte `/api/events/brief` responses.

Useful frames displace fresh cryptographic filler. Complete HTTP status,
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
public site containing the seven required transport files and all additional
resources. No separate site `root` or `file_server` is needed. See the
[directory layout and complete Caddyfile](../README.md#site-directory-and-caddyfile).
Omit `profile` or set `native-stream-v1`.

Diagnostics are disabled unless explicitly enabled. Private statistics
retain bounded request labels, directional cell counts, useful/filler byte
counts, startup/WS lifecycle and mux counters. Unknown method/protocol labels
are folded into bounded categories and media IDs into one wildcard label.
Counters never include credentials, cookie values or payload bytes.
