# CDN deployment contract

**Status: work in progress; not validated for production use. CDN integration
is currently deferred.** Direct connections using H2 or H3 are the supported
deployment. Local TLS reverse-proxy checks do not establish compatibility with
a real CDN; complete end-to-end provider testing has not been performed and no
provider is supported yet.

The proposed first integration uses H2 startup followed by binary WSS over
HTTP/1.1, a dedicated public hostname and one HTTPS origin process. Client and
server must be updated together. The requirements below describe future
validation, not a supported deployment.

| Route | Required edge behavior |
| --- | --- |
| GET / | Bypass cache, forward Set-Cookie, preserve the exact representation |
| Selected site resources | Bypass cache for initial deployment; preserve MIME and bytes |
| POST /api/sync | Forward body and cookies; do not convert the method |
| GET /api/events/*, /media/chunk/* | Bypass cache; preserve the seq query and cookies |
| /api/realtime | Binary WebSocket passthrough; preserve Origin and Sec-WebSocket-* |

Use HTTPS to the origin with certificate verification and correct SNI. Preserve
the public Host. Configure Caddy trusted_proxies, trusted_proxies_strict and
client_ip_headers for the actual proxy chain; do not trust arbitrary forwarded
headers or the whole Internet. Health checks should use HEAD / rather than
creating sessions with GET /. Disable response transformations and browser-only
challenges using the provider's supported policy settings.

A CDN terminates TLS and is trusted with the NaiveFox protocol and authentication.
Application HTTPS inside a proxied connection retains its own encryption.
An outer carrier failure terminates its streams; new sessions can reconnect.
There is no transparent stream resume, multi-origin socket migration, or
supported H3-through-CDN deployment in this integration stage.

Startup GET uses ?seq=N (0 through 19); POST is identified by its cell sequence
and complete body. A repeated operation returns its original result, including
identical randomized GET padding, and never delivers its input twice.
A conflicting POST body or invalid ordering is rejected. The journal retains
at most 880 KiB of response bodies per session, uses a process-wide 64 MiB
body budget, expires two minutes after AUTH, and is cleared at sustained-carrier
entry or session close. Capacity exhaustion returns 503 without consuming
the operation. Expired/retired startup operations cannot execute again.

Provider readiness requires a local TLS reverse-proxy fixture, direct H2/H3
regressions, runtime checks on Linux/Windows/Android, and short performance
screens before testing public staging. Actual provider cache, timeout, origin
routing and security rules must be verified before claiming compatibility.

## Experimental Cloudflare configuration

This unvalidated example is retained for future development. It does not
establish CDN availability from any network or compatibility with NaiveFox.

Use a dedicated proxied DNS hostname with WebSockets enabled and Full (strict)
TLS. The origin certificate must be valid for that name and trusted by
Cloudflare (a public certificate or Cloudflare Origin CA). Keep one origin
process and the public Host. Use a normal proxied DNS route for this first test;
Workers, tunnels, load-balancer failover and H3 through the CDN are separate
integration work. See [WebSockets](https://developers.cloudflare.com/network/websockets/)
and [Full (strict)](https://developers.cloudflare.com/ssl/origin-configuration/ssl-modes/full-strict/).

Set a Cache Rule for the complete test hostname to bypass cache. Disable
response-altering features for it, including Rocket Loader, HTML/script
injection and image transformations. The module sends no-transform to configured trusted proxies and identity
bodies; do not add an origin encode handler. Configure a noninteractive
security policy for the test hostname so legitimate native requests do not
receive browser challenges. Keep these changes scoped to the dedicated test
hostname. See [Cache Rules](https://developers.cloudflare.com/cache/how-to/cache-rules/settings/)
and [compression/transform behavior](https://developers.cloudflare.com/speed/optimization/content/compression/).

Configure Caddy trusted_proxies with current Cloudflare egress ranges, enable
trusted_proxies_strict and set client_ip_headers to CF-Connecting-IP.
Do not include arbitrary intermediate proxies. The first test uses no Worker
subrequests and no Pseudo IPv4 header overwrite. Sessions remain bound to the
verified client address; changing networks requires a fresh session.
See [Cloudflare headers](https://developers.cloudflare.com/fundamentals/reference/http-headers/)
and the official [IPv4](https://www.cloudflare.com/ips-v4) /
[IPv6](https://www.cloudflare.com/ips-v6) lists.

Use HEAD / for health probes. Check public DNS and remove any client MAP to
the origin. Record the tested hostname's cache/security/TLS rules and verify
Cf-Ray at the client and origin. The client failure log records a bounded,
sanitized Cf-Ray identifier when present, plus phase/status/protocol; it does
not log authentication, cookie values, destination names or payload.
