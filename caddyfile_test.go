package transport

import (
	"testing"

	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
)

func TestCaddyfileConfiguration(t *testing.T) {
	input := `naivefox_transport {
        application_root /absolute/application
            basic_auth fixture fixture
            basic_auth second p:a:ss
        stats_path /tmp/transport-stats.json
    }`
	var handler Transport
	if err := handler.UnmarshalCaddyfile(caddyfile.NewTestDispenser(input)); err != nil {
		t.Fatal(err)
	}
	if handler.ApplicationRoot != "/absolute/application" || len(handler.Access.Credentials) != 2 ||
		handler.Access.Credentials[1] != (Credential{Username: "second", Password: "p:a:ss"}) {
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
