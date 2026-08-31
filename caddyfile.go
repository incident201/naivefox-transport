package transport

import (
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/caddyserver/forwardproxy"
	"strconv"
)

func init() {
	httpcaddyfile.RegisterHandlerDirective("naivefox_transport", parseCaddyfile)
}

func parseCaddyfile(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	t := new(Transport)
	return t, t.UnmarshalCaddyfile(h.Dispenser)
}

// UnmarshalCaddyfile reads the handler's explicit configuration. Use it inside
// a route block with one nested forward_proxy for both transports.
func (t *Transport) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	seen := make(map[string]bool)
	for d.Next() {
		if d.NextArg() {
			return d.ArgErr()
		}
		for d.NextBlock(0) {
			name := d.Val()
			if seen[name] {
				return d.Errf("duplicate naivefox_transport option %q", name)
			}
			seen[name] = true
			switch name {
			case "max_sessions":
				var value string
				if !d.AllArgs(&value) {
					return d.ArgErr()
				}
				limit, err := strconv.Atoi(value)
				if err != nil || limit <= 0 {
					return d.Err("max_sessions must be a positive integer")
				}
				t.MaxSessions = limit
			case "key", "allowed_targets":
				return d.Errf("%s was removed; nest forward_proxy inside naivefox_transport and configure basic_auth once for both transports", name)
			case "forward_proxy":
				t.ForwardProxy = new(forwardproxy.Handler)
				if err := t.ForwardProxy.UnmarshalCaddyfile(d.NewFromNextSegment()); err != nil {
					return err
				}
			case "profile":
				if !d.AllArgs(&t.Profile) {
					return d.ArgErr()
				}
			case "stats_path":
				if !d.AllArgs(&t.StatsPath) {
					return d.ArgErr()
				}
			case "append_mode":
				if d.NextArg() {
					return d.ArgErr()
				}
				t.AppendMode = true
			default:
				return d.Errf("unknown naivefox_transport option %q", name)
			}
		}
	}
	return nil
}

var _ caddyfile.Unmarshaler = (*Transport)(nil)
