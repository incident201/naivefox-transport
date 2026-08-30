package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"naivefox.local/transport/internal/cell"
	"naivefox.local/transport/internal/mux"
)

func TestApplicationCapacityAuthAndReplay(t *testing.T) {
	module := &Transport{Key: string(bytes.Repeat([]byte{'a'}, 32)), AllowedTargets: []string{"localhost:9"}}
	if err := module.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}
	defer module.Cleanup()
	next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error { w.WriteHeader(404); return nil })
	root := httptest.NewRecorder()
	module.ServeHTTP(root, httptest.NewRequest("GET", "https://localhost/", nil), next)
	if root.Code != 200 || root.Body.Len() != 4096 {
		t.Fatal("root")
	}
	cookie := root.Result().Cookies()[0]
	request := func(method, path string, body []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://localhost"+path, bytes.NewReader(body))
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		if err := module.ServeHTTP(w, r, next); err != nil {
			t.Fatal(err)
		}
		return w
	}
	open := cell.Frame{Kind: cell.Open, Stream: 1, Body: []byte("localhost:9")}
	unauthorized, _ := cell.Encode(0, 4096, []cell.Frame{open})
	if request("POST", "/api/sync", unauthorized).Code != 400 {
		t.Fatal("unauthenticated open")
	}
	if module.stats.Opens != 0 {
		t.Fatal("unauthenticated dial")
	}
	wrong, _ := cell.Encode(0, 4096, []cell.Frame{{Kind: cell.Auth, Body: bytes.Repeat([]byte{'b'}, 32)}})
	if request("POST", "/api/sync", wrong).Code != 400 {
		t.Fatal("wrong key")
	}
	valid, _ := cell.Encode(0, 4096, []cell.Frame{{Kind: cell.Auth, Body: []byte(module.Key)}})
	if request("POST", "/api/sync", valid).Code != 204 {
		t.Fatal("auth")
	}
	if request("POST", "/api/sync", valid).Code != 400 {
		t.Fatal("replay")
	}
	for _, path := range []string{"/api/events", "/media/chunk/2"} {
		w := request("GET", path, nil)
		capacity := 24576
		if path != "/api/events" {
			capacity = 131072
		}
		if w.Code != 200 || w.Body.Len() != capacity {
			t.Fatal("capacity")
		}
		_, frames, filler, err := cell.Decode(w.Body.Bytes())
		if err != nil || len(frames) != 0 || filler != capacity-cell.Header {
			t.Fatal("filler")
		}
	}
	for _, path := range []string{"/assets/app.js", "/assets/site.css", "/assets/image-1.svg"} {
		w := request("GET", path, nil)
		spec, _ := assetDefinition(path)
		if w.Code != 200 || w.Body.Len() != spec.size {
			t.Fatal("asset")
		}
	}
}

func TestModuleRequiresAllowlist(t *testing.T) {
	for _, m := range []Transport{{}, {Key: string(bytes.Repeat([]byte{'a'}, 32))}, {Key: string(bytes.Repeat([]byte{'a'}, 32)), AllowedTargets: []string{"invalid"}}} {
		if err := m.Provision(caddy.Context{}); err == nil {
			m.Cleanup()
			t.Fatal("unsafe configuration")
		}
	}
}

func TestFixedProfiles(t *testing.T) {
	budgets := map[string]int{"v1": 1671168, "duplex-v1": 1671168, "compact": 884736, "compact-sync": 884736, "compact-sync20": 1146880, "compact-fast20": 1146880, "staged": 770048, "staged-fast": 770048, "staged-fast20": 901120, "staged-stream20": 901120, "staged-commit20": 905216, "continuous-v1": 901120, "continuous-sync": 901120, "continuous-sync2": 901120}
	budgets["continuous-bulk"] = 901120
	budgets["continuous-bulk-ready"] = 901120
	budgets["continuous-bulk-frames"] = 901120
	budgets["continuous-bulk-duplex"] = 901120
	budgets["continuous-bulk-interactive1"] = 901120
	budgets["continuous-bulk-upload1"] = 901120
	budgets["continuous-bulk-noack"] = 901120
	budgets["continuous-bulk-noack-download"] = 901120
	budgets["continuous-bulk-window512"] = 901120
	budgets["continuous-bulk-filler"] = 901120
	if len(budgets) != len(profiles) {
		t.Fatal("every profile requires a frozen budget")
	}
	for name, profile := range profiles {
		t.Run(name, func(t *testing.T) {
			module := &Transport{Profile: name, Key: string(bytes.Repeat([]byte{'a'}, 32)), AllowedTargets: []string{"localhost:9"}}
			if err := module.Provision(caddy.Context{}); err != nil {
				t.Fatal(err)
			}
			defer module.Cleanup()
			next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error { t.Error("unexpected fallback"); return nil })
			root := httptest.NewRecorder()
			module.ServeHTTP(root, httptest.NewRequest("GET", "https://localhost/", nil), next)
			cookie := root.Result().Cookies()[0]
			request := func(method, path string, body []byte) *httptest.ResponseRecorder {
				r := httptest.NewRequest(method, "https://localhost"+path, bytes.NewReader(body))
				r.AddCookie(cookie)
				w := httptest.NewRecorder()
				if err := module.ServeHTTP(w, r, next); err != nil {
					t.Fatal(err)
				}
				return w
			}
			js := request("GET", "/assets/app.js", nil)
			encoded, _ := json.Marshal(profile)
			if js.Code != 200 || js.Body.Len() != 24576 || !bytes.Contains(js.Body.Bytes(), encoded) || bytes.Contains(js.Body.Bytes(), []byte("__NFC_PROFILE__")) {
				t.Fatal("fixed script profile")
			}
			for round := 0; round < profile.Rounds; round++ {
				media := round >= 2 && round < profile.Rounds-2
				capacity := 24576
				if media {
					capacity = profile.Down
				}
				if profile.Slots != nil {
					capacity = profile.Slots[round]
				}
				path := "/api/sync"
				if profile.Duplex && media {
					path += "/media"
				}
				body, _ := cell.Encode(uint32(round), 4096, nil)
				w := request("POST", path, body)
				if !profile.Duplex {
					if w.Code != 204 || w.Body.Len() != 0 {
						t.Fatal("sync acknowledgment")
					}
					path = "/media/chunk/" + strconv.Itoa(round)
					switch capacity {
					case 8192:
						path = "/api/events/brief"
					case 32768:
						path = "/api/events/state"
					case 24576:
						path = "/api/events"
					}
					w = request("GET", path, nil)
				}
				seq, frames, filler, err := cell.Decode(w.Body.Bytes())
				if w.Code != 200 || err != nil || seq != uint32(round) || len(frames) != 0 || filler != capacity-cell.Header || w.Body.Len() != capacity || w.Header().Get("X-App-Capacity") != strconv.Itoa(capacity) || w.Header().Get("Cache-Control") != "no-store" {
					t.Fatalf("round %d capacity/sequence", round)
				}
			}
			expectedUpload := profile.Rounds * 4096
			if profile.Commit {
				body, _ := cell.Encode(uint32(profile.Rounds), 4096, nil)
				w := request("POST", "/api/action", body)
				seq, frames, filler, err := cell.Decode(w.Body.Bytes())
				if w.Code != 200 || w.Body.Len() != 4096 || seq != uint32(profile.Rounds) || err != nil || len(frames) != 0 || filler != 4080 {
					t.Fatal("terminal confirmation")
				}
				expectedUpload += 4096
			}
			if module.stats.DownloadBytes != uint64(budgets[name]) || module.stats.UploadBytes != uint64(expectedUpload) || module.stats.Opens != 0 || module.stats.Rejected != 0 {
				t.Fatal("empty visitor budget or authorization")
			}
		})
	}
}

func TestUnknownProfileRejected(t *testing.T) {
	module := &Transport{Profile: "typo", Key: string(bytes.Repeat([]byte{'a'}, 32)), AllowedTargets: []string{"localhost:9"}}
	if err := module.Provision(caddy.Context{}); err == nil {
		module.Cleanup()
		t.Fatal("unknown profile accepted")
	}
}

func TestIdleWakeCancellationAndSinglePoll(t *testing.T) {
	peer := mux.New(nil)
	defer peer.Close()
	s := &session{peer: peer, wake: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- waitIdle(ctx, s, time.Second) }()
	deadline := time.Now().Add(time.Second)
	for {
		s.mu.Lock()
		active := s.idle
		s.mu.Unlock()
		if active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("idle did not start")
		}
		time.Sleep(time.Millisecond)
	}
	if err := waitIdle(ctx, s, time.Second); err == nil {
		t.Fatal("overlapping idle poll")
	}
	s.mu.Lock()
	s.up++
	s.mu.Unlock()
	s.wake <- struct{}{}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("upload did not wake idle poll")
	}
	go func() { done <- waitIdle(ctx, s, time.Second) }()
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("cancel: %v", err)
	}
	if err := waitIdle(context.Background(), s, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	peer.Close()
	if err := waitIdle(context.Background(), s, time.Second); err != context.Canceled {
		t.Fatalf("closed peer: %v", err)
	}
}
