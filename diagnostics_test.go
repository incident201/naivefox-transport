package transport

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

func TestDiagnosticsRequireExplicitPrivateOptIn(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		module := &Transport{ApplicationRoot: testApplicationRoot(t), ForwardProxy: testForwardProxy(), Diagnostics: enabled}
		if err := module.Provision(testCaddyContext(t)); err != nil {
			t.Fatal(err)
		}
		for _, operation := range []struct{ method, path, auth string }{
			{http.MethodGet, "/__lab/stats", testAuthorization},
			{http.MethodGet, "/__lab/stats", ""},
			{http.MethodGet, "/__lab/stats", "Basic wrong"},
			{http.MethodDelete, "/__lab/sessions", testAuthorization},
			{http.MethodPost, "/__lab/stats", testAuthorization},
		} {
			request := testRequest(operation.method, "https://localhost"+operation.path, nil)
			request.Header.Set("Authorization", operation.auth)
			response := httptest.NewRecorder()
			err := module.ServeHTTP(response, request, caddyhttp.HandlerFunc(func(http.ResponseWriter, *http.Request) error { t.Fatal("diagnostic fell through"); return nil }))
			if err != nil {
				t.Fatal(err)
			}
			expected := http.StatusNotFound
			if enabled && operation.method == http.MethodGet && operation.path == "/__lab/stats" && operation.auth == testAuthorization {
				expected = http.StatusOK
			}
			if response.Code != expected {
				t.Fatalf("enabled=%v %s %s: %d", enabled, operation.method, operation.path, response.Code)
			}
			for _, secret := range []string{"fixture", testAuthorization, "localhost"} {
				if strings.Contains(response.Body.String(), secret) {
					t.Fatal("diagnostic response contains credentials or addresses")
				}
			}
		}
		module.Cleanup()
	}
}
