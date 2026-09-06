# NaiveFox site directory

`application_root` is the **single directory containing the complete public
site**, including `index.html`, `assets/` and every additional resource.
The supplied template is a starting point; the seven required files are not
a limit on the number of files.

## Directory layout

```text
/etc/caddy/naivefox-applications/atlas-v1/
├── index.html                 # required
├── assets/
│   ├── site.css               # required
│   ├── app.js                 # required
│   ├── image-1.svg            # required
│   ├── image-2.svg            # required
│   ├── image-3.svg            # required
│   ├── image-4.svg            # required
│   ├── extra.js               # optional example
│   └── fonts/
│       └── body.woff2         # optional example
├── pages/
│   └── about/
│       └── index.html         # optional example
└── favicon.ico                # optional example
```

Add other HTML pages, scripts, styles, fonts, images and downloads anywhere in
this tree. For example, `assets/extra.js` is served at `/assets/extra.js`;
`pages/about/index.html` is served at `/pages/about/`. No generator, manifest,
second root or separate `file_server` is needed. Keep private configuration,
logs and keys outside this public directory.

## Install and configure

Customize the template and add your site's remaining files before copying it:

```sh
cd /path/to/application-template
sudo install -d -o root -g caddy -m 0750 /etc/caddy/naivefox-applications/atlas-v1
sudo cp -a ./. /etc/caddy/naivefox-applications/atlas-v1/
sudo chown -R root:caddy /etc/caddy/naivefox-applications/atlas-v1
sudo find /etc/caddy/naivefox-applications/atlas-v1 -type d -exec chmod 0750 {} +
sudo find /etc/caddy/naivefox-applications/atlas-v1 -type f -exec chmod 0640 {} +
```

Save this as **`/etc/caddy/Caddyfile`**, outside the site directory. Replace
`proxy.example.com`, `USER` and `PASSWORD`; keep `:443`:

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

`application_root` points to the directory containing `index.html`.
No extra `root` or `file_server` block is required. Keep the hostless `:443`
address for classic CONNECT and the named address for certificate automation.
Move any existing `forward_proxy` block inside `naivefox_transport`, preserving
its options; do not keep a duplicate standalone block.

Use the combined Caddy binary containing both proxy modules. After installing
it at `/usr/local/bin/caddy-naivefox` and configuring `caddy.service` to use it:

```sh
sudo -u caddy /usr/local/bin/caddy-naivefox validate --config /etc/caddy/Caddyfile --adapter caddyfile
sudo systemctl reload caddy
```

Reload only after successful validation. For a first start use
`sudo systemctl start caddy`; if reload is unavailable, restart the service.
See [binary installation and service setup](https://github.com/incident201/naivefox-transport#install-or-upgrade-the-systemd-service).
The repository's `examples/Caddyfile` provides the same configuration using
four environment variables documented in that file.

## Fixed public contract

Only these seven files have transport size and format requirements:

| Route | Source | Maximum source size | Wire size | Content-Type |
| --- | --- | ---: | ---: | --- |
| `/` | `index.html` | 4096 | 4096 | `text/html; charset=utf-8` |
| `/assets/site.css` | `assets/site.css` | 12288 | 12288 | `text/css` |
| `/assets/app.js` | `assets/app.js` | 24576 | 24576 | `text/javascript` |
| `/assets/image-1.svg` | `assets/image-1.svg` | 8192 | 8192 | `image/svg+xml` |
| `/assets/image-2.svg` | `assets/image-2.svg` | 8192 | 8192 | `image/svg+xml` |
| `/assets/image-3.svg` | `assets/image-3.svg` | 8192 | 8192 | `image/svg+xml` |
| `/assets/image-4.svg` | `assets/image-4.svg` | 8192 | 8192 | `image/svg+xml` |

Each required file must be nonempty, NUL-free UTF-8 and a regular file, not
a symlink. SVG files must contain complete `<svg>...</svg>` markup.
`index.html` must reference each of the six fixed asset paths exactly once;
references to additional resources are allowed. Query strings are allowed,
for example `/assets/app.js?v=2`.

At startup/reload the module reads the required files twice, validates that
both reads match, and pads their responses with ASCII spaces to the wire sizes.
Do not pre-pad files. Missing, invalid, oversized or changing required files
reject startup/reload. There is no built-in fallback site.

The JavaScript is ordinary site code, served without injected transport code
or required markers. NaiveFox fetches the seven resources without executing
JavaScript; a normal browser runs your site and loads its additional resources.

## Additional files and routes

Additional files are read from disk on GET/HEAD, with no transport size,
UTF-8, SVG or padding requirements. Binary and empty files work. Responses
support Content-Type, Content-Length, Last-Modified, conditional requests and
Range, with `Cache-Control: no-cache` for revalidation. These requests do not
create transport sessions.

Directories with `index.html` redirect to a trailing slash and serve that page.
There is no directory listing or automatic SPA fallback. Missing files and
unsupported static methods continue through forwardproxy to the next handler;
the example returns 404.

Files cannot shadow `/`, the six fixed asset URLs, `/api/sync`,
`/api/realtime`, `/api/events/brief`, `/api/events/state`,
`/media/chunk/*` or `/__lab/*`. `/index.html` redirects to `/`.
Normalized URL aliases cannot bypass transport routing; redirects preserve
query strings. Do not put a compression handler around transport routes.
Reads stay inside the root, including relative symlink targets. Escaping
symlinks, traversal paths and special files are rejected.

## Updates

| Change | Action |
| --- | --- |
| Edit any of the seven required files | Validate, then reload/restart |
| Add, edit or delete an additional file | No reload; subsequent requests read disk |
| Replace the root directory or change `application_root` | Validate, then reload/restart |

The seven responses stay in memory even if their source files are edited or
removed. The module also retains a handle to its root directory, so renaming
or replacing the directory does not switch a running module to a new one.

Replace individual additional files atomically to avoid partial reads. For
a consistent whole-site update, prepare a new complete directory, change
`application_root` and reload. A failed reload preserves the running site.
