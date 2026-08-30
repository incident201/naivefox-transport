package transport

import (
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

func init() {
	httpcaddyfile.RegisterHandlerDirective("naivefox_transport", parseCaddyfile)
}

func parseCaddyfile(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	t := new(Transport)
	return t, t.UnmarshalCaddyfile(h.Dispenser)
}

// UnmarshalCaddyfile reads the handler's explicit configuration. Use it inside
// a route block before forward_proxy so CONNECT falls through unchanged.
func (t *Transport) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	seen := make(map[string]bool)
	for d.Next() {
		if d.NextArg() {
			return d.ArgErr()
		}
		for d.NextBlock(0) {
			name := d.Val()
			if seen[name] && name != "allowed_targets" {
				return d.Errf("duplicate naivefox_transport option %q", name)
			}
			seen[name] = true
			switch name {
			case "key":
				if !d.AllArgs(&t.Key) {
					return d.ArgErr()
				}
			case "profile":
				if !d.AllArgs(&t.Profile) {
					return d.ArgErr()
				}
			case "stats_path":
				if !d.AllArgs(&t.StatsPath) {
					return d.ArgErr()
				}
			case "allowed_targets":
				values := d.RemainingArgs()
				if len(values) == 0 {
					return d.ArgErr()
				}
				t.AllowedTargets = append(t.AllowedTargets, values...)
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
