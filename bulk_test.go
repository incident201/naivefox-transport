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
