package transport

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"naivefox.local/transport/internal/cell"
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
