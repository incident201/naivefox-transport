# Historical experiment settings

References below to private transport keys and exact target allowlists describe
the original prototype only. Current deployment uses one nested forwardproxy
username/password configuration and shared destination policy. See
[the current README](../README.md) and [protocol contract](PROTOCOL.md).

# Application-capacity transport laboratory

## Hybrid follow-up history preflight

The complete server history and all-ref NaiveFox application-carrier journal
were reviewed before the hybrid implementation. Earlier `continuous-sync`,
`continuous-sync2`, selective bulk duplex, bounded window, paired HTTP pipeline,
prefix delivery, and local ACK variants all retained finite outer HTTP exchanges.
Their local WebSocket was IPC only. None tested an outer WebSocket admitted
after the completed twenty-pair startup with the existing NFC1 streams intact.
The distinct premise is removing repeated active HTTP lifecycles while keeping
the complete startup graph; it does not repeat finite-slot or timing-barrier
tuning. Current Firefox uses HTTP/1.1 WSS after H2/H3 startup, so comparisons
must include that second physical connection and H3-to-TCP transition. The
hybrid is an opt-in native experiment, and historical browser-worker speed or
residual results do not qualify it. See the current protocol for its exact
message capacities, completion barrier and independent flow-control bounds.

Experimental Caddy module and loopback bridge for the NaiveFox application-carrier
campaign. Not a product and not wire-compatible with NaiveProxy. The native
NaiveFox defaults are unchanged. The paired Firefox experiment lives on the dedicated
`experiment/application-carrier-20260830` source branch.

An actual Firefox instance executes the bundled gallery SPA. All outer traffic
uses its ordinary origin HTTP/2 or HTTP/3 stack. There is no outer CONNECT or
WebSocket. A private loopback WSS connection joins that page to a Go bridge with
SOCKS5 CONNECT and HTTP CONNECT listeners. Firefox still owns TLS, HTTP, QUIC,
pooling and packetization. This full-browser worker measures an upper bound; it
is not a lean-runtime implementation.

## Experimental default

Omitting the server `profile` now selects `continuous-bulk-pipeline`: the
20-round startup, continuous active/idle lifecycle and bounded two-transaction
bulk pipeline. No interactive-only duplex, fast filler or enlarged idle-event
variant is enabled. The bridge's omitted `continuous` and `receive_window`
fields default to `true` and `524288`, matching that application. Explicit
legacy values remain authoritative; alternative profiles still require matched
private bridge/server configuration. This is a default only for the new
experimental transport, not a native/full-source product promotion.

## Historical finite application profiles

The v1 job consists of 16 rounds: two interactive, twelve media-download,
two interactive. Each round performs one 4096-byte POST and one finite GET,
followed by a browser animation callback. Interactive GET bodies are 24576 bytes;
media GET bodies are 131072 bytes. Useful frames displace cryptographic filler.
Empty and loaded sessions have identical body capacity. Queue state cannot add
requests, change capacity, postpone an incomplete cell, or advance the lifecycle.
The explicit append ablation keeps those slots but adds frame bytes above filler.

Bootstrap HTML is 4096 bytes, CSS 12288, JS 24576, and four SVGs 8192 each.
These are real executable/renderable assets, but are not themselves payload
carriers in v0. Their 73728 bytes remain startup overhead. Static assets are
cacheable; cold profiles are mandatory for the initial comparison. Root/API
responses are not cached, and carrier bodies must not be content-encoded.
This specific application/module is required: arbitrary existing websites do not
satisfy the protocol. At that earlier stage idle polls and adaptive states were
not implemented; the continuous default below supersedes that limit. A diagnostic round-count override is not the
primary profile.

The original 12-round profile admitted initial H2 transfers, but one repeated
append-ablation run exhausted its usable slots with roughly 117 KiB outstanding.
The incomplete comparison was not scored. V1 increases the declared lifecycle
symmetrically for empty, replacement and append sessions, before new scoring.
Its extra capacity is a real bandwidth cost, not a free optimization.
The root is ASCII for admission by the existing lean H3 control. The append
ablation is selected by private server/bridge configuration, not by a different
wire URL: every outer navigation requests exactly `/`.

The private `profile` configuration selects preregistered cost experiments;
omission selects `continuous-bulk-pipeline`; select `v1` explicitly to reproduce
the original finite job. `duplex-v1` returns the fixed downstream cell in each
POST response. `compact*` reduces media cells to 64 KiB with explicit 16/20
round and animation-cadence variants. `staged*` moves capacity out of startup
and tail slots: four 8-KiB, two 32-KiB, ten (18 rounds) or twelve (20 rounds)
64-KiB, then two 8-KiB responses. `staged-fast20` has 901120 downstream bytes
and 81920 upload bytes; it waits for animation every second round. All visitors
receive the same selected profile in the still-24576-byte script. The brief
and state endpoints are `/api/events/brief` and `/api/events/state`.
Failed profiles remain named to reproduce failures, not as recommended modes.
These finite jobs can exhaust capacity before a workload finishes. Reducing
filler for one workload does not qualify arbitrary size, rate or session length.

`staged-stream20` preserves `staged-fast20`'s complete HTTP graph and capacities,
but forwards a validated used prefix before draining filler. The following slot
still waits for a complete successful body. A truncated tail or malformed frame
aborts the local session; early delivery is never treated as full HTTP success.
The script validates Content-Length, capacity, content encoding, sequence and
frame bounds in both buffered and streaming modes. This reduces a local delivery
barrier, not the outer byte budget. It is opt-in and is not a proven large speed
improvement. Idle/active state transitions remain a separate unsolved problem.

`staged-commit20` adds one 4096-byte `/api/action` upload and 4096-byte response
after the final render callback (48 total HTTP requests, 905216 downstream and
86016 upload-body bytes). It passed initial admission but failed the subsequent
H3 screen: a late inner completion request still lacked a slot. It is a recorded
negative experiment, not a liveness fix. An ongoing application lifecycle is
required before treating reduced finite profiles as a practical transport.

## Continuous application

The final directional-WS screening experiment ports the four finite activity
capacities to `nfc1.hybrid.a1` while retaining generic v1 on the same server.
It first requires codec, state-transition and live H2/H3 correctness gates.
The short matched screen uses the same active Firefox application and fresh
generic/asymmetric arms. A full campaign is not admitted unless asymmetric
reduces complete-session IP bytes by at least 15% and post-startup WS filler by at
least 30% versus generic, while download, upload, and parallel-download
durations are each no more than 10% worse and small/wake latency is no more
than 15% worse. Early packet
views are regression checks because startup is unchanged. Short rows remain
descriptive and are never spliced into a later primary campaign.

The controlled H2 screen stopped this candidate. All six sessions used the
same active application: Firefox A/B and generic/asymmetric arms completed the
same resources, twenty GET/POST bootstrap pairs, eleven proxy jobs, one
application WebSocket, 10,506,240 downloaded bytes, 1,069,056 uploaded bytes,
and a normal application-WebSocket close. An independent TShark recount of
the outer flow matched every reported packet and `ip.len` total exactly.

| Listener | Complete IP vs generic | WS filler vs generic | Download rate loss | Upload rate loss | Parallel rate loss |
| --- | ---: | ---: | ---: | ---: | ---: |
| SOCKS | +19.85% | +31.97% | +46.30% | +13.79% | +28.72% |
| HTTP | +9.39% | +14.87% | +40.97% | +0.90% | +15.62% |

The uplink policy did reduce client-to-server filler by 76--80%, but the
downlink policy increased server-to-client filler by 102--136%. It emitted
107--116 256-KiB messages, versus 40 in each generic arm, often after a
fragmented 16-KiB credit return and before enough useful download data had
accumulated. WS serialization therefore spent the 20-Mbit/s link on the
partially filled message before delivering its DATA/CREDIT frames.

This is one descriptive mechanism block, not residual inference. Both
listeners failed the preregistered traffic, filler, and bulk-speed gate, so
the H3 performance screen and full matrix were not run. The selector remains
opt-in and non-default for reproducibility. Retrying the direction would
require a different causal rule, such as granting 256 KiB only when enough
useful downstream data is already sendable or explicitly coalescing credits.

The opt-in `continuous-v1` profile keeps the `staged-fast20` startup job, then
runs indefinitely. Startup completion is not transport shutdown. A local queue
notification or a server event can grant another four-slot activity lease:

| State | POST capacity | Response capacity |
| --- | ---: | ---: |
| Interactive | 4096 | 8192 |
| Download | 4096 | 65536 |
| Upload | 131072 | 8192 |
| Mixed | 131072 | 65536 |

Each lease keeps its capacities even if queue pressure changes mid-lease.
States are selected at lease boundaries using ready queue pressure and a coarse
server hint, so empty and loaded sessions do not have identical total request
traces. No candidate trace is replayed into a normal visitor reference. The
ordinary refresh button can also wake an idle visitor. Network turnover after
startup does not wait for animation frames.

Idle holds one ordinary `GET /api/events/idle` for at most 30 seconds and always
returns a finite 512-byte cell. New server bytes end that wait immediately.
A local event sends a normal 4096-byte POST while the idle GET is outstanding,
waking it without response reordering or an outer WebSocket. Queue notifications
are coalesced over the existing local WSS IPC; they do not require rapid outer
polling. Only one idle GET is allowed per session. Request cancellation and
peer shutdown release the wait. Empty timeout bodies alone cost 61,440 bytes/hour;
HTTP/TLS/IP/QUIC costs are additional and are measured separately.

The separate activity routes `/api/data/{interactive,download,upload,mixed}`
and existing upload route share the authenticated multiplexer. Idle/active
capacity counters, write errors and cancellation counts support exact accounting.
The source-branch journal contains liveness, idle and performance results;
this remains a full-browser prototype, not a lean-runtime or production default.

The optional `continuous-sync` variant preserves that startup and idle behavior,
but each active slot is one POST `/api/exchange/{state}` with a fixed 200 response,
instead of POST-204 followed by GET. Both body capacities and the four-slot lease
remain unchanged. The same authentication, ordering, credits and cancellation
apply. This isolates active HTTP turnaround cost; it is not a larger body budget
or a speculative concurrent stream. See the source-branch journal for paired
speed/traffic admission before treating it as a preferred experiment profile.

`continuous-sync2` is a separate follow-up: the same combined exchanges and
body capacities, but a fixed two-slot active lease instead of four. This halves
the capacity committed before checking state again (256 rather than 512 KiB
for upload); it does not change per-stream credit or the startup/idle contract.

`continuous-bulk` is a separate equal-budget download experiment based on
continuous-v1. One 16-KiB POST `/api/sync/bulk` followed by one 256-KiB GET
`/api/data/bulk` replaces a four-slot download lease (4/64 KiB per pair).
Interactive, upload and mixed retain their original four pairs; idle and
startup are unchanged. No wait for a full target buffer or larger credit is
introduced. Full-body buffering can delay useful delivery on slower links;
speed and filler utilization must be measured before adopting this profile.

## Framing and limits

`continuous-bulk-pipeline-interactive` combines only active interactive POSTs
with their fixed 8-KiB responses (`/api/exchange/interactive`). It derives from
plain pipeline, retaining four interactive slots and unchanged upload/mixed,
startup, 512-byte idle responses and 512-KiB window. Other combined-exchange
paths and the duplicate interactive GET are unavailable. This isolates the
interactive RTT cost from the earlier all-active duplex upload-tail failure.

The campaign was stopped before its performance test. This interactive-only
profile is implemented and unit-tested but unmeasured; it is not the selected
speed profile and has no residual qualification.

`continuous-bulk-pipeline-events` composes the bounded pipeline and idle
event/heartbeat split. It retains 512-KiB credit, two fixed bulk transactions,
ordered local delivery and at most two HTTP responses. Only idle differs from
pipeline alone. No fast filler/progress probe/deferred ACK is enabled. Fresh
cost/residual evidence is required; old continuous-v1 scores do not qualify it.

`continuous-bulk-idle-events` independently changes only bulk-duplex's idle
API: timeout without observed work returns 204/no cell; activity returns a
fixed 8-KiB cell. The long-poll maximum remains 30 seconds, wake POST 4 KiB,
and startup/active budgets are unchanged. HTTP 204 does not advance codec
sequence. A race at timeout leaves late work for the next poll, not discarded.
This removes carrier-body cost for genuine heartbeat timeouts but can spend
more filler on active wakes. All visitors see the same status/capacity policy.

`continuous-bulk-pair` and `continuous-bulk-pipeline` both derive from window512
and commit to two 16/256-KiB exchanges per bulk lease. The first is serial; the
second starts the next POST after the first response headers and after preparing
its upload, but before delivering the first body. This bounds requests to two,
keeps local IPC and response dispatch ordered, and needs no server reordering.
Both responses finish/cancel before another state decision. Failed work aborts
and drains/cancels the outstanding fetch. Startup/idle/other states are unchanged.
No fast filler, progress hint or deferred ACK is mixed into this experiment.
The pair has twice a single bulk lease's capacity and retains the 512-KiB
window's memory cost; compare to the single-lease candidate before promotion.

`continuous-bulk-progress` adds a productive-cell handoff to bulk-filler:
at least 128 KiB useful data in a bulk cell and a not-yet-finished readable
stream permit one subsequent bulk hint despite an empty instantaneous queue.
An empty or low-use response cannot renew it, and observed FIN/reset cannot
justify it. Actual credits are unchanged. This explicitly risks one 256-KiB
empty tail probe; it tests state decisions coupled to producer/encoder timing,
not a wait or larger window. Separate opportunity/promotion counters are kept.

`continuous-bulk-filler` retains bulk-duplex's original window and profile but
generates random bytes only for the unused suffix. Useful bytes overwrite the
entire prefix; all filler still comes fresh from crypto/rand. Body capacity,
wire codec and allocation size are unchanged. This separately measures CPU
wasted randomizing data that is immediately overwritten, not padding reuse.

`continuous-bulk-window512` changes only bulk-duplex's per-stream credit/budget
from 256 to 512 KiB at both mux peers. Cell capacities, prefetch depth, HTTP
concurrency and ACKs are unchanged. Bridge private `receive_window` configuration
must match the server profile; zero retains 256 KiB, other values except 512 KiB
are rejected. This prototype has no window negotiation. Worst-case additional
outstanding receive bytes are 256 KiB per stream, 8 MiB over 32 streams per peer;
prefetch/in-flight overhead is additional. Memory is spent explicitly, not hidden
behind a state hint. This is an opt-in experiment, not a changed default.

`continuous-bulk-noack` retains bulk-duplex and defers active/idle local
delivery ACKs to the next already-required ordered pressure/take response.
Local opcode 7 decodes/delivers a whole cell without a standalone reply. A
client fence permits only one unacknowledged cell; no following cell may be
delivered until a command reply clears it. Startup and all outer requests/body
capacities are unchanged. Decoder errors close the connection; actual local
socket writes still govern credit. This is private-loopback IPC optimization,
not earlier local SOCKS success or optimistic target delivery.

`continuous-bulk-noack-download` narrows this experiment to the bulk state;
idle, interactive, upload and mixed delivery remain explicitly acknowledged.
This isolates the small-request penalty found with all-active deferred ACKs.

Two independent lease ablations retain bulk-duplex behavior:
`continuous-bulk-interactive1` rechecks state after one interactive slot;
`continuous-bulk-upload1` rechecks after one upload slot. They keep the other
state's four slots, the four-slot mixed lease, single bulk exchange and all
per-slot capacities. Earlier state reevaluation can reduce tails or add local
IPC/state churn; these variants require separate paired measurement.

`continuous-bulk-duplex` independently tests one 16-KiB POST-200 with a
256-KiB response at `/api/sync/bulk`, replacing the bulk-ready POST-204/GET pair.
Other states, credit hints and buffered delivery are unchanged. Unlike the
earlier continuous-sync profiles, upload and interactive slots are not combined.
The separate bulk GET is unavailable in this profile; request and body counters
must match the declared one-request lease. This is not yet a preferred profile.

`continuous-bulk-ready` isolates a server-state hint experiment on top of bulk.
After sending at least 128 KiB useful data in a 256-KiB cell, queued backlog
of at least 32 KiB may preserve one download hint even if currently sendable
credit falls below 32 KiB. The next POST can return credit. An empty or lightly
used response cannot repeat this promotion; backlog never bypasses credit or
keeps idle polling awake by itself. Both profiles record opportunity counters;
only this profile records promotions. No increased buffers, sleeps or window.

Cells have a 16-byte `NFC1` header: big-endian cell sequence, used length,
frame count and reserved zeros. Frames have a 16-byte header: type, reserved
zeros, stream ID, byte sequence, payload length. Types are OPEN, DATA, FIN,
RESET, CREDIT, AUTH and OPENED. Used length excludes filler. Whole-cell framing
and strict per-direction sequence checks precede dispatch, except for the
explicit incremental experiment below.

`continuous-bulk-frames` keeps bulk-ready capacities and state hints but
delivers complete logical frames from a 256-KiB response as they arrive.
Other responses, including startup, keep buffered delivery. Local IPC opcode
6 carries ordered used-prefix fragments and a final marker; the bridge keeps
one decoder of at most 256 KiB and forbids interleaved commands. Cell sequence
advances only on finalization after Firefox validates full HTTP EOF, including
filler. Every complete frame is checked before dispatch and still obeys mux
credit/byte-sequence bounds. Malformed later data or truncated filler closes
the peer, but cannot retract already-delivered bytes. Filler stays out of the
incremental local IPC, without changing any outer body length or request.

Only the first authenticated upload can provide AUTH. A random secure HTTP-only
session cookie plus peer IP binds the session; source ports are not identity.
The server requires a private key of at least 32 bytes and exact target allowlist.
Unauthenticated visitors may synchronize empty cells but cannot dial targets.
The bridge binds only loopback and requires a private URL capability and exact
Origin for WSS. Keep all private configuration, logs and captures outside Git.

Each session has at most 32 active streams, with 512 KiB receive credit each
in the default pipeline (256 KiB in explicit legacy profiles),
16 queued outbound reads of at most 16 KiB each and 128 inbound frame slots.
In-flight plus prefetched bytes mean the total memory bound is larger than the
credit alone. A slow target/client stops credit grants. Byte sequence space is
32-bit: a stream exceeding 4 GiB fails closed. Dial timeout is five seconds;
server sessions expire after two minutes without an HTTP request. No reconnect,
retry/resume, production key management or deployment hardening is promised.
Local proxy success is optimistic; remote dial failure becomes RESET.

## Build and checks

The campaign pins Go 1.25.12, Caddy 2.11.2 and the established Naive forwardproxy
revision. `tools/go.sh` locates the retained WSL fixture toolchain/cache;
`tools/build.sh /absolute/output` builds only the bridge and Caddy. It never
builds Firefox. Those convenience paths are machine-local laboratory paths.

Run `bash tools/go.sh go test -race ./...`. Tests cover exact cell capacities,
malformed lengths/sequences, authorization/replay rejection, four concurrent
streams through both local frontends, byte-exact transfers larger than flow
windows, half-close and cancellation. Network captures and residual scores are
recorded in NaiveFox's `APPLICATION-CARRIER.md`; unit tests do not establish
camouflage quality or a default promotion.

Run `node --test test/*.test.js` for all response split points, strict
framing, prefix-before-EOF, sink backpressure, tail truncation, append handling
and cancellation. The WSL fixture's managed Node is
`/root/.mozbuild/node/bin/node` when Node is not on PATH.
