package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/incident201/naivefox-transport/internal/cell"
)

func TestSessionClientIPTrustAndNormalization(t *testing.T) {
	for _, tc := range []struct {
		peer, forwarded, client string
		trusted                 bool
		want                    string
	}{
		{"192.0.2.1:123", "198.51.100.1", "198.51.100.1", false, "192.0.2.1"},
		{"192.0.2.2:123", "198.51.100.2", "198.51.100.1", true, "198.51.100.1"},
		{"[::ffff:192.0.2.1]:123", "", "", false, "192.0.2.1"},
		{"192.0.2.2:123", "", "invalid", true, ""},
	} {
		r := httptest.NewRequest("GET", "https://localhost/", nil)
		r.RemoteAddr = tc.peer
		r.Header.Set("X-Forwarded-For", tc.forwarded)
		r = r.WithContext(context.WithValue(r.Context(), caddyhttp.VarsCtxKey, map[string]any{
			caddyhttp.TrustedProxyVarKey: tc.trusted,
			caddyhttp.ClientIPVarKey:     tc.client,
		}))
		got, err := sessionClientIP(r)
		if got != tc.want || (err != nil) != (tc.want == "") {
			t.Fatalf("peer %q trusted %v: %q %v", tc.peer, tc.trusted, got, err)
		}
	}
}

func TestSessionSurvivesTrustedProxyAddressChange(t *testing.T) {
	f := newRealtimeFixture(t)
	request := func(peer, client string, trusted bool, cookie *http.Cookie) (*session, error) {
		r := testRequest("GET", "https://localhost/api/events/brief?seq=0", nil)
		r.RemoteAddr = peer
		r = r.WithContext(context.WithValue(r.Context(), caddyhttp.VarsCtxKey, map[string]any{
			caddyhttp.TrustedProxyVarKey: trusted, caddyhttp.ClientIPVarKey: client,
		}))
		r.AddCookie(cookie)
		return f.module.getSession(httptest.NewRecorder(), r)
	}
	s := f.module.sessions[f.cookie.Value]
	s.ip = "198.51.100.1"
	for _, peer := range []string{"192.0.2.1:123", "192.0.2.2:456"} {
		if got, err := request(peer, s.ip, true, f.cookie); err != nil || got != s {
			t.Fatal("trusted edge change lost session", err)
		}
		if _, err := request(peer, s.ip, false, f.cookie); err == nil {
			t.Fatal("untrusted forwarded address accepted")
		}
	}
	if _, err := request("192.0.2.1:123", "198.51.100.2", true, f.cookie); err == nil {
		t.Fatal("different verified client reused the session")
	}
}

type failedStartupWriter struct{ header http.Header }

func (w *failedStartupWriter) Header() http.Header       { return w.header }
func (w *failedStartupWriter) WriteHeader(int)           {}
func (w *failedStartupWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestStartupLostResponseAndConcurrentReplay(t *testing.T) {
	f := newRealtimeFixture(t)
	auth, _ := cell.Encode(0, 4096, []cell.Frame{{Kind: cell.Auth, Body: []byte(testAuthorization)}})
	if status, _ := f.request("POST", "/api/sync", auth); status != 204 {
		t.Fatal(status)
	}
	r := testRequest("GET", "https://localhost/api/events/brief?seq=0", nil)
	r.RemoteAddr = "127.0.0.1:123"
	r.AddCookie(f.cookie)
	if err := f.module.ServeHTTP(&failedStartupWriter{make(http.Header)}, r, nil); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal("lost write not exercised", err)
	}
	s := f.module.sessions[f.cookie.Value]
	if s.startupSteps != 2 || s.down != 1 {
		t.Fatal("logical operation depends on response delivery")
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, body := f.request("GET", "/api/events/brief?seq=0", nil)
			if status != 200 || !bytes.Equal(body, s.startupJournal[1].body) {
				t.Error("GET replay changed its result")
			}
		}()
	}
	wg.Wait()
	if s.down != 1 || s.startupSteps != 2 || f.module.stats.DownloadBytes != 8192 {
		t.Fatal("replays consumed data or advanced logical counters")
	}
	conflict := append([]byte(nil), auth...)
	conflict[len(conflict)-1] ^= 1
	if status, _ := f.request("POST", "/api/sync", conflict); status != 400 {
		t.Fatal("conflicting POST accepted")
	}
	if status, _ := f.request("POST", "/api/sync", auth); status != 204 {
		t.Fatal("identical AUTH retry rejected")
	}
	if s.up != 1 || f.module.stats.UploadBytes != 4096 {
		t.Fatal("POST retry was applied twice")
	}
}

func TestStartupJournalRetirementAndExpiry(t *testing.T) {
	for _, mode := range []string{"websocket", "expiry", "close"} {
		t.Run(mode, func(t *testing.T) {
			before := startupJournalBytes.Load()
			f := newRealtimeFixture(t)
			f.bootstrap(true, nil)
			s := f.module.sessions[f.cookie.Value]
			if s.startupBytes != 880*1024 || startupJournalBytes.Load() != before+880*1024 {
				t.Fatal("startup retention bound changed")
			}
			switch mode {
			case "websocket":
				conn, _, err := f.dial()
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
			case "expiry":
				s.mu.Lock()
				s.startupDeadline = time.Now().Add(-time.Second)
				s.mu.Unlock()
				f.module.expire(time.Now())
			case "close":
				s.close()
			}
			s.mu.Lock()
			retained := s.startupBytes
			s.mu.Unlock()
			if retained != 0 || startupJournalBytes.Load() != before {
				t.Fatal("journal reservation leaked")
			}
			status, _ := f.request("GET", "/api/events/brief?seq=0", nil)
			if status != 400 && status != 404 {
				t.Fatal("retired operation executed again")
			}
		})
	}
}

func TestStartupJournalBudgetAndUnambiguousIdentity(t *testing.T) {
	f := newRealtimeFixture(t)
	auth, _ := cell.Encode(0, 4096, []cell.Frame{{Kind: cell.Auth, Body: []byte(testAuthorization)}})
	if status, _ := f.request("POST", "/api/sync", auth); status != 204 {
		t.Fatal(status)
	}
	s := f.module.sessions[f.cookie.Value]
	for _, query := range []string{"", "?seq=00", "?seq=0&seq=1", "?seq=-1", "?seq=20", "?seq=0&x=1"} {
		if status, _ := f.request("GET", "/api/events/brief"+query, nil); status != 400 {
			t.Fatal(query, status)
		}
	}
	reserved := int64(startupJournalLimit) - startupJournalBytes.Load()
	if !reserveStartupBytes(reserved) {
		t.Fatal("reserve limit")
	}
	status, _ := f.request("GET", "/api/events/brief?seq=0", nil)
	startupJournalBytes.Add(-reserved)
	if status != 503 || s.down != 0 || s.startupSteps != 1 {
		t.Fatal("budget failure consumed operation")
	}
	if status, _ := f.request("GET", "/api/events/brief?seq="+strconv.Itoa(0), nil); status != 200 {
		t.Fatal("capacity recovery", status)
	}
}

type blockedStartupWriter struct {
	header          http.Header
	started, finish chan struct{}
}

func (w *blockedStartupWriter) Header() http.Header { return w.header }
func (w *blockedStartupWriter) WriteHeader(int)     {}
func (w *blockedStartupWriter) Write(body []byte) (int, error) {
	close(w.started)
	<-w.finish
	return len(body), nil
}
func TestStartupInFlightWriteRetainsQuotaAfterSessionClose(t *testing.T) {
	before := startupJournalBytes.Load()
	f := newRealtimeFixture(t)
	auth, _ := cell.Encode(0, 4096, []cell.Frame{{Kind: cell.Auth, Body: []byte(testAuthorization)}})
	if status, _ := f.request("POST", "/api/sync", auth); status != 204 {
		t.Fatal(status)
	}
	request := testRequest("GET", "https://localhost/api/events/brief?seq=0", nil)
	request.RemoteAddr = "127.0.0.1:123"
	request.AddCookie(f.cookie)
	w := &blockedStartupWriter{header: make(http.Header), started: make(chan struct{}), finish: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- f.module.ServeHTTP(w, request, nil) }()
	select {
	case <-w.started:
	case <-time.After(time.Second):
		t.Fatal("writer did not start")
	}
	s := f.module.sessions[f.cookie.Value]
	s.close()
	if s.startupBytes != 0 || startupJournalBytes.Load() != before+8192 {
		close(w.finish)
		t.Fatal("in-flight response escaped quota on journal retirement")
	}
	close(w.finish)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if startupJournalBytes.Load() != before {
		t.Fatal("writer reservation leaked")
	}
}

func TestNoTransformOnlyForValidatedProxy(t *testing.T) {
	for _, trusted := range []bool{false, true} {
		r := testRequest("GET", "https://localhost/", nil)
		r.Header.Set("CF-Connecting-IP", "198.51.100.1")
		r = r.WithContext(context.WithValue(r.Context(), caddyhttp.VarsCtxKey, map[string]any{caddyhttp.TrustedProxyVarKey: trusted}))
		for _, public := range []bool{false, true} {
			want := "no-store"
			if public {
				want = "public, max-age=3600"
			}
			if trusted {
				want += ", no-transform"
			}
			if responseCacheControl(r, public) != want {
				t.Fatal("unexpected cache/representation policy")
			}
		}
	}
}
