package transport

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/incident201/naivefox-transport/internal/cell"
)

func assertNoApplicationHeaders(t *testing.T, headers http.Header) {
	t.Helper()
	for name := range headers {
		if strings.HasPrefix(strings.ToLower(name), "x-app-") {
			t.Fatalf("public transport header: %s", name)
		}
	}
}

func TestPublicSurfaceAndAnonymousCarrierFallback(t *testing.T) {
	f := newRealtimeFixture(t)
	for _, path := range []string{"/", "/assets/app.js", "/assets/site.css"} {
		for _, method := range []string{"GET", "HEAD"} {
			request, _ := http.NewRequest(method, f.server.URL+path, nil)
			response, err := f.server.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			assertNoApplicationHeaders(t, response.Header)
			response.Body.Close()
			if response.StatusCode != 200 {
				t.Fatal("public site failed")
			}
			for _, cookie := range response.Cookies() {
				if cookie.Name != "session" {
					t.Fatal("unexpected public cookie name")
				}
			}
		}
	}
	empty, _ := cell.Encode(0, 4096, nil)
	wrong, _ := cell.Encode(0, 4096, []cell.Frame{{Kind: cell.Auth, Body: []byte("Basic invalid")}})
	mixed, _ := cell.Encode(0, 4096, []cell.Frame{
		{Kind: cell.Auth, Body: []byte(testAuthorization)},
		{Kind: cell.Open, Stream: 1, Body: []byte("127.0.0.1:9")},
	})
	for _, cookie := range []bool{false, true} {
		for _, probe := range []struct {
			method, path string
			body         []byte
		}{
			{"GET", "/api/events/brief", nil}, {"GET", "/api/events/state", nil},
			{"GET", "/media/chunk/6", nil}, {"GET", "/api/realtime", nil},
			{"POST", "/api/sync", empty}, {"POST", "/api/sync", wrong},
			{"POST", "/api/sync", mixed}, {"POST", "/api/sync", []byte("invalid")},
		} {
			request, _ := http.NewRequest(probe.method, f.server.URL+probe.path, bytes.NewReader(probe.body))
			if cookie {
				request.AddCookie(f.cookie)
			}
			response, err := f.server.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			assertNoApplicationHeaders(t, response.Header)
			response.Body.Close()
			if response.StatusCode != 404 || len(response.Cookies()) != 0 {
				t.Fatalf("anonymous request disclosed carrier: %s %s", probe.method, probe.path)
			}
		}
	}
	s := f.module.sessions[f.cookie.Value]
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.authed || s.up != 0 || s.down != 0 || s.startupSteps != 0 {
		t.Fatal("anonymous probe advanced the carrier")
	}
}

func TestOpenRequiresAuthenticatedConfirmation(t *testing.T) {
	f := newRealtimeFixture(t)
	auth, _ := cell.Encode(0, 4096, []cell.Frame{{Kind: cell.Auth, Body: []byte(testAuthorization)}})
	if status, _ := f.request("POST", "/api/sync", auth); status != 204 {
		t.Fatal("AUTH failed")
	}
	open, _ := cell.Encode(1, 4096, []cell.Frame{{Kind: cell.Open, Stream: 1, Body: []byte("127.0.0.1:9")}})
	if status, _ := f.request("POST", "/api/sync", open); status != 400 {
		t.Fatal("OPEN preceded confirmation")
	}
	if f.module.stats.Opens != 0 {
		t.Fatal("early OPEN reached target policy")
	}
}

func TestAnonymousFallbackPreservesRequestBody(t *testing.T) {
	f := newRealtimeFixture(t)
	for _, size := range []int{0, 16, 4096, 4097, 16384} {
		body := bytes.Repeat([]byte("x"), size)
		request := testRequest("POST", "https://localhost/api/sync", bytes.NewReader(body))
		request.RemoteAddr = "127.0.0.1:12345"
		request.AddCookie(f.cookie)
		response := httptest.NewRecorder()
		next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
			if r.Method != "POST" || r.URL.Path != "/api/sync" || r.ContentLength != int64(size) {
				t.Fatal("fallback request metadata changed")
			}
			received, err := io.ReadAll(r.Body)
			if err != nil || !bytes.Equal(received, body) {
				t.Fatal("fallback body changed")
			}
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(418)
			_, err = w.Write(received)
			return err
		})
		if err := f.module.ServeHTTP(response, request, next); err != nil {
			t.Fatal(err)
		}
		assertNoApplicationHeaders(t, response.Header())
		if response.Code != 418 || !bytes.Equal(response.Body.Bytes(), body) {
			t.Fatal("fallback response changed")
		}
	}
}
