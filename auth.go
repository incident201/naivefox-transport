package transport

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/forwardproxy"
)

// One first 4096-byte cell, minus its cell and AUTH frame headers.
const maxAuthorization = 4064

// The ordinary module registers upstream dialers globally while provisioning.
// Serialize our nested instances without altering or replacing that module.
var forwardProxyProvisionMu sync.Mutex

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
	forwardProxyProvisionMu.Lock()
	loaded, err := ctx.LoadModuleByID("http.handlers.forward_proxy", config)
	forwardProxyProvisionMu.Unlock()
	if err != nil {
		return err
	}
	t.ForwardProxy = loaded.(*forwardproxy.Handler)
	t.authHashes = make([][32]byte, 0, len(t.ForwardProxy.AuthCredentials))
	for _, credential := range t.ForwardProxy.AuthCredentials {
		t.authHashes = append(t.authHashes, sha256.Sum256(credential))
	}
	t.policy, err = newTCPPolicy(t.ForwardProxy)
	return err
}

func (t *Transport) authenticate(authorization []byte) bool {
	if len(authorization) > maxAuthorization {
		return false
	}
	parts := strings.Split(string(authorization), " ")
	if len(parts) != 2 || strings.ToLower(parts[0]) != "basic" {
		return false
	}
	candidate := sha256.Sum256([]byte(parts[1]))
	matched := 0
	for _, expected := range t.authHashes {
		matched |= subtle.ConstantTimeCompare(candidate[:], expected[:])
	}
	return matched == 1
}
