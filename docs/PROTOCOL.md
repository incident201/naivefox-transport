# Native no-connect contract (NFC1)

This document describes the `continuous-bulk-pipeline` profile shared by the
Caddy module and the native lean NaiveFox client. Other profiles are laboratory
variants documented in [EXPERIMENTS.md](EXPERIMENTS.md); they are not negotiated
or silently substituted. `append_mode` must be false for the native client.

## Origin, session and authentication

1. Connect to a configured HTTPS origin with ordinary certificate validation,
   using the client's normal HTTP/2 or HTTP/3 stack. No CONNECT, outer WebSocket,
   browser JavaScript runtime, or private loopback bridge is needed by the
   native implementation.
2. `GET /` returns 200, 4096 bytes of ASCII HTML, and
   `X-App-Profile: continuous-bulk-pipeline` and `X-App-Auth: basic`. The native
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
reporting local proxy success; the historical Go bridge was optimistic.

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
minutes without an HTTP request. Active requests and idle polls refresh this
timer, so active sessions have no fixed lifetime limit.

The round-robin scheduler retains only active stream IDs. RESET and completed
half-closes retire entries immediately; repeated short connections cannot grow
the scheduler while the client omits downstream reads. Removing an entry keeps
the next surviving stream's turn.

Upload bodies are read and decoded within their fixed size limit before taking
the session mutex. A stalled body cannot hold session expiry or global cleanup
locks. Expired or cancelled uploads are rejected without authenticating or
advancing the cell sequence; sequence validation remains atomic with dispatch.

## Startup

Each startup round sends one 4096-byte `POST /api/sync` (204, empty response),
then one finite GET (200) with the following response capacity:

| Zero-based rounds | GET path | Bytes per response |
| --- | --- | ---: |
| 0..3 | `/api/events/brief` | 8192 |
| 4..5 | `/api/events/state` | 32768 |
| 6..17 | `/media/chunk/{round}` | 65536 |
| 18..19 | `/api/events/brief` | 8192 |

This commits 81920 upload-body bytes and 901120 download-body bytes, in
addition to assets. Payload replaces filler without changing body capacity.
Startup completion enters the continuous lifecycle; it does not close the
session. The browser experiment's rendering callbacks are not native runtime
dependencies or a claim of identical browser timing.

## Optional realtime transition

The same profile also advertises `X-App-Realtime: websocket-v1` on the root.
A `no-connect-hybrid` client finishes the root, assets and all twenty startup
pairs, then opens a genuine WebSocket at `/api/realtime` with subprotocol
`nfc1.hybrid.v1`, the existing session cookie and a same-origin HTTPS Origin.
The current native Firefox implementation opens a new HTTP/1.1 TLS/TCP route;
H3 remains the startup protocol only. This is an explicit transport policy,
not fallback from an H3-only no-connect session. No CONNECT tunnel is used.

The server requires forty successfully completed requests in the declared
alternating startup order, no invalid startup request, and no outstanding
carrier HTTP request. After claiming the session, a second WebSocket and all
further carrier HTTP requests are rejected. A failed upgrade is terminal.
The client must separately verify complete asset responses before the transition;
the server does not require cacheable assets to be fetched again. HTTP and WS
share the existing directional cell counters and logical mux without resetting
sequences, credentials, stream IDs or credit.

Each binary WebSocket message contains exactly one complete NFC1 cell of 512,
65536 or 262144 bytes. Text, compressed, oversized and malformed messages fail
the session. Gorilla supplies RFC 6455 framing, masking validation, fragmentation
and close/control handling; Caddy supplies HTTP and TLS. There is one reader and
one bounded application writer, no unbounded message queue. Reading is limited
to one 256-KiB message before cell/mux dispatch. Existing mux input, output,
32-stream and 512-KiB byte-credit limits remain unchanged.

The server selects 256 KiB with at least 128 KiB of currently sendable bytes,
64 KiB with other ready data, and 512 bytes for controls or idle. When fewer
than 256 KiB are sendable, it coalesces nonempty data for 2 ms before selecting
capacity. At least 256 KiB of ready data bypasses that delay; blocking writes
still provide backpressure. This does not control the startup transition.
Useful bytes displace fresh cryptographic filler. A 25-second
idle timer supplies empty 512-byte cells, without returning to HTTP. Complete
messages refresh session activity; stalled input has a 75-second deadline and
stalled writes a 30-second deadline. Session expiry and module cleanup close
the WebSocket and its logical streams.

WS adds frame type 8, ACK: stream zero, empty payload, frame sequence equal to
the latest completely applied client cell sequence. ACK is cumulative and sent
only after a client cell containing nonempty application frames. One pending
ACK scalar replaces older pending values and is carried in the next fixed
capacity response, including a 512-byte control response. The client rejects
future or decreasing acknowledgements and waits for the FIN cell's ACK before
retiring its local stream. Empty heartbeat cells are not acknowledged. Clients
never send ACK; HTTP and mux application-frame dispatch reject it. ACK means
cell application, not local socket delivery: CREDIT still follows actual local
writes, and FIN still preserves the opposite direction.

Authenticated startup permits ordinary OPEN/DATA/CREDIT/FIN/RESET frames over
WS. AUTH and re-authentication are forbidden after upgrade. Anonymous ordinary
visitors may complete empty startup and enter an empty realtime lifecycle, but
any nonempty client frame list closes their WS before opening a target. This
supports the gallery's `#realtime` visitor path without putting proxy credentials
in the page. There is no resume, retransmission or HTTP fallback after failure.

### Asymmetric realtime experiment

The optional `nfc1.hybrid.a1` subprotocol has the same byte length as the
generic `nfc1.hybrid.v1` token and shares its startup, session, mux, credit and
failure contracts. Generic v1 keeps the 512/65536/262144 message policy above.
The asymmetric variant uses NFC1 header byte 14, which is reserved and zero in
HTTP and generic v1 cells, as a residual pressure hint: 0 idle, 1 interactive,
2 bulk. Byte 15 remains zero. Ordinary HTTP decoding rejects every nonzero
hint, and asymmetric decoding rejects unknown hints.

The hint describes sendable pressure remaining after the sender extracted the
current cell. It affects only the next message already needed for DATA, CREDIT,
FIN, RESET, ACK, OPEN, or heartbeat work. A hint never creates a message by
itself, avoiding a self-sustaining filler loop. A 32768-byte threshold
separates bulk from interactive pressure.

| Activity | Client to server | Server to client |
| --- | ---: | ---: |
| Download | 16384 | 262144 |
| Upload | 131072 | 8192 |
| Interactive | 4096 | 8192 |
| Mixed | 131072 | 65536 |
| Idle heartbeat | 512 | 512 |

Local bulk plus peer bulk selects mixed. Client-only bulk selects upload;
server-only bulk selects download. Otherwise either interactive side selects
interactive, then idle. The server computes its residual hint after `Take`
using the existing continuous-pipeline downstream state, including its single
productive credit handoff. Uplink capacity identifies the current client
activity for the immediate response; the carried hint describes later work.
The 2-ms coalescing turn remains for partial payload and OPEN, but fully ready
directional capacity and pure control work dispatch immediately.

## Continuous lifecycle

At lease boundaries, use ready local bytes/control frames and the latest
server `X-App-State` hint (`idle`, `interactive`, or `download`). At least 32768
ready local bytes selects upload, or mixed when the remote hints download.
Otherwise a download hint selects bulk; any local data/control or interactive
hint selects interactive. With neither, idle. Fix the selected capacity for
the entire lease before checking pressure again.

| State | Lease | Request(s) per slot | Response capacity |
| --- | ---: | --- | ---: |
| Interactive | 4 slots | POST `/api/sync`, 4096 bytes, 204; GET `/api/data/interactive` | 8192 |
| Upload | 4 slots | POST `/api/upload/chunk`, 131072 bytes, 204; GET `/api/data/upload` | 8192 |
| Mixed | 4 slots | POST `/api/upload/chunk`, 131072 bytes, 204; GET `/api/data/mixed` | 65536 |
| Bulk | 2 slots | POST `/api/sync/bulk`, 16384 bytes, 200 | 262144 |

The two bulk transactions may overlap: start the second POST only after the
first response headers, then decode/deliver the two full responses in their
original order. Both finish or are cancelled before another lease decision.
At most two HTTP responses are outstanding. Do not make multiple unordered
GETs: the server's downstream cell sequence is assigned when producing bodies,
not by the client's request order.

Idle keeps one `GET /api/events/idle` outstanding for at most 30 seconds. It
returns a finite 512-byte cell, even on timeout. New server bytes/control frames
finish it immediately. New local work sends a 4096-byte POST `/api/sync` while
the idle GET is outstanding, then drains that idle response before starting the
next lease. Cancellation releases the poll. There is at most one idle GET per
session. The selected profile has no 204 idle heartbeat optimization.

## HTTP validation and failure handling

Carrier responses have `Content-Type: application/octet-stream`, exact decimal
`Content-Length` and `X-App-Capacity`, and `Cache-Control: no-store`. No content
encoding is allowed. Validate status, declared capacity, complete EOF, actual
length, cell sequence, reserved fields and every frame boundary before buffered
dispatch. Do not silently accept truncation, appended bytes, unexpected profile
capacities, or a 204 where a cell was required.

Any HTTP envelope, sequence, authentication or flow-control failure aborts the
session and closes affected local connections. There is no automatic retry,
reconnect, stream migration or resume. Unit and integration tests establish
correctness of these checks; they do not establish network camouflage.

## Bounded diagnostic labels

The private diagnostic counters keep static route names, aggregate all media
chunk suffixes as `/media/chunk/*`, and retain only `GET`, `POST` or `OTHER`
method labels. CONNECT has its separate counter. Protocol labels are
`HTTP/1.0`, `HTTP/1.1`, `HTTP/2.0`, `HTTP/3.0` or `OTHER`. This bounds metric
storage even for unauthenticated requests with arbitrary methods and paths.
Routing, statuses and response bodies are unchanged. Historical research tools
that expected one metric key per numbered chunk must use the aggregated key;
the private counters are not part of the client wire contract.
