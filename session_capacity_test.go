package transport

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/incident201/naivefox-transport/internal/cell"
)

func TestAnonymousVisitsCannotOccupyAuthenticatedCapacity(t *testing.T) {
	m := &Transport{ForwardProxy: testForwardProxy(), MaxSessions: 2}
	if err := m.Provision(testCaddyContext(t)); err != nil {
		t.Fatal(err)
	}
	defer m.Cleanup()
	next := caddyhttp.HandlerFunc(func(http.ResponseWriter, *http.Request) error { t.Fatal("unexpected fallback"); return nil })
	visit := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		if err := m.ServeHTTP(w, testRequest("GET", "https://localhost/", nil), next); err != nil {
			t.Fatal(err)
		}
		return w
	}
	authenticate := func(cookie *http.Cookie) {
		body, err := cell.Encode(0, 4096, []cell.Frame{{Kind: cell.Auth, Body: []byte(testAuthorization)}})
		if err != nil {
			t.Fatal(err)
		}
		r := testRequest("POST", "https://localhost/api/sync", bytes.NewReader(body))
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		if err := m.ServeHTTP(w, r, next); err != nil || w.Code != 204 {
			t.Fatalf("auth response: %d %v", w.Code, err)
		}
	}
	first := visit().Result().Cookies()[0]
	authenticate(first)
	var last *http.Cookie
	for range 500 {
		w := visit()
		if w.Code != 200 || len(m.sessions) != 2 || m.sessions[first.Value] == nil {
			t.Fatal("anonymous visitor evicted authenticated session or exhausted capacity")
		}
		last = w.Result().Cookies()[0]
	}
	authenticate(last)
	if w := visit(); w.Code != 400 || len(m.sessions) != 2 {
		t.Fatal("active authenticated sessions should not be evicted")
	}
	// Active requests retain a session; inactivity expires it and frees capacity.
	m.sessions[first.Value].mu.Lock()
	m.sessions[first.Value].last = time.Now().Add(-3 * time.Minute)
	m.sessions[first.Value].mu.Unlock()
	m.expire(time.Now())
	if w := visit(); w.Code != 200 || len(m.sessions) != 2 || m.sessions[last.Value] == nil {
		t.Fatal("expired session did not free capacity")
	}
}
