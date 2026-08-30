package transport

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

func TestCaddyfileConfiguration(t *testing.T) {
	input := `naivefox_transport {
		key abcdefghijklmnopqrstuvwxyz012345
		profile continuous-bulk-pipeline
		allowed_targets example.com:443 [::1]:8080
		allowed_targets localhost:9090
		stats_path /tmp/transport-stats.json
	}`
	var handler Transport
	if err := handler.UnmarshalCaddyfile(caddyfile.NewTestDispenser(input)); err != nil {
		t.Fatal(err)
	}
	if handler.Key != "abcdefghijklmnopqrstuvwxyz012345" || handler.Profile != defaultProfile || handler.StatsPath != "/tmp/transport-stats.json" || handler.AppendMode || !reflect.DeepEqual(handler.AllowedTargets, []string{"example.com:443", "[::1]:8080", "localhost:9090"}) {
		t.Fatal("configuration changed")
	}
	// Provision validates the same explicit key/allowlist contract as JSON.
	handler.StatsPath = ""
	if err := handler.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}
	defer handler.Cleanup()
}

func TestCaddyfileRejectsAmbiguousOptions(t *testing.T) {
	for _, input := range []string{
		"naivefox_transport argument",
		"naivefox_transport {\n key\n}",
		"naivefox_transport {\n key first second\n}",
		"naivefox_transport {\n key first\n key second\n}",
		"naivefox_transport {\n allowed_targets\n}",
		"naivefox_transport {\n profile\n}",
		"naivefox_transport {\n stats_path\n}",
		"naivefox_transport {\n append_mode true\n}",
		"naivefox_transport {\n allow_all\n}",
	} {
		t.Run(input, func(t *testing.T) {
			var handler Transport
			if err := handler.UnmarshalCaddyfile(caddyfile.NewTestDispenser(input)); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}

func TestProfileHandshakeAndCoexistingHandler(t *testing.T) {
	for _, configured := range []string{"", defaultProfile, "continuous-v1"} {
		t.Run(configured, func(t *testing.T) {
			handler := &Transport{Profile: configured, Key: strings.Repeat("a", 32), AllowedTargets: []string{"localhost:9"}}
			if err := handler.Provision(caddy.Context{}); err != nil {
				t.Fatal(err)
			}
			defer handler.Cleanup()
			fallbacks := 0
			next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
				fallbacks++
				if r.Header.Get("Proxy-Authorization") != "Basic fixture" || r.Host != "example.com:443" {
					t.Error("fallback request mutated")
				}
				w.WriteHeader(http.StatusTeapot)
				return nil
			})
			for _, path := range []string{"/", "/assets/app.js", "/assets/site.css"} {
				w := httptest.NewRecorder()
				if err := handler.ServeHTTP(w, httptest.NewRequest("GET", "https://localhost"+path, nil), next); err != nil {
					t.Fatal(err)
				}
				expected := ""
				if path == "/" {
					expected = handler.profileName()
				}
				if w.Code != 200 || w.Header().Get("X-App-Profile") != expected {
					t.Fatalf("profile handshake on %s: %d %q", path, w.Code, w.Header().Get("X-App-Profile"))
				}
			}
			for _, method := range []string{"CONNECT", "GET"} {
				target := "https://example.com:443/unrelated"
				if method == "CONNECT" {
					target = "example.com:443"
				}
				r := httptest.NewRequest(method, target, bytes.NewReader([]byte("unchanged")))
				r.Header.Set("Proxy-Authorization", "Basic fixture")
				w := httptest.NewRecorder()
				if err := handler.ServeHTTP(w, r, next); err != nil {
					t.Fatal(err)
				}
				if w.Code != http.StatusTeapot || w.Header().Get("Set-Cookie") != "" || w.Header().Get("X-App-Profile") != "" {
					t.Fatal("classic or unrelated handler intercepted")
				}
			}
			if fallbacks != 2 || handler.stats.Connect != 1 {
				t.Fatal("handler fallthrough counters")
			}
		})
	}
}
