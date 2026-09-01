package transport

import (
	"bytes"
	"github.com/caddyserver/forwardproxy"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

func TestCaddyfileConfiguration(t *testing.T) {
	input := `naivefox_transport {
        application_root /absolute/application
        profile continuous-bulk-pipeline
        forward_proxy {
            basic_auth fixture fixture
            basic_auth second p:a:ss
            hide_ip
            hide_via
            probe_resistance
        }
        stats_path /tmp/transport-stats.json
    }`
	var handler Transport
	if err := handler.UnmarshalCaddyfile(caddyfile.NewTestDispenser(input)); err != nil {
		t.Fatal(err)
	}
	if handler.ApplicationRoot != "/absolute/application" || handler.Profile != defaultProfile || handler.StatsPath != "/tmp/transport-stats.json" || handler.AppendMode || handler.ForwardProxy == nil || len(handler.ForwardProxy.AuthCredentials) != 2 || !bytes.Equal(handler.ForwardProxy.AuthCredentials[1], forwardproxy.EncodeAuthCredentials("second", "p:a:ss")) {
		t.Fatal("configuration changed")
	}
	handler.StatsPath = ""
	handler.ApplicationRoot = testApplicationRoot(t)
	if err := handler.Provision(testCaddyContext(t)); err != nil {
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
		"naivefox_transport {\n application_root\n}",
		"naivefox_transport {\n application_root /one /two\n}",
		"naivefox_transport {\n application_root /one\n application_root /two\n}",
		"naivefox_transport {\n stats_path\n}",
		"naivefox_transport {\n append_mode true\n}",
		"naivefox_transport {\n allow_all\n}",
		"naivefox_transport {\n max_sessions 0\n}",
		"naivefox_transport {\n max_sessions -1\n}",
		"naivefox_transport {\n max_sessions many\n}",
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
			handler := &Transport{ApplicationRoot: testApplicationRoot(t), Profile: configured, ForwardProxy: testForwardProxy()}
			if err := handler.Provision(testCaddyContext(t)); err != nil {
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
				if err := handler.ServeHTTP(w, testRequest("GET", "https://localhost"+path, nil), next); err != nil {
					t.Fatal(err)
				}
				expected := ""
				if path == "/" {
					expected = handler.profileName()
				}
				if w.Code != 200 || w.Header().Get("X-App-Profile") != expected || (path == "/" && w.Header().Get("X-App-Auth") != "basic") {
					t.Fatalf("profile handshake on %s: %d %q", path, w.Code, w.Header().Get("X-App-Profile"))
				}
			}
			for _, method := range []string{"CONNECT", "GET"} {
				target := "https://example.com:443/unrelated"
				if method == "CONNECT" {
					target = "example.com:443"
				}
				r := testRequest(method, target, bytes.NewReader([]byte("unchanged")))
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
