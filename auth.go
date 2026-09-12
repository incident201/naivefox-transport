package transport

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"strings"
)

// One first 4096-byte cell, minus its cell and AUTH frame headers.
const maxAuthorization = 4064

type Credential struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (t *Transport) provisionAccess() error {
	if len(t.Access.Credentials) == 0 {
		return errors.New("naivefox_transport requires basic_auth")
	}
	t.authHashes = nil
	for _, credential := range t.Access.Credentials {
		if (credential.Username == "" && credential.Password == "") || strings.Contains(credential.Username, ":") {
			return errors.New("invalid basic_auth credentials")
		}
		encoded := base64.StdEncoding.EncodeToString([]byte(credential.Username + ":" + credential.Password))
		if len(encoded)+6 > maxAuthorization {
			return errors.New("authorization exceeds AUTH capacity")
		}
		t.authHashes = append(t.authHashes, sha256.Sum256([]byte(encoded)))
	}
	var err error
	t.policy, err = newTCPPolicy(&t.Access)
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
