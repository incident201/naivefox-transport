# Site contract for native-stream-v2

Set `application_root` to one absolute directory containing `index.html` and
the complete public site. The module discovers startup resources from that HTML.
There are no compulsory CSS, JavaScript or image filenames, resource counts,
manifest files, generators or injected transport scripts.

Both the server and NaiveFox must support `native-stream-v2`. Older
`native-stream-v1` clients/servers require a coordinated upgrade. Classic
CONNECT and its separate fronting-page requirements are unchanged.

## What is loaded

The native client loads the root and every distinct supported external resource
declared directly in its HTML. Resources referenced only by another CSS, script,
image or page are not recursively discovered.

| Initial HTML | Native bootstrap |
| --- | --- |
| `<link rel="stylesheet" href="...">` | Unconditional stylesheet |
| `<script defer src="..."></script>` | Classic deferred script, downloaded without execution |
| `<img src="...">` | Simple eager image |
| `<link rel="preload" as="image" href="...">` | Declared image |
| `<link rel="icon" href="...">` or `rel="shortcut icon"` | Declared icon |
| Inline content or `data:` URLs | Part of the root; no separate request |
| Navigation links and form actions | Not followed |
| Content inside `template` or scripting-disabled `noscript` | Not loaded |

A root-only page is valid. Duplicate declarations of the same effective URL and
compatible resource kind are fetched once per carrier. Image preload followed
by an image declaration is one request. Query strings are preserved and create
distinct request identities; fragments do not. Conflicting resource kinds fail
validation.

Use relative or root-relative URLs within the site. Cross-origin,
scheme-relative and credential-bearing URLs, `base`, traversal and collisions
with transport routes are rejected. Selected resources must be regular files
inside `application_root`, not final-component symlinks or special files.
Root, CSS and JavaScript must be NUL-free UTF-8. Images/icons may be binary and
need an appropriate image extension/type. SVG files require complete SVG markup.

The initial supported subset deliberately rejects automatic network behavior
it cannot interpret: modules, async or non-deferred external scripts,
responsive/picture or lazy image selection, iframes, objects, embedded media,
preconnect/prefetch, web-manifest declarations and non-image preloads. CSS/media
selection, crossorigin, integrity, nonce, referrer-policy, fetch-priority and
charset variations are unsupported on selected declarations. Meta refresh,
meta CSP and meta referrer policy are rejected. Unsupported declarations cause
a clear validation error, not silent omission. Ordinary non-network markup,
text and inline site scripts are allowed; the native client executes no script.

CSS `@import`/`url()`, JavaScript `fetch()`/`import()`, SVG secondary
references and dynamically inserted HTML remain ordinary browser behavior.
NaiveFox does not follow them. This contract represents the initial HTML's
declared first level, not a complete browser page load. Merely storing PNG
originals, fonts or downloads in the directory never makes them bootstrap
downloads.

## Size and request recommendations

There is **no protocol limit on total site bytes, individual file size or
number of resources**. Every selected resource is loaded completely with its
real size; the module does not add space padding or select only a cheap subset.

Keep the entry page lightweight and use appropriately sized images. The old
seven-file bootstrap transmitted 72 KiB of bodies; this is a reference point,
not a required budget or a padding target. Do not enlarge files or tune sizes
to packet-window scores. A large directly declared image is downloaded on every
new native carrier, even if it is several megabytes.

The client keeps caching disabled. It opens at most six resource requests at
once and queues the rest. Public response bodies are consumed incrementally
with fixed-size I/O buffers. Root parsing and resource metadata still require
memory proportional to document structure and the selected inventory.
Ordinary timeouts, cancellation, memory availability and HTTP implementation
limits apply; removing a size budget does not promise unlimited machine
resources or unlimited transfer time.

For a stable root/resource body total `S` and `N` new successful carriers,
public bootstrap bodies cost `N * S`. Existing warm carriers can serve many
local connections without another bootstrap. More than 32 simultaneous streams
may require additional carriers. Failed/restarted attempts and encrypted
transport overhead add traffic. There is no recursive crawl, periodic reload,
automatic retry or inter-carrier resource cache.

The twenty NFC1 POST/GET pairs remain an additional 960 KiB of body capacity per
new carrier, with useful proxy data displacing filler where available. They
still precede the persistent WebSocket. This site contract does not make an
arbitrary site's JavaScript perform those transport exchanges.

## Memory snapshot and updates

At startup/reload, the module reads `index.html`, derives its selected
inventory and loads those files into an immutable memory snapshot. It verifies
the sources again before publication; verification uses a streaming read.
Selected bytes and MIME types are served identically to browsers and native
clients. Responses carry their actual Content-Length and no X-App-* headers.
Snapshot identity is confirmed inside the authenticated carrier using the
document and resource body digests, URLs, kinds and MIME types. There is no client-specific HTML or script
injection.

Validation/startup logs report startup_resources, bootstrap_body_bytes and
retained_body_bytes. These are body counts, not encrypted IP traffic or total
allocator usage. Server memory scales with selected file bytes and metadata. Account for both
the running and candidate snapshot during reload. All selected files must
exist before validation; additional unselected files are not scanned or loaded.
Missing files, unsupported declarations or unstable reads reject a reload,
preserving the running valid configuration.

| Content | Source | Update takes effect |
| --- | --- | --- |
| Root and HTML-selected resources | Immutable memory snapshot | Successful reload/restart |
| Other site files | Confined disk reads on GET/HEAD | Next request |
| Replacement of the root directory | Open directory handle | Successful reload/restart |

A client whose downloaded site differs from the authenticated server snapshot
fails before target opening without an automatic refetch loop. The identity provides consistency, not a
secret or a replacement for TLS/authentication.

The root is not cacheable. Selected public resources retain ordinary server
cache headers; the native client nevertheless inhibits caching. Other files
support MIME types, Last-Modified, conditional requests and byte ranges with
`Cache-Control: no-cache`. Directory index handling is supported without
directory listing or a catch-all SPA fallback.

`/index.html` redirects to `/`. Snapshot paths and `/api/sync`,
`/api/events/brief`, `/api/events/state`, `/media/chunk/*`,
`/api/realtime` and `/__lab/*` take priority over disk fallback.
Do not wrap transport/snapshot routes in a compression handler: the current
native profile requires identity-encoded complete responses.

Keep credentials, Caddy configuration, private keys and logs outside the public
directory. Prepare a complete new directory for atomic whole-site changes,
update `application_root`, validate the Caddy configuration and reload.
