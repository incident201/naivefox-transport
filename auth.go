package transport

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/forwardproxy"
)

// One first 4096-byte cell, minus its cell and AUTH frame headers.
const maxAuthorization = 4064

// provisionForwardProxy composes one real forwardproxy handler. Its configured
// credential list is authoritative for both transports; no global module scan,
// duplicated password configuration or unauthenticated fallback is involved.
func (t *Transport) provisionForwardProxy(ctx caddy.Context) error {
	if t.ForwardProxy == nil || len(t.ForwardProxy.AuthCredentials) == 0 {
		return errors.New("naivefox_transport requires a nested forward_proxy with basic_auth username password; the same credentials serve both transports")
	}
	for _, credential := range t.ForwardProxy.AuthCredentials {
		raw, err := base64.StdEncoding.Strict().DecodeString(string(credential))
		user, password, ok := strings.Cut(string(raw), ":")
		if err != nil || !ok || (user == "" && password == "") || len(credential)+6 > maxAuthorization || base64.StdEncoding.EncodeToString(raw) != string(credential) {
			return errors.New("forward_proxy basic_auth requires a username or password and an encoded authorization fitting the 4064-byte AUTH payload")
		}
	}
	config, err := json.Marshal(t.ForwardProxy)
	if err != nil {
		return err
	}
	loaded, err := ctx.LoadModuleByID("http.handlers.forward_proxy", config)
	if err != nil {
		return err
	}
	t.ForwardProxy = loaded.(*forwardproxy.Handler)
	return nil
}

func (t *Transport) authenticate(authorization []byte) bool {
	if len(authorization) > maxAuthorization || !strings.HasPrefix(string(authorization), "Basic ") {
		return false
	}
	return t.ForwardProxy.Authenticate(string(authorization))
}
