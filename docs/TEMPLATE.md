# NaiveFox public site template

This directory is an example site for the `native-stream-v2` server and client.
Its existing CSS, JavaScript and four SVGs are ordinary example files; their
names and counts are not a transport requirement.

Put the complete public site in one directory and point `application_root`
at the directory containing `index.html`:

```text
my-site/
  index.html
  css/main.css
  js/main.js
  images/hero.webp
  pages/about/index.html
  downloads/archive.zip
```

For example, a small entry page can declare:

```html
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>My site</title>
  <link rel="icon" href="data:,">
  <link rel="stylesheet" href="/css/main.css">
  <script defer src="/js/main.js"></script>
</head>
<body>
  <img src="/images/hero.webp" alt="Site artwork">
  <a href="/pages/about/">About</a>
</body>
</html>
```

NaiveFox downloads the HTML, CSS, script and image; it does not follow the
About link, crawl the directory or execute JavaScript. All directly selected
resources must exist before validation. They remain in memory until reload;
other files are served from disk on demand. The server serves actual file
sizes without padding. There is no fixed number of files or mandatory byte
budget. Keep the initial selection small because every new carrier repeats it
with caching disabled.

See the canonical [site requirements](https://github.com/incident201/naivefox-transport/blob/main/docs/SITE.md)
for supported markup, URL rules, streaming behavior, size recommendations,
snapshot memory costs, update behavior and unsupported automatic resource types.

Configure one combined Caddy binary:

```caddyfile
:443, proxy.example.com {
    route {
        naivefox_transport {
            application_root /etc/caddy/my-site
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

Keep both site addresses and move any existing forward_proxy block inside
naivefox_transport. No additional root or file_server block is needed.
Keep private keys, logs and configuration outside the public directory.
Validate the complete site and Caddyfile, then reload the service.
Upgrade NaiveFox and the server together. Only the current native-stream-v2
implementation is supported. This instruction is kept outside the public template.
