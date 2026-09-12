package transport

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"

	"golang.org/x/net/dns/dnsmessage"
)

// Exercise the unmodified module's public HTTP handler, rather than reproducing
// its private credential check in the policyFixture oracle. HTTP version 9 makes
// authenticated requests fail before any dial, and distinguishes them from 407.
func policyFixtureResolver(t *testing.T) func() int {
	t.Helper()
	listener, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	queries := 0
	done := make(chan struct{})
	go func() {
		defer close(done)
		buffer := make([]byte, 4096)
		for {
			count, peer, err := listener.ReadFrom(buffer)
			if err != nil {
				return
			}
			var request dnsmessage.Message
			if request.Unpack(buffer[:count]) != nil {
				continue
			}
			mu.Lock()
			queries++
			mu.Unlock()
			response := dnsmessage.Message{
				Header:    dnsmessage.Header{ID: request.ID, Response: true, RecursionDesired: true, RecursionAvailable: true},
				Questions: request.Questions,
			}
			for _, question := range request.Questions {
				if question.Type == dnsmessage.TypeA {
					if strings.EqualFold(question.Name.String(), "multi.fixture.test.") {
						response.Answers = append(response.Answers, dnsmessage.Resource{
							Header: dnsmessage.ResourceHeader{Name: question.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET},
							Body:   &dnsmessage.AResource{A: [4]byte{127, 0, 0, 2}},
						})
					}
					response.Answers = append(response.Answers, dnsmessage.Resource{
						Header: dnsmessage.ResourceHeader{Name: question.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET},
						Body:   &dnsmessage.AResource{A: [4]byte{127, 0, 0, 1}},
					})
				}
			}
			if packet, err := response.Pack(); err == nil {
				_, _ = listener.WriteTo(packet, peer)
			}
		}
	}()
	previous := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "udp", listener.LocalAddr().String())
	}}
	t.Cleanup(func() {
		net.DefaultResolver = previous
		listener.Close()
		<-done
	})
	return func() int {
		mu.Lock()
		defer mu.Unlock()
		return queries
	}
}

func policyFixtureTarget(t *testing.T) (string, func() int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	accepted := 0
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			accepted++
			mu.Unlock()
			connection.SetDeadline(time.Now().Add(time.Second))
			_, _ = io.WriteString(connection, "policyFixture-target")
			connection.Close()
		}
	}()
	t.Cleanup(func() { listener.Close(); <-done })
	return listener.Addr().String(), func() int {
		mu.Lock()
		defer mu.Unlock()
		return accepted
	}
}

func policyFixtureNativeOpen(t *testing.T, policy *tcpPolicy, target string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	connection, err := policy.DialContext(ctx, target)
	if err != nil {
		return false
	}
	defer connection.Close()
	connection.SetDeadline(time.Now().Add(time.Second))
	if half, ok := connection.(interface{ CloseWrite() error }); ok {
		_ = half.CloseWrite()
	}
	body, err := io.ReadAll(connection)
	if err != nil || !bytes.Equal(body, []byte("policyFixture-target")) {
		t.Fatal("destination policy did not read the live target")
	}
	return true
}

func TestOrderedDestinationPolicy(t *testing.T) {
	policyFixtureResolver(t)
	address, accepted := policyFixtureTarget(t)
	_, port, _ := net.SplitHostPort(address)
	portNumber, _ := strconv.Atoi(port)
	allow := func(subject string) ACLRule {
		return ACLRule{Subjects: []string{subject}, Allow: true}
	}
	deny := func(subject string) ACLRule { return ACLRule{Subjects: []string{subject}} }
	cases := []struct {
		name, host string
		rules      []ACLRule
		ports      []int
		allowed    bool
	}{
		{name: "default-private-denial", host: "127.0.0.1"},
		{name: "default-private-10", host: "10.1.2.3"},
		{name: "default-private-172", host: "172.31.1.2"},
		{name: "default-private-192", host: "192.168.1.2"},
		{name: "default-private-ipv6-loopback", host: "::1"},
		{name: "default-private-ipv6-link-local", host: "fe80::1"},
		{name: "default-private-mapped-ipv4", host: "::ffff:127.0.0.1"},
		{name: "exact-ip", host: "127.0.0.1", rules: []ACLRule{allow("127.0.0.1"), deny("all")}, allowed: true},
		{name: "cidr-before-allow", host: "127.0.0.1", rules: []ACLRule{deny("127.0.0.0/8"), allow("127.0.0.1")}},
		{name: "allow-before-cidr", host: "127.0.0.1", rules: []ACLRule{allow("127.0.0.1"), deny("127.0.0.0/8")}, allowed: true},
		{name: "deny-all-first", host: "127.0.0.1", rules: []ACLRule{deny("all"), allow("127.0.0.1")}},
		{name: "allow-all-first", host: "127.0.0.1", rules: []ACLRule{allow("all"), deny("127.0.0.0/8")}, allowed: true},
		{name: "domain-allow", host: "svc.fixture.test", rules: []ACLRule{allow("svc.fixture.test"), deny("all")}, allowed: true},
		{name: "domain-deny-precedes-ip-allow-in-precheck", host: "svc.fixture.test", rules: []ACLRule{allow("127.0.0.1"), deny("svc.fixture.test")}},
		{name: "domain-allow-does-not-override-earlier-all-deny", host: "svc.fixture.test", rules: []ACLRule{deny("all"), allow("svc.fixture.test")}},
		{name: "domain-deny-still-prechecked-after-all-allow", host: "svc.fixture.test", rules: []ACLRule{allow("all"), deny("svc.fixture.test")}},
		{name: "wildcard-child", host: "svc.fixture.test", rules: []ACLRule{allow("*.fixture.test"), deny("all")}, allowed: true},
		{name: "wildcard-base", host: "fixture.test", rules: []ACLRule{allow("*.fixture.test"), deny("all")}, allowed: true},
		{name: "wildcard-deny", host: "svc.fixture.test", rules: []ACLRule{allow("all"), deny("*.fixture.test")}},
		{name: "exact-domain-does-not-match-child", host: "sub.fixture.test", rules: []ACLRule{allow("fixture.test")}},
		{name: "domain-case-insensitive", host: "Svc.fixture.test", rules: []ACLRule{allow("svc.fixture.test")}, allowed: true},
		{name: "domain-trailing-dot", host: "svc.fixture.test.", rules: []ACLRule{allow("svc.fixture.test")}, allowed: true},
		{name: "allowed-port", host: "127.0.0.1", rules: []ACLRule{allow("127.0.0.1")}, ports: []int{portNumber}, allowed: true},
		{name: "disallowed-port", host: "127.0.0.1", rules: []ACLRule{allow("127.0.0.1")}, ports: []int{1}},
		{name: "multiple-addresses-skip-denied", host: "multi.fixture.test", rules: []ACLRule{allow("127.0.0.1"), deny("all")}, allowed: true},
		{name: "multiple-addresses-retry-refused", host: "multi.fixture.test", rules: []ACLRule{allow("127.0.0.0/8")}, allowed: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ordinary := testAccess()
			ordinary.ACL, ordinary.AllowedPorts = tc.rules, tc.ports
			ordinary.DialTimeout = caddy.Duration(200 * time.Millisecond)
			policy, err := newTCPPolicy(&ordinary)
			if err != nil {
				t.Fatal(err)
			}
			before := accepted()
			target := net.JoinHostPort(tc.host, port)
			native := policyFixtureNativeOpen(t, policy, target)
			if native != tc.allowed {
				t.Fatalf("policy mismatch: actual=%v expected=%v", native, tc.allowed)
			}
			wantConnections := 0
			if tc.allowed {
				wantConnections = 1
			}
			if accepted()-before != wantConnections {
				t.Fatal("policy decision disagrees with live target connection count")
			}
		})
	}
}

func TestUpstreamOwnsDNSAndPolicy(t *testing.T) {
	queries := policyFixtureResolver(t)
	var mu sync.Mutex
	var targets []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			t.Error("upstream received non-CONNECT request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		targets = append(targets, r.Host)
		mu.Unlock()
		connection, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer connection.Close()
		connection.SetDeadline(time.Now().Add(time.Second))
		_, _ = io.WriteString(connection, "HTTP/1.1 200 Connection Established\r\n\r\n")
		// Send target data after the caller's FIN, so this policy/DNS oracle
		// does not depend on the ordinary dialer's HTTP prefetch behavior.
		if _, err := io.Copy(io.Discard, connection); err != nil {
			t.Error("upstream did not receive the caller's half-close")
			return
		}
		_, _ = io.WriteString(connection, "policyFixture-target")
	}))
	defer upstream.Close()
	ordinary := testAccess()
	ordinary.Upstream = upstream.URL
	ordinary.ACL = []ACLRule{{Subjects: []string{"all"}}}
	ordinary.AllowedPorts = []int{1}
	policy, err := newTCPPolicy(&ordinary)
	if err != nil {
		t.Fatal(err)
	}
	const target = "must-not-resolve.invalid:4242"
	if !policyFixtureNativeOpen(t, policy, target) {
		t.Fatal("configured upstream did not receive the stream")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(targets) != 1 || targets[0] != target || queries() != 0 {
		t.Fatal("upstream path changed target authority, resolved locally, or applied local ACL/ports")
	}
}
