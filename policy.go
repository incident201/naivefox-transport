package transport

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/caddyserver/caddy/v2"
)

type AccessConfig struct {
	Credentials  []Credential   `json:"credentials"`
	ACL          []ACLRule      `json:"acl,omitempty"`
	AllowedPorts []int          `json:"allowed_ports,omitempty"`
	DialTimeout  caddy.Duration `json:"dial_timeout,omitempty"`
	Upstream     string         `json:"upstream,omitempty"`
}
type ACLRule struct {
	Subjects []string `json:"subjects"`
	Allow    bool     `json:"allow"`
}

type tcpPolicy struct {
	rules    []destinationRule
	ports    map[int]bool
	timeout  time.Duration
	upstream *url.URL
}

type destinationRule struct {
	allow      bool
	network    *net.IPNet
	domain     string
	subdomains bool
}

func newTCPPolicy(fp *AccessConfig) (*tcpPolicy, error) {
	p := &tcpPolicy{timeout: time.Duration(fp.DialTimeout), ports: make(map[int]bool)}
	if p.timeout <= 0 {
		p.timeout = 30 * time.Second
	}
	for _, port := range fp.AllowedPorts {
		if port < 1 || port > 65535 {
			return nil, errors.New("invalid allowed port")
		}
		p.ports[port] = true
	}
	for _, rule := range fp.ACL {
		for _, subject := range rule.Subjects {
			compiled, err := compileDestinationRule(subject, rule.Allow)
			if err != nil {
				return nil, err
			}
			p.rules = append(p.rules, compiled)
		}
	}
	// Explicit operator rules precede the default private-network exclusions.
	for _, cidr := range []string{"10.0.0.0/8", "127.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "::1/128", "fe80::/10"} {
		rule, _ := compileDestinationRule(cidr, false)
		p.rules = append(p.rules, rule)
	}
	p.rules = append(p.rules, destinationRule{allow: true})
	if fp.Upstream != "" {
		var err error
		p.upstream, err = url.Parse(fp.Upstream)
		if err != nil || p.upstream.Hostname() == "" || (p.upstream.Scheme != "http" && p.upstream.Scheme != "https" && p.upstream.Scheme != "socks5" && p.upstream.Scheme != "socks5h") {
			return nil, errors.New("invalid NaiveFox upstream URL")
		}
	}
	return p, nil
}

func compileDestinationRule(subject string, allow bool) (destinationRule, error) {
	rule := destinationRule{allow: allow}
	if subject == "all" {
		return rule, nil
	}
	if _, network, err := net.ParseCIDR(subject); err == nil {
		rule.network = network
		return rule, nil
	}
	if ip := net.ParseIP(subject); ip != nil {
		bits := 128
		if ipv4 := ip.To4(); ipv4 != nil {
			ip, bits = ipv4, 32
		}
		rule.network = &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)}
		return rule, nil
	}
	rule.subdomains = strings.HasPrefix(subject, "*.")
	rule.domain = strings.ToLower(strings.TrimPrefix(subject, "*."))
	for _, label := range strings.Split(rule.domain, ".") {
		if len(label) == 0 || len(label) > 63 {
			return rule, errors.New("invalid NaiveFox ACL domain")
		}
		for _, c := range label {
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
				return rule, errors.New("invalid NaiveFox ACL domain")
			}
		}
	}
	return rule, nil
}

func (r destinationRule) matches(host string, ip net.IP) bool {
	if r.network != nil {
		return ip != nil && r.network.Contains(ip)
	}
	if r.domain == "" {
		return true
	}
	host = strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(host, "."), "."))
	return host == r.domain || (r.subdomains && strings.HasSuffix(host, "."+r.domain))
}

func (p *tcpPolicy) DialContext(ctx context.Context, target string) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	host, port, err := net.SplitHostPort(target)
	if err != nil || host == "" {
		return nil, errors.New("invalid TCP destination")
	}
	if p.upstream != nil {
		// A configured upstream owns destination resolution and policy.
		return dialUpstream(ctx, target, p.upstream, p.timeout)
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 || (len(p.ports) > 0 && !p.ports[number]) {
		return nil, errors.New("destination port denied")
	}
	for _, rule := range p.rules {
		if rule.domain != "" && rule.matches(host, nil) {
			if !rule.allow {
				return nil, errors.New("destination domain denied")
			}
			break
		}
	}
	dialCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	addresses, err := net.DefaultResolver.LookupIP(dialCtx, "ip", host)
	if err != nil {
		return nil, err
	}
	for _, ip := range addresses {
		allowed := false
		for _, rule := range p.rules {
			if rule.matches(host, ip) {
				allowed = rule.allow
				break
			}
		}
		if !allowed {
			continue
		}
		conn, err := (&net.Dialer{Timeout: p.timeout, KeepAlive: 30 * time.Second}).DialContext(dialCtx, "tcp", net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		if dialCtx.Err() != nil {
			return nil, dialCtx.Err()
		}
	}
	return nil, errors.New("no reachable permitted destination address")
}
