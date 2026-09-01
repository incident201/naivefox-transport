# NaiveFox application template

Copy this directory, edit the seven public files, point `application_root` at
the copy, and start or restart Caddy. No generator, manifest compiler, watcher,
or per-request filesystem access is required.

The plugin opens the directory through a confined root, reads every required
file twice during provisioning, and accepts only two identical, complete
snapshots. It rejects missing files, symlinks, directories, devices, FIFOs,
oversized content, invalid UTF-8, malformed SVG, and an incomplete resource
graph. Successful provisioning pads the responses to their fixed capacities and
serves the resulting immutable in-memory snapshot.

## Quick start

```sh
cd /path/to/application-template
sudo install -d -o root -g caddy -m 0750 /etc/caddy/naivefox-applications/atlas-v1
sudo cp -a ./index.html ./assets /etc/caddy/naivefox-applications/atlas-v1/
sudo chown -R root:caddy /etc/caddy/naivefox-applications/atlas-v1
sudo find /etc/caddy/naivefox-applications/atlas-v1 -type d -exec chmod 0750 {} +
sudo find /etc/caddy/naivefox-applications/atlas-v1 -type f -exec chmod 0640 {} +
```

Configure the combined handler:

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

After copying or editing the complete application, an ordinary systemd restart
is sufficient:

```sh
sudo systemctl restart caddy
sudo systemctl status caddy --no-pager
```

The plugin performs all application validation during startup. A reload is
safer when the service supports it, because Caddy keeps the running
configuration if the new bundle is invalid:

```sh
sudo systemctl reload caddy
```

## Fixed public contract

The paths, response capacities and MIME types are part of the native client
contract:

| Route | Source | Maximum source size | Wire size | Content-Type |
| --- | --- | ---: | ---: | --- |
| `/` | `index.html` | 4096 | 4096 | `text/html; charset=utf-8` |
| `/assets/site.css` | `assets/site.css` | 12288 | 12288 | `text/css` |
| `/assets/app.js` | `assets/app.js` | 24576 | 24576 | `text/javascript` |
| `/assets/image-1.svg` | `assets/image-1.svg` | 8192 | 8192 | `image/svg+xml` |
| `/assets/image-2.svg` | `assets/image-2.svg` | 8192 | 8192 | `image/svg+xml` |
| `/assets/image-3.svg` | `assets/image-3.svg` | 8192 | 8192 | `image/svg+xml` |
| `/assets/image-4.svg` | `assets/image-4.svg` | 8192 | 8192 | `image/svg+xml` |

Every source must be a nonempty, NUL-free UTF-8 regular file. Each SVG must
contain complete `<svg>...</svg>` markup. The root document must reference
each of the six asset paths exactly once. Query strings may be added for browser
cache busting, for example `/assets/app.js?v=atlas-v2`.

The server pads unused capacity with ASCII spaces. Do not pre-pad files to their
wire size.

## Customization boundary

The production `assets/app.js` is served verbatim before padding. It has no
NaiveFox markers, injected profile, NFC1 codec, carrier endpoint names, or
required transport globals. You may replace it with any ordinary JavaScript
that fits the size bound. The same applies to HTML, CSS and the four SVGs.

Native no-connect does not execute this JavaScript. It independently performs
the fixed HTTPS startup and carrier graph. A normal browser does execute your
application, so added fonts, imports, images, fetches or WebSockets change that
browser's request graph. That can be intentional for a real application, but it
is then a new camouflage profile that should be measured rather than assumed
equivalent to the supplied seven-resource template.

Extra ordinary files can be served by a later Caddy `file_server`. They are
outside the validated no-connect resource graph. Keep `application_root`
outside that public file-server root.

For updates, finish writing a new directory before changing
`application_root` or restarting the service. The plugin does not watch files;
editing the active directory has no effect until the next successful
start/reload.
