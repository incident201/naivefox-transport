# NaiveFox application template

Copy this complete directory to a versioned, operator-owned location and set
`application_root` to its absolute path. The Caddy module reads and validates
the bundle once while provisioning, renders the profile script, pads the seven
public responses to their fixed capacities, and then serves only that immutable
memory snapshot. Editing files on disk has no effect until a successful Caddy
reload.

## Quick start

```sh
cd /path/to/application-template
sudo install -d -o root -g caddy -m 0750 /etc/caddy/naivefox-applications/atlas-v1
sudo cp -a ./. /etc/caddy/naivefox-applications/atlas-v1/
sudo chown -R root:caddy /etc/caddy/naivefox-applications/atlas-v1
sudo find /etc/caddy/naivefox-applications/atlas-v1 -type d -exec chmod 0750 {} +
sudo find /etc/caddy/naivefox-applications/atlas-v1 -type f -exec chmod 0640 {} +
```

Configure the combined Caddy handler:

```caddyfile
:443, proxy.example.com {
    route {
        naivefox_transport {
            application_root /etc/caddy/naivefox-applications/atlas-v1
            forward_proxy {
                basic_auth USER PASSWORD
                hide_ip
                hide_via
                probe_resistance
            }
        }
        respond 404
    }
}
```

Keep the hostless `:443` address. Classic CONNECT uses the destination as its
HTTP authority, while the named address is needed for certificate automation.

Validate before reload:

```sh
sudo -u caddy /usr/local/bin/caddy-naivefox validate \
  --config /etc/caddy/Caddyfile --adapter caddyfile
sudo /usr/local/bin/caddy-naivefox reload \
  --config /etc/caddy/Caddyfile --adapter caddyfile --force
```

An invalid or incomplete bundle makes validation/reload fail. The running
configuration remains active.

After changing any of the nine required source files, regenerate the content
inventory before validation:

```sh
python3 update-manifest.py .
```

`application.json` declares format `naivefox-application-v1`, wire graph
`navigation-assets-api-v1`, and the exact raw size and SHA-256 of every
required source. The loader rejects unknown fields, missing or extra entries,
noncanonical hashes, mismatches, symlink escapes and a manifest changed during
snapshot creation. Do not edit hashes manually.

## Fixed public contract

The paths, response capacities and MIME types are part of the native client
contract and cannot be changed:

| Route | Template source | Maximum source or rendered size | Wire size | Content-Type |
| --- | --- | ---: | ---: | --- |
| `/` | `index.html` | 4096 | 4096 | `text/html; charset=utf-8` |
| `/assets/site.css` | `assets/site.css` | 12288 | 12288 | `text/css` |
| `/assets/app.js` | rendered `assets/app.js` | 24576 | 24576 | `text/javascript` |
| `/assets/image-1.svg` | `assets/image-1.svg` | 8192 | 8192 | `image/svg+xml` |
| `/assets/image-2.svg` | `assets/image-2.svg` | 8192 | 8192 | `image/svg+xml` |
| `/assets/image-3.svg` | `assets/image-3.svg` | 8192 | 8192 | `image/svg+xml` |
| `/assets/image-4.svg` | `assets/image-4.svg` | 8192 | 8192 | `image/svg+xml` |

Every source must be a nonempty, NUL-free UTF-8 regular file recorded in
`application.json`. Each SVG must contain complete `<svg>...</svg>` markup. The root document must reference all
six fixed asset paths. Query strings may be added for browser cache busting,
for example `/assets/app.js?v=atlas-v2`; the path remains fixed.

The server pads unused response capacity with ASCII spaces. Do not pre-pad files
to their wire size merely to satisfy the contract.

## Script contract

`assets/app.js` must contain each marker exactly once:

```text
__NFC_READER__
__NFC_LIFECYCLE__
__NFC_PROFILE__
```

During provisioning, the module replaces them with
`runtime/read-cell.js`, `runtime/lifecycle.js`, and the selected profile JSON.
The fully rendered script must fit in 24576 bytes. Keep the two runtime files
unchanged unless you are deliberately maintaining the browser-side NFC1
lifecycle. They never contain proxy credentials.

You may change page text, layout, colors, SVG artwork and the UI code around the
markers. If the supplied application script remains in use, keep the DOM
elements it addresses: `progress`, `status`, `chart`, and `refresh`.

## Traffic-shape boundary

The native no-connect client always fetches the fixed root and six assets, then
runs its fixed carrier API graph; it does not execute this JavaScript. A normal
browser does execute the template. Adding fonts, imports, images, fetches,
WebSockets or other network resources changes that browser's request graph even
though proxy transport correctness remains intact.

For a template-equivalent application, retain exactly one CSS, one script and
four image references and do not add external network activity. Additional
ordinary site files can be served by a later Caddy `file_server`, but they are
outside the validated no-connect application graph.

Keep this directory outside the public `file_server` root so that
`runtime/` and this documentation are not exposed accidentally. Deploy
updates into a new immutable versioned directory, run `update-manifest.py`,
validate it, change `application_root`, then reload Caddy. Do not edit the active directory in
place.
