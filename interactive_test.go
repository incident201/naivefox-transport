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

func TestInteractiveOnlyRouteBudgetReplayAndIsolation(t *testing.T) {
	m := &Transport{Profile: "continuous-bulk-pipeline-interactive", Key: string(bytes.Repeat([]byte{'a'}, 32)), AllowedTargets: []string{"localhost:9"}}
	if err := m.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}
	defer m.Cleanup()
	next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error { w.WriteHeader(404); return nil })
	root := httptest.NewRecorder()
	m.ServeHTTP(root, httptest.NewRequest("GET", "https://localhost/", nil), next)
	cookie := root.Result().Cookies()[0]
	request := func(method, path string, body []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://localhost"+path, bytes.NewReader(body))
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		if err := m.ServeHTTP(w, r, next); err != nil {
			t.Fatal(err)
		}
		return w
	}
	body, _ := cell.Encode(0, 4096, []cell.Frame{{Kind: cell.Auth, Body: []byte(m.Key)}})
	reply := request("POST", "/api/exchange/interactive", body)
	seq, _, _, err := cell.Decode(reply.Body.Bytes())
	if err != nil || reply.Code != 200 || reply.Body.Len() != 8192 || seq != 0 {
		t.Fatal("interactive response")
	}
	if request("POST", "/api/exchange/interactive", body).Code != 400 {
		t.Fatal("replay")
	}
	if request("GET", "/api/data/interactive", nil).Code != 404 {
		t.Fatal("duplicate GET")
	}
	for _, state := range []string{"upload", "mixed", "download"} {
		if request("POST", "/api/exchange/"+state, body).Code != 404 {
			t.Fatal("unrelated combined route")
		}
	}
	body, _ = cell.Encode(1, 131072, nil)
	if request("POST", "/api/upload/chunk", body).Code != 204 || request("GET", "/api/data/upload", nil).Body.Len() != 8192 {
		t.Fatal("upload changed")
	}
	body, _ = cell.Encode(2, 4096, nil)
	if request("POST", "/api/sync", body).Code != 204 || request("GET", "/api/events/brief", nil).Body.Len() != 8192 {
		t.Fatal("startup changed")
	}
}
