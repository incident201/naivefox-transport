package transport

import (
	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"strconv"
	"time"
)

func init() {
	httpcaddyfile.RegisterHandlerDirective("naivefox_transport", parseCaddyfile)
}

func parseCaddyfile(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	t := new(Transport)
	return t, t.UnmarshalCaddyfile(h.Dispenser)
}

// UnmarshalCaddyfile reads the single NaiveFox handler configuration.
func (t *Transport) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	seen := make(map[string]bool)
	for d.Next() {
		if d.NextArg() {
			return d.ArgErr()
		}
		for d.NextBlock(0) {
			name := d.Val()
			if seen[name] && name != "basic_auth" && name != "allow" && name != "deny" {
				return d.Errf("duplicate naivefox_transport option %q", name)
			}
			seen[name] = true
			switch name {
			case "application_root":
				if !d.AllArgs(&t.ApplicationRoot) {
					return d.ArgErr()
				}
			case "diagnostics":
				if d.NextArg() {
					return d.ArgErr()
				}
				t.Diagnostics = true
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
			case "basic_auth":
				var credential Credential
				if !d.AllArgs(&credential.Username, &credential.Password) {
					return d.ArgErr()
				}
				t.Access.Credentials = append(t.Access.Credentials, credential)
			case "allow", "deny":
				subjects := d.RemainingArgs()
				if len(subjects) == 0 {
					return d.ArgErr()
				}
				t.Access.ACL = append(t.Access.ACL, ACLRule{Subjects: subjects, Allow: name == "allow"})
			case "ports":
				values := d.RemainingArgs()
				if len(values) == 0 {
					return d.ArgErr()
				}
				for _, value := range values {
					port, err := strconv.Atoi(value)
					if err != nil || port < 1 || port > 65535 {
						return d.Err("invalid port")
					}
					t.Access.AllowedPorts = append(t.Access.AllowedPorts, port)
				}
			case "dial_timeout":
				var value string
				if !d.AllArgs(&value) {
					return d.ArgErr()
				}
				duration, err := time.ParseDuration(value)
				if err != nil || duration <= 0 {
					return d.Err("invalid dial_timeout")
				}
				t.Access.DialTimeout = caddy.Duration(duration)
			case "upstream":
				if !d.AllArgs(&t.Access.Upstream) {
					return d.ArgErr()
				}
			case "stats_path":
				if !d.AllArgs(&t.StatsPath) {
					return d.ArgErr()
				}
			default:
				return d.Errf("unknown naivefox_transport option %q", name)
			}
		}
	}
	return nil
}

var _ caddyfile.Unmarshaler = (*Transport)(nil)
