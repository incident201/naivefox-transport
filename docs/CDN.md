# CDN deployment

NaiveFox Transport is a research project created entirely with AI. This covers all
project-specific code and documentation; upstream dependencies retain their
original authorship and licenses.

CDN is an optional deployment of the default [HTTPS packet transport](HTTPS.md).
Use https://user~PIN:password@host:443 whether the endpoint is direct or proxied.
The origin keeps its inner private key; the intermediary is not trusted with
credentials or target payloads.

## Edge requirements

- Client-facing HTTP/2 over verified HTTPS.
- Verified HTTPS to the origin on port 443, with the expected Host and SNI.
  Origin HTTP/1.1 or H2 is sufficient. Forwarding to an HTTP redirect endpoint
  can cause a loop back to the public HTTPS URL.
- Completed finite binary POSTs and incrementally forwarded streaming GETs.
  WebSocket and a streaming request body are not required.
- Cache bypass for /api/packet and /api/packet/*, preserving method, query,
  headers and body. Cookie/query cache variation alone is not cache bypass.
- No response transformations or browser challenges.
- Stable origin-process routing for each session. Edge IP changes are allowed;
  authentication does not trust source or forwarded IPs.

Responses use no-store, no-transform and X-Accel-Buffering: no. Verify that the
provider honors the required streaming/cache behavior. A provider buffering an
entire live GET cannot carry this delivery. Public-site hashing rejects
transformed or inconsistent bootstrap resources.

## Acceptance boundary

Provider-specific production acceptance remains incomplete. Local fixtures
and short live probes do not prove long-term streaming, loss recovery,
all-edge behavior or performance. Verify idle/lifetime limits, request-size
limits, prefix delivery and routing before accepting a deployment.
Origin restart or migration cannot preserve existing target TCP connections.
