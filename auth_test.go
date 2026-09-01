package transport

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bytes"
	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/caddyserver/forwardproxy"
	"github.com/incident201/naivefox-transport/internal/cell"
)

func TestInvalidAuthenticationCannotOpenLiveTarget(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	m := &Transport{ApplicationRoot: testApplicationRoot(t), ForwardProxy: testForwardProxy()}
	m.ForwardProxy.ACL = []forwardproxy.ACLRule{{Subjects: []string{"127.0.0.1"}, Allow: true}}
	if err := m.Provision(testCaddyContext(t)); err != nil {
		t.Fatal(err)
	}
	defer m.Cleanup()
	next := caddyhttp.HandlerFunc(func(http.ResponseWriter, *http.Request) error { t.Fatal("unexpected fallback"); return nil })
	for _, authorization := range []string{"", "Basic ", "Basic " + string(forwardproxy.EncodeAuthCredentials("fixture", "wrong")), "Basic " + string(forwardproxy.EncodeAuthCredentials("", ""))} {
		root := httptest.NewRecorder()
		m.ServeHTTP(root, testRequest("GET", "https://localhost/", nil), next)
		frames := []cell.Frame{{Kind: cell.Open, Stream: 1, Body: []byte(listener.Addr().String())}}
		if authorization != "" {
			frames = append([]cell.Frame{{Kind: cell.Auth, Body: []byte(authorization)}}, frames...)
		}
		body, err := cell.Encode(0, 4096, frames)
		if err != nil {
			t.Fatal(err)
		}
		r := testRequest("POST", "https://localhost/api/sync", bytes.NewReader(body))
		r.AddCookie(root.Result().Cookies()[0])
		w := httptest.NewRecorder()
		if err := m.ServeHTTP(w, r, next); err != nil || w.Code != 400 {
			t.Fatalf("invalid auth was not rejected: %d %v", w.Code, err)
		}
	}
	listener.(*net.TCPListener).SetDeadline(time.Now().Add(30 * time.Millisecond))
	if conn, err := listener.Accept(); err == nil {
		conn.Close()
		t.Fatal("unauthenticated target dial")
	}
	if m.stats.Opens != 0 {
		t.Fatal("unauthenticated OPEN dispatched")
	}
}

const testAuthorization = "Basic Zml4dHVyZTpmaXh0dXJl"

func testRequest(method, target string, body io.Reader) *http.Request {
	r := httptest.NewRequest(method, target, body)
	return r.WithContext(context.WithValue(r.Context(), caddy.ReplacerCtxKey, caddy.NewReplacer()))
}

func testForwardProxy() *forwardproxy.Handler {
	return &forwardproxy.Handler{
		AuthCredentials: [][]byte{forwardproxy.EncodeAuthCredentials("fixture", "fixture")},
		HideIP:          true, HideVia: true, ProbeResistance: &forwardproxy.ProbeResistance{},
	}
}

func testApplicationRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("template")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func testCaddyContext(t *testing.T) caddy.Context {
	t.Helper()
	ctx, cancel := caddy.NewContext(caddy.Context{Context: context.Background()})
	t.Cleanup(cancel)
	return ctx
}

func TestSharedCredentials(t *testing.T) {
	m := &Transport{ApplicationRoot: testApplicationRoot(t), ForwardProxy: testForwardProxy()}
	m.ForwardProxy.AuthCredentials = append(m.ForwardProxy.AuthCredentials, forwardproxy.EncodeAuthCredentials("another", "p:a:ss"))
	if err := m.Provision(testCaddyContext(t)); err != nil {
		t.Fatal(err)
	}
	defer m.Cleanup()
	for _, value := range []string{testAuthorization, "Basic " + base64.StdEncoding.EncodeToString([]byte("another:p:a:ss"))} {
		if !m.authenticate([]byte(value)) {
			t.Fatal("valid shared credentials rejected")
		}
	}
	for _, value := range []string{"", "Basic ", "Basic Zml4dHVyZTp3cm9uZw==", "Basic OmZpeHR1cmU=", "Basic Zml4dHVyZTo=", "Bearer " + testAuthorization, strings.ToLower(testAuthorization), testAuthorization + "\n", "Basic " + strings.Repeat("a", 4096)} {
		if m.authenticate([]byte(value)) {
			t.Fatal("invalid credentials accepted")
		}
	}
}

func TestModuleRequiresSharedAuthentication(t *testing.T) {
	for _, credentials := range [][][]byte{nil, {}, {forwardproxy.EncodeAuthCredentials("", "")}, {[]byte("not-base64")}, {forwardproxy.EncodeAuthCredentials("user", strings.Repeat("a", 4000))}} {
		m := &Transport{ApplicationRoot: testApplicationRoot(t), ForwardProxy: &forwardproxy.Handler{AuthCredentials: credentials}}
		if err := m.Provision(testCaddyContext(t)); err == nil {
			m.Cleanup()
			t.Fatal("unsafe configuration accepted")
		}
	}
	if err := (&Transport{ApplicationRoot: testApplicationRoot(t)}).Provision(testCaddyContext(t)); err == nil {
		t.Fatal("missing forward_proxy accepted")
	}
}

func TestOneEmptyCredentialPartMatchesClassic(t *testing.T) {
	for _, pair := range [][2]string{{"", "password"}, {"username", ""}} {
		m := &Transport{ApplicationRoot: testApplicationRoot(t), ForwardProxy: testForwardProxy()}
		m.ForwardProxy.AuthCredentials = [][]byte{forwardproxy.EncodeAuthCredentials(pair[0], pair[1])}
		if err := m.Provision(testCaddyContext(t)); err != nil {
			t.Fatal(err)
		}
		if !m.authenticate([]byte("Basic " + string(m.ForwardProxy.AuthCredentials[0]))) {
			t.Fatal("valid classic-compatible credential rejected")
		}
		m.Cleanup()
	}
}

func TestLegacyJSONRejected(t *testing.T) {
	for _, input := range []string{`{"key":""}`, `{"key":null}`, `{"allowed_targets":[]}`, `{"allowed_targets":null}`} {
		var m Transport
		if err := json.Unmarshal([]byte(input), &m); err != nil {
			t.Fatal(err)
		}
		m.ForwardProxy = testForwardProxy()
		if err := m.Provision(testCaddyContext(t)); err == nil || !strings.Contains(err.Error(), "were removed") {
			t.Fatalf("migration should fail explicitly: %v", err)
		}
	}
}
