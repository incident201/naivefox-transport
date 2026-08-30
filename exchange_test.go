package transport

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/incident201/naivefox-transport/internal/cell"
)

func TestActiveExchangeCapacityAndOrdering(t *testing.T) {
	module := &Transport{Profile: "continuous-sync", Key: string(bytes.Repeat([]byte{'a'}, 32)), AllowedTargets: []string{"localhost:9"}}
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
	for index, state := range []string{"interactive", "download", "upload", "mixed"} {
		up, down := 4096, 8192
		if state == "upload" || state == "mixed" {
			up = 131072
		}
		if state == "download" || state == "mixed" {
			down = 65536
		}
		path := "/api/exchange/" + state
		body, _ := cell.Encode(uint32(index), up, nil)
		if request("GET", path, nil).Code != 400 {
			t.Fatal("method")
		}
		if request("POST", path, body[:len(body)-1]).Code != 400 {
			t.Fatal("truncated upload")
		}
		reply := request("POST", path, body)
		seq, frames, filler, err := cell.Decode(reply.Body.Bytes())
		if reply.Code != 200 || err != nil || seq != uint32(index) || len(frames) != 0 || filler != down-cell.Header || reply.Body.Len() != down || reply.Header().Get("X-App-Capacity") != strconv.Itoa(down) || reply.Header().Get("X-App-State") != "idle" {
			t.Fatal("response envelope")
		}
		if request("POST", path, body).Code != 400 {
			t.Fatal("replay")
		}
	}
	if request("POST", "/api/exchange/invalid", nil).Code != 404 {
		t.Fatal("unknown state")
	}
	module.Profile = "continuous-v1"
	if request("POST", "/api/exchange/interactive", nil).Code != 404 {
		t.Fatal("profile isolation")
	}
}
