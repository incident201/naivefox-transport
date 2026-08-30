package transport

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"naivefox.local/transport/internal/cell"
	"naivefox.local/transport/internal/mux"
)

func TestCreditHintIsBoundedByUsefulProgress(t *testing.T) {
	blocked := mux.Pressure{Streams: 1, Queued: 65536}
	for _, preserve := range []bool{false, true} {
		state, opportunity := downstreamState(blocked, 262144, 200000, preserve)
		if !opportunity || (preserve && state != "download") || (!preserve && state != "idle") {
			t.Fatal("credit handoff decision")
		}
		for _, useful := range []uint64{0, 131071} {
			state, opportunity := downstreamState(blocked, 262144, useful, preserve)
			if opportunity || state != "idle" {
				t.Fatal("stalled receiver retained bulk state")
			}
		}
		if state, opportunity := downstreamState(blocked, 65536, 200000, preserve); opportunity || state != "idle" {
			t.Fatal("non-bulk state changed")
		}
		if state, opportunity := downstreamState(mux.Pressure{Bytes: 32768, Queued: 32768}, 262144, 200000, preserve); opportunity || state != "download" {
			t.Fatal("ready data changed")
		}
	}
}

func TestBulkLeaseCapacityReplayAndIsolation(t *testing.T) {
	module := &Transport{Profile: "continuous-bulk", Key: string(bytes.Repeat([]byte{'a'}, 32)), AllowedTargets: []string{"localhost:9"}}
	if err := module.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}
	defer module.Cleanup()
	next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error { w.WriteHeader(404); return nil })
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
	body, _ := cell.Encode(0, 16384, []cell.Frame{{Kind: cell.Auth, Body: []byte(module.Key)}})
	if request("POST", "/api/sync/bulk", body[:len(body)-1]).Code != 400 {
		t.Fatal("short body")
	}
	if request("GET", "/api/sync/bulk", nil).Code != 400 {
		t.Fatal("method")
	}
	if request("POST", "/api/sync/bulk", body).Code != 204 {
		t.Fatal("upload")
	}
	if request("POST", "/api/sync/bulk", body).Code != 400 {
		t.Fatal("replay")
	}
	reply := request("GET", "/api/data/bulk", nil)
	seq, frames, filler, err := cell.Decode(reply.Body.Bytes())
	if reply.Code != 200 || err != nil || seq != 0 || len(frames) != 0 || reply.Body.Len() != 262144 || filler != 262144-cell.Header || reply.Header().Get("X-App-Capacity") != "262144" {
		t.Fatal("response capacity")
	}
	if module.stats.UploadBytes != 4*4096 || module.stats.DownloadBytes != 4*65536 {
		t.Fatal("aggregate budget")
	}
	module.Profile = "continuous-v1"
	if request("POST", "/api/sync/bulk", body).Code != 404 || request("GET", "/api/data/bulk", nil).Code != 404 {
		t.Fatal("profile isolation")
	}
}

func TestBulkDuplexKeepsOtherStatesUnchanged(t *testing.T) {
	module := &Transport{Profile: "continuous-bulk-duplex", Key: string(bytes.Repeat([]byte{'a'}, 32)), AllowedTargets: []string{"localhost:9"}}
	if err := module.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}
	defer module.Cleanup()
	next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error { w.WriteHeader(404); return nil })
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
	body, _ := cell.Encode(0, 16384, []cell.Frame{{Kind: cell.Auth, Body: []byte(module.Key)}})
	if request("POST", "/api/sync/bulk", body[:len(body)-1]).Code != 400 || request("GET", "/api/sync/bulk", nil).Code != 400 {
		t.Fatal("invalid upload")
	}
	reply := request("POST", "/api/sync/bulk", body)
	seq, _, _, err := cell.Decode(reply.Body.Bytes())
	if reply.Code != 200 || reply.Body.Len() != 262144 || err != nil || seq != 0 {
		t.Fatal("bulk response")
	}
	if request("POST", "/api/sync/bulk", body).Code != 400 {
		t.Fatal("replay")
	}
	if request("GET", "/api/data/bulk", nil).Code != 404 {
		t.Fatal("duplicate bulk path")
	}
	body, _ = cell.Encode(1, 4096, nil)
	if request("POST", "/api/sync", body).Code != 204 {
		t.Fatal("interactive post changed")
	}
	reply = request("GET", "/api/data/interactive", nil)
	if reply.Code != 200 || reply.Body.Len() != 8192 {
		t.Fatal("interactive response changed")
	}
}
