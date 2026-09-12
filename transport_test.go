package transport

import (
	"github.com/incident201/naivefox-transport/internal/cell"
	"testing"
)

func TestStartupCapacityAndHTTPRetirement(t *testing.T) {
	f := newRealtimeFixture(t)
	f.bootstrap(true, nil)
	if f.up != 20 || f.down != 20 {
		t.Fatal("startup sequence")
	}
	for _, path := range []string{"/api/events/brief", "/media/chunk/20"} {
		if status, _ := f.request("GET", path, nil); status != 400 {
			t.Fatal("HTTP carrier continued after startup")
		}
	}
	if conn, _, err := f.dial(); err != nil {
		t.Fatal(err)
	} else {
		conn.Close()
	}
}

func TestStartupRejectsReplayAndMalformedEnvelope(t *testing.T) {
	for _, invalid := range []string{"replay", "capacity", "reserved"} {
		t.Run(invalid, func(t *testing.T) {
			f := newRealtimeFixture(t)
			body, err := cell.Encode(0, 4096, []cell.Frame{{Kind: cell.Auth, Body: []byte(testAuthorization)}})
			if err != nil {
				t.Fatal(err)
			}
			if invalid == "replay" {
				if status, _ := f.request("POST", "/api/sync", body); status != 204 {
					t.Fatal("valid startup upload")
				}
			} else if invalid == "capacity" {
				body = body[:512]
			} else {
				body[14] = 1
			}
			expected := 404
			if invalid == "replay" {
				expected = 400
			}
			if status, _ := f.request("POST", "/api/sync", body); status != expected {
				t.Fatal("invalid upload accepted")
			}
			if f.module.stats.Opens != 0 {
				t.Fatal("invalid upload opened a target")
			}
		})
	}
}
