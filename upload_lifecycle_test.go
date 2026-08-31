package transport

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/incident201/naivefox-transport/internal/cell"
)

type gatedUpload struct {
	reader  io.Reader
	started chan struct{}
	release chan struct{}
	reading sync.Once
	closing sync.Once
}

func (b *gatedUpload) Read(p []byte) (int, error) {
	b.reading.Do(func() { close(b.started) })
	<-b.release
	return b.reader.Read(p)
}

func (b *gatedUpload) Close() error {
	b.closing.Do(func() { close(b.release) })
	return nil
}

func TestStalledUploadDoesNotBlockSessionLifecycle(t *testing.T) {
	for _, operation := range []string{"expire", "cleanup", "cancel"} {
		t.Run(operation, func(t *testing.T) {
			module := &Transport{ForwardProxy: testForwardProxy()}
			if err := module.Provision(testCaddyContext(t)); err != nil {
				t.Fatal(err)
			}
			cleaned := false
			defer func() {
				if !cleaned {
					module.Cleanup()
				}
			}()
			next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) error {
				w.WriteHeader(404)
				return nil
			})
			root := httptest.NewRecorder()
			if err := module.ServeHTTP(root, testRequest("GET", "https://localhost/", nil), next); err != nil {
				t.Fatal(err)
			}
			cookie := root.Result().Cookies()[0]
			s := module.sessions[cookie.Value]
			encoded, err := cell.Encode(0, 4096, []cell.Frame{{Kind: cell.Auth, Body: []byte(testAuthorization)}})
			if err != nil {
				t.Fatal(err)
			}
			body := &gatedUpload{reader: bytes.NewReader(encoded), started: make(chan struct{}), release: make(chan struct{})}
			defer body.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			request := testRequest("POST", "https://localhost/api/sync", nil).WithContext(ctx)
			request.AddCookie(cookie)
			request.Body = body
			upload := httptest.NewRecorder()
			done := make(chan error, 1)
			go func() { done <- module.ServeHTTP(upload, request, next) }()
			select {
			case <-body.started:
			case <-time.After(time.Second):
				t.Fatal("upload did not begin reading")
			}

			operated := make(chan error, 1)
			switch operation {
			case "expire":
				go func() { module.expire(time.Now().Add(3 * time.Minute)); operated <- nil }()
			case "cleanup":
				cleaned = true
				go func() { operated <- module.Cleanup() }()
			case "cancel":
				cancel()
				operated <- nil
			}
			select {
			case err := <-operated:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("stalled request blocked " + operation)
			}
			if operation != "cleanup" {
				otherDone := make(chan error, 1)
				other := httptest.NewRecorder()
				go func() {
					otherDone <- module.ServeHTTP(other, testRequest("GET", "https://localhost/", nil), next)
				}()
				select {
				case err := <-otherDone:
					if err != nil || other.Code != 200 {
						t.Fatal("another session could not start")
					}
				case <-time.After(time.Second):
					t.Fatal("stalled upload blocked another session")
				}
			} else {
				other := httptest.NewRecorder()
				if err := module.ServeHTTP(other, testRequest("GET", "https://localhost/", nil), next); err != nil || other.Code != 400 || len(module.sessions) != 0 {
					t.Fatal("cleanup allowed a new session after the expiry loop stopped")
				}
			}
			body.Close()
			select {
			case err := <-done:
				if err != nil || upload.Code != 400 {
					t.Fatalf("stale/cancelled upload accepted: status=%d error=%v", upload.Code, err)
				}
			case <-time.After(time.Second):
				t.Fatal("released upload did not finish")
			}
			s.mu.Lock()
			advanced := s.up != 0 || s.authed
			s.mu.Unlock()
			if advanced {
				t.Fatal("failed upload changed authentication or sequence")
			}
			if operation == "cancel" {
				retry := testRequest("POST", "https://localhost/api/sync", bytes.NewReader(encoded))
				retry.AddCookie(cookie)
				w := httptest.NewRecorder()
				if err := module.ServeHTTP(w, retry, next); err != nil || w.Code != 204 {
					t.Fatal("cancelled body consumed the valid sequence")
				}
			}
		})
	}
}

func TestConcurrentUploadsCommitOneSequence(t *testing.T) {
	module := &Transport{ForwardProxy: testForwardProxy()}
	if err := module.Provision(testCaddyContext(t)); err != nil {
		t.Fatal(err)
	}
	defer module.Cleanup()
	next := caddyhttp.HandlerFunc(func(http.ResponseWriter, *http.Request) error { return nil })
	root := httptest.NewRecorder()
	if err := module.ServeHTTP(root, testRequest("GET", "https://localhost/", nil), next); err != nil {
		t.Fatal(err)
	}
	cookie := root.Result().Cookies()[0]
	body, err := cell.Encode(0, 4096, []cell.Frame{{Kind: cell.Auth, Body: []byte(testAuthorization)}})
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{})
	results := make(chan int, 2)
	for range 2 {
		go func() {
			r := testRequest("POST", "https://localhost/api/sync", bytes.NewReader(body))
			r.AddCookie(cookie)
			w := httptest.NewRecorder()
			<-ready
			if err := module.ServeHTTP(w, r, next); err != nil {
				results <- 0
				return
			}
			results <- w.Code
		}()
	}
	close(ready)
	first, second := <-results, <-results
	if !((first == 204 && second == 400) || (first == 400 && second == 204)) {
		t.Fatalf("duplicate sequence results: %d, %d", first, second)
	}
	s := module.sessions[cookie.Value]
	if s.up != 1 || !s.authed || module.stats.UploadBytes != 4096 {
		t.Fatal("duplicate upload changed committed state")
	}
}
