package transport

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/caddyserver/forwardproxy"
)

// tcpPolicy is this module's small TCP policy engine. It consumes only the
// ordinary forwardproxy handler's public configuration; it neither modifies
// that module nor calls private methods. Differential tests preserve its ACL
// decisions, including the separate domain-denial check before DNS lookup.
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

func newTCPPolicy(fp *forwardproxy.Handler) (*tcpPolicy, error) {
	p := &tcpPolicy{timeout: time.Duration(fp.DialTimeout), ports: make(map[int]bool)}
	if p.timeout <= 0 {
		p.timeout = 30 * time.Second
	}
	for _, port := range fp.AllowedPorts {
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
	// These are the ordinary module's default private-network rules, evaluated
	// after the operator's explicit rules and before the final allow-all rule.
	for _, cidr := range []string{"10.0.0.0/8", "127.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "::1/128", "fe80::/10"} {
		rule, _ := compileDestinationRule(cidr, false)
		p.rules = append(p.rules, rule)
	}
	p.rules = append(p.rules, destinationRule{allow: true})
	if fp.Upstream != "" {
		var err error
		p.upstream, err = url.Parse(fp.Upstream)
		if err != nil {
			return nil, errors.New("invalid forward_proxy upstream URL")
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
	rule.domain = strings.TrimPrefix(subject, "*.")
	for _, label := range strings.Split(rule.domain, ".") {
		if len(label) == 0 || len(label) > 63 {
			return rule, errors.New("invalid forward_proxy ACL domain")
		}
		for _, c := range label {
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
				return rule, errors.New("invalid forward_proxy ACL domain")
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
	host = strings.TrimPrefix(host, ".")
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
		// Ordinary forwardproxy deliberately delegates DNS, ACL and port policy
		// to an upstream when configured. Do not resolve or filter locally.
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
