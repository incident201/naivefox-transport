package transport

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/caddyserver/forwardproxy"
	"golang.org/x/net/dns/dnsmessage"
)

// Exercise the unmodified module's public HTTP handler, rather than reproducing
// its private credential check in the compatibility oracle. HTTP version 9 makes
// authenticated requests fail before any dial, and distinguishes them from 407.
func compatibilityClassicAuthenticated(t *testing.T, h *forwardproxy.Handler, authorization string) bool {
	t.Helper()
	r := testRequest(http.MethodConnect, "https://target.invalid:443", nil)
	r.ProtoMajor = 9
	r.Header.Set("Proxy-Authorization", authorization)
	err := h.ServeHTTP(httptest.NewRecorder(), r, caddyhttp.HandlerFunc(func(http.ResponseWriter, *http.Request) error {
		t.Fatal("unexpected ordinary forward-proxy fallback")
		return nil
	}))
	status, ok := err.(caddyhttp.HandlerError)
	if !ok || (status.StatusCode != http.StatusProxyAuthRequired && status.StatusCode != http.StatusHTTPVersionNotSupported) {
		t.Fatalf("ordinary authentication returned an unexpected result: %T", err)
	}
	return status.StatusCode == http.StatusHTTPVersionNotSupported
}

func TestCompatibilityAuthenticationAgainstOrdinaryForwardProxy(t *testing.T) {
	credentials := [][]byte{
		forwardproxy.EncodeAuthCredentials("fixture user@example", "p:/: %"),
		forwardproxy.EncodeAuthCredentials("second", "p:a:ss"),
		forwardproxy.EncodeAuthCredentials("fixture user@example", "p:/: %"),
		forwardproxy.EncodeAuthCredentials("", "password"),
		forwardproxy.EncodeAuthCredentials("username", ""),
	}
	m := &Transport{ForwardProxy: &forwardproxy.Handler{AuthCredentials: credentials}}
	if err := m.Provision(testCaddyContext(t)); err != nil {
		t.Fatal(err)
	}
	defer m.Cleanup()
	valid := "Basic " + string(credentials[0])
	cases := []struct {
		name, value string
		allowed     bool
	}{
		{"valid", valid, true},
		{"lowercase-type", "basic " + string(credentials[0]), true},
		{"uppercase-type", "BASIC " + string(credentials[0]), true},
		{"second-credential", "Basic " + string(credentials[1]), true},
		{"empty-user", "Basic " + string(credentials[3]), true},
		{"empty-password", "Basic " + string(credentials[4]), true},
		{"wrong-user", "Basic " + string(forwardproxy.EncodeAuthCredentials("wrong", "p:/: %")), false},
		{"wrong-password", "Basic " + string(forwardproxy.EncodeAuthCredentials("fixture user@example", "wrong")), false},
		{"both-empty", "Basic " + string(forwardproxy.EncodeAuthCredentials("", "")), false},
		{"missing", "", false},
		{"empty-token", "Basic ", false},
		{"two-spaces", "Basic  " + string(credentials[0]), false},
		{"leading-space", " " + valid, false},
		{"trailing-space", valid + " ", false},
		{"tab-separator", "Basic\t" + string(credentials[0]), false},
		{"newline-suffix", valid + "\n", false},
		{"wrong-scheme", "Bearer " + string(credentials[0]), false},
		{"malformed-base64", "Basic !!!", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			classic := compatibilityClassicAuthenticated(t, m.ForwardProxy, tc.value)
			native := m.authenticate([]byte(tc.value))
			if classic != tc.allowed || native != classic {
				t.Fatalf("authentication mismatch: classic=%v no-connect=%v expected=%v", classic, native, tc.allowed)
			}
		})
	}
}

// The transport deliberately requires at least one usable credential. The
// ordinary JSON module allows an empty list (deny all), nil (no auth), or the
// empty pair. None is a supported shared authenticated NaiveFox configuration.
func TestCompatibilityExplicitAuthenticationProvisioningBoundary(t *testing.T) {
	for _, credentials := range [][][]byte{nil, {}, {forwardproxy.EncodeAuthCredentials("", "")}} {
		ordinary := &forwardproxy.Handler{AuthCredentials: credentials}
		if err := ordinary.Provision(testCaddyContext(t)); err != nil {
			t.Fatal(err)
		}
		m := &Transport{ForwardProxy: &forwardproxy.Handler{AuthCredentials: credentials}}
		if err := m.Provision(testCaddyContext(t)); err == nil {
			m.Cleanup()
			t.Fatal("unsupported authentication configuration was accepted")
		}
	}
}

// Use a private DNS responder for deterministic exact, wildcard, case and
// trailing-dot domain tests. No fixture hostname is resolved on the public DNS.
func compatibilityResolver(t *testing.T) func() int {
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

func compatibilityTarget(t *testing.T) (string, func() int) {
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
			_, _ = io.WriteString(connection, "compatibility-target")
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

func compatibilityClassicOpen(t *testing.T, h *forwardproxy.Handler, target string) bool {
	t.Helper()
	r := testRequest(http.MethodConnect, "https://target.invalid", nil)
	r.Host = target
	r.URL = &url.URL{Host: target}
	r.ProtoMajor = 2
	r.Body = io.NopCloser(strings.NewReader(""))
	r.Header.Set("Proxy-Authorization", testAuthorization)
	response := httptest.NewRecorder()
	err := h.ServeHTTP(response, r, caddyhttp.HandlerFunc(func(http.ResponseWriter, *http.Request) error {
		t.Fatal("unexpected ordinary forward-proxy fallback")
		return nil
	}))
	if err != nil {
		return false
	}
	if response.Body.String() != "compatibility-target" {
		t.Fatal("ordinary forward proxy did not read the live target")
	}
	return true
}

func compatibilityNativeOpen(t *testing.T, policy *tcpPolicy, target string) bool {
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
	if err != nil || !bytes.Equal(body, []byte("compatibility-target")) {
		t.Fatal("no-connect policy did not read the live target")
	}
	return true
}

func TestCompatibilityOrderedPolicyAgainstOrdinaryForwardProxy(t *testing.T) {
	compatibilityResolver(t)
	address, accepted := compatibilityTarget(t)
	_, port, _ := net.SplitHostPort(address)
	portNumber, _ := strconv.Atoi(port)
	allow := func(subject string) forwardproxy.ACLRule {
		return forwardproxy.ACLRule{Subjects: []string{subject}, Allow: true}
	}
	deny := func(subject string) forwardproxy.ACLRule { return forwardproxy.ACLRule{Subjects: []string{subject}} }
	cases := []struct {
		name, host string
		rules      []forwardproxy.ACLRule
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
		{name: "exact-ip", host: "127.0.0.1", rules: []forwardproxy.ACLRule{allow("127.0.0.1"), deny("all")}, allowed: true},
		{name: "cidr-before-allow", host: "127.0.0.1", rules: []forwardproxy.ACLRule{deny("127.0.0.0/8"), allow("127.0.0.1")}},
		{name: "allow-before-cidr", host: "127.0.0.1", rules: []forwardproxy.ACLRule{allow("127.0.0.1"), deny("127.0.0.0/8")}, allowed: true},
		{name: "deny-all-first", host: "127.0.0.1", rules: []forwardproxy.ACLRule{deny("all"), allow("127.0.0.1")}},
		{name: "allow-all-first", host: "127.0.0.1", rules: []forwardproxy.ACLRule{allow("all"), deny("127.0.0.0/8")}, allowed: true},
		{name: "domain-allow", host: "svc.fixture.test", rules: []forwardproxy.ACLRule{allow("svc.fixture.test"), deny("all")}, allowed: true},
		{name: "domain-deny-precedes-ip-allow-in-precheck", host: "svc.fixture.test", rules: []forwardproxy.ACLRule{allow("127.0.0.1"), deny("svc.fixture.test")}},
		{name: "domain-allow-does-not-override-earlier-all-deny", host: "svc.fixture.test", rules: []forwardproxy.ACLRule{deny("all"), allow("svc.fixture.test")}},
		{name: "domain-deny-still-prechecked-after-all-allow", host: "svc.fixture.test", rules: []forwardproxy.ACLRule{allow("all"), deny("svc.fixture.test")}},
		{name: "wildcard-child", host: "svc.fixture.test", rules: []forwardproxy.ACLRule{allow("*.fixture.test"), deny("all")}, allowed: true},
		{name: "wildcard-base", host: "fixture.test", rules: []forwardproxy.ACLRule{allow("*.fixture.test"), deny("all")}, allowed: true},
		{name: "wildcard-deny", host: "svc.fixture.test", rules: []forwardproxy.ACLRule{allow("all"), deny("*.fixture.test")}},
		{name: "exact-domain-does-not-match-child", host: "sub.fixture.test", rules: []forwardproxy.ACLRule{allow("fixture.test")}},
		{name: "domain-case-sensitive", host: "Svc.fixture.test", rules: []forwardproxy.ACLRule{allow("svc.fixture.test")}},
		{name: "domain-trailing-dot", host: "svc.fixture.test.", rules: []forwardproxy.ACLRule{allow("svc.fixture.test")}},
		{name: "allowed-port", host: "127.0.0.1", rules: []forwardproxy.ACLRule{allow("127.0.0.1")}, ports: []int{portNumber}, allowed: true},
		{name: "disallowed-port", host: "127.0.0.1", rules: []forwardproxy.ACLRule{allow("127.0.0.1")}, ports: []int{1}},
		{name: "multiple-addresses-skip-denied", host: "multi.fixture.test", rules: []forwardproxy.ACLRule{allow("127.0.0.1"), deny("all")}, allowed: true},
		{name: "multiple-addresses-retry-refused", host: "multi.fixture.test", rules: []forwardproxy.ACLRule{allow("127.0.0.0/8")}, allowed: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ordinary := testForwardProxy()
			ordinary.ProbeResistance = nil
			ordinary.ACL, ordinary.AllowedPorts = tc.rules, tc.ports
			ordinary.DialTimeout = caddy.Duration(200 * time.Millisecond)
			if err := ordinary.Provision(testCaddyContext(t)); err != nil {
				t.Fatal(err)
			}
			policy, err := newTCPPolicy(ordinary)
			if err != nil {
				t.Fatal(err)
			}
			before := accepted()
			target := net.JoinHostPort(tc.host, port)
			classic := compatibilityClassicOpen(t, ordinary, target)
			native := compatibilityNativeOpen(t, policy, target)
			if classic != tc.allowed || native != classic {
				t.Fatalf("policy mismatch: classic=%v no-connect=%v expected=%v", classic, native, tc.allowed)
			}
			wantConnections := 0
			if tc.allowed {
				wantConnections = 2
			}
			if accepted()-before != wantConnections {
				t.Fatal("policy decision disagrees with live target connection count")
			}
		})
	}
}

func TestCompatibilityUpstreamOwnsDNSAndPolicy(t *testing.T) {
	queries := compatibilityResolver(t)
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
		_, _ = io.WriteString(connection, "compatibility-target")
	}))
	defer upstream.Close()
	ordinary := testForwardProxy()
	ordinary.ProbeResistance = nil
	ordinary.Upstream = upstream.URL
	ordinary.ACL = []forwardproxy.ACLRule{{Subjects: []string{"all"}}}
	ordinary.AllowedPorts = []int{1}
	if err := ordinary.Provision(testCaddyContext(t)); err != nil {
		t.Fatal(err)
	}
	policy, err := newTCPPolicy(ordinary)
	if err != nil {
		t.Fatal(err)
	}
	const target = "must-not-resolve.invalid:4242"
	if !compatibilityClassicOpen(t, ordinary, target) || !compatibilityNativeOpen(t, policy, target) {
		t.Fatal("configured upstream did not receive both tunnels")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(targets) != 2 || targets[0] != target || targets[1] != target || queries() != 0 {
		t.Fatal("upstream path changed target authority, resolved locally, or applied local ACL/ports")
	}
}
