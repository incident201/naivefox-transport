package transport

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

func TestUntrustedRequestLabelsRemainBounded(t *testing.T) {
	module := &Transport{ApplicationRoot: testApplicationRoot(t), ForwardProxy: testForwardProxy()}
	if err := module.Provision(testCaddyContext(t)); err != nil {
		t.Fatal(err)
	}
	defer module.Cleanup()
	next := caddyhttp.HandlerFunc(func(http.ResponseWriter, *http.Request) error {
		t.Fatal("carrier route unexpectedly delegated")
		return nil
	})
	const requests = 4096
	for index := 0; index < requests; index++ {
		r := testRequest("UNSUPPORTED"+strconv.Itoa(index), "https://localhost/media/chunk/"+strconv.Itoa(index), nil)
		r.Proto = "UNKNOWN/" + strconv.Itoa(index)
		w := httptest.NewRecorder()
		if err := module.ServeHTTP(w, r, next); err != nil || w.Code != 400 {
			t.Fatalf("unauthenticated request %d: %v, %d", index, err, w.Code)
		}
	}
	if len(module.stats.Requests) != 1 || module.stats.Requests["OTHER /media/chunk/*"] != requests || len(module.stats.Protocols) != 1 || module.stats.Protocols["OTHER"] != requests {
		t.Fatal("untrusted paths, methods or protocols created unbounded metric keys")
	}
	for _, path := range []string{"/media/chunk/6", "/media/chunk/17", "/api/events/brief"} {
		w := httptest.NewRecorder()
		if err := module.ServeHTTP(w, testRequest("GET", "https://localhost"+path, nil), next); err != nil || w.Code != 400 {
			t.Fatal("unauthenticated GET changed behavior")
		}
	}
	if module.stats.Requests["GET /media/chunk/*"] != 2 || module.stats.Requests["GET /api/events/brief"] != 1 || module.stats.Protocols["HTTP/1.1"] != 3 {
		t.Fatal("normal route/method/protocol accounting changed")
	}
}
