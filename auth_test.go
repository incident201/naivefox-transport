package transport

import (
	"context"
	"encoding/base64"
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
	"github.com/incident201/naivefox-transport/internal/cell"
)

func TestInvalidAuthenticationCannotOpenLiveTarget(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	m := &Transport{ApplicationRoot: testApplicationRoot(t), Access: testAccess()}
	m.Access.ACL = []ACLRule{{Subjects: []string{"127.0.0.1"}, Allow: true}}
	if err := m.Provision(testCaddyContext(t)); err != nil {
		t.Fatal(err)
	}
	defer m.Cleanup()
	next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) error { w.WriteHeader(404); return nil })
	for _, authorization := range []string{"", "Basic ", "Basic " + string(encodeTestCredential("fixture", "wrong")), "Basic " + string(encodeTestCredential("", ""))} {
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
		if err := m.ServeHTTP(w, r, next); err != nil || w.Code != 404 {
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

func encodeTestCredential(user, password string) []byte {
	return []byte(base64.StdEncoding.EncodeToString([]byte(user + ":" + password)))
}
func testAccess() AccessConfig {
	return AccessConfig{Credentials: []Credential{{Username: "fixture", Password: "fixture"}}}
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
	m := &Transport{ApplicationRoot: testApplicationRoot(t), Access: testAccess()}
	m.Access.Credentials = append(m.Access.Credentials, Credential{Username: "another", Password: "p:a:ss"})
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

func TestModuleRequiresAuthentication(t *testing.T) {
	for _, credentials := range [][]Credential{nil, {}, {{}}, {{Username: "bad:user", Password: "p"}}, {{Username: "user", Password: strings.Repeat("a", 4000)}}} {
		m := &Transport{ApplicationRoot: testApplicationRoot(t), Access: AccessConfig{Credentials: credentials}}
		if err := m.Provision(testCaddyContext(t)); err == nil {
			m.Cleanup()
			t.Fatal("unsafe configuration accepted")
		}
	}
}

func TestOneEmptyCredentialPart(t *testing.T) {
	for _, pair := range [][2]string{{"", "password"}, {"username", ""}} {
		m := &Transport{ApplicationRoot: testApplicationRoot(t), Access: testAccess()}
		m.Access.Credentials = []Credential{{Username: pair[0], Password: pair[1]}}
		if err := m.Provision(testCaddyContext(t)); err != nil {
			t.Fatal(err)
		}
		if !m.authenticate([]byte("Basic " + string(encodeTestCredential(pair[0], pair[1])))) {
			t.Fatal("valid credential rejected")
		}
		m.Cleanup()
	}
}
