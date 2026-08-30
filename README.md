# Application-capacity transport laboratory

Experimental Caddy module and loopback bridge for the NaiveFox application-carrier
campaign. Not a product, not wire-compatible with NaiveProxy, and not the current
NaiveFox default. The paired Firefox experiment lives on the dedicated
`experiment/application-carrier-20260830` source branch.

An actual Firefox instance executes the bundled gallery SPA. All outer traffic
uses its ordinary origin HTTP/2 or HTTP/3 stack. There is no outer CONNECT or
WebSocket. A private loopback WSS connection joins that page to a Go bridge with
SOCKS5 CONNECT and HTTP CONNECT listeners. Firefox still owns TLS, HTTP, QUIC,
pooling and packetization. This full-browser worker measures an upper bound; it
is not a lean-runtime implementation.

## Fixed application profile

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
satisfy the protocol. Idle polls, upload lifecycle and adaptive states are not
implemented or qualified yet. A diagnostic round-count override is not the
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
omission remains `v1`. `duplex-v1` returns the fixed downstream cell in each
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
and strict per-direction sequence checks precede dispatch.

Only the first authenticated upload can provide AUTH. A random secure HTTP-only
session cookie plus peer IP binds the session; source ports are not identity.
The server requires a private key of at least 32 bytes and exact target allowlist.
Unauthenticated visitors may synchronize empty cells but cannot dial targets.
The bridge binds only loopback and requires a private URL capability and exact
Origin for WSS. Keep all private configuration, logs and captures outside Git.

Each session has at most 32 active streams with 256 KiB receive credit each,
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
