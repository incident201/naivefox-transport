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

## Framing and limits

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
