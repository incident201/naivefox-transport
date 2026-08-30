package transport

import (
	"bytes"
	"context"
	"net"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"
	"naivefox.local/transport/internal/cell"
	"naivefox.local/transport/internal/mux"
)

func TestIdleHeartbeatAndEventKeepCellSequence(t *testing.T) {
	for _, profile := range []string{"continuous-bulk-duplex", "continuous-bulk-idle-events"} {
		module := &Transport{Profile: profile, Key: string(bytes.Repeat([]byte{'a'}, 32)), AllowedTargets: []string{"localhost:9"}}
		if err := module.Provision(caddy.Context{}); err != nil {
			t.Fatal(err)
		}
		peer := mux.New(nil)
		s := &session{peer: peer, wake: make(chan struct{}, 1)}
		for index, ready := range []bool{false, true, false, true} {
			w := httptest.NewRecorder()
			before := s.down
			if err := module.finishIdle(w, s, ready); err != nil {
				t.Fatal(err)
			}
			if profile == "continuous-bulk-idle-events" && !ready {
				if w.Code != 204 || w.Body.Len() != 0 || s.down != before || w.Header().Get("X-App-Capacity") != "" {
					t.Fatal("heartbeat sequence/body")
				}
			} else {
				capacity := 512
				if profile == "continuous-bulk-idle-events" {
					capacity = 8192
				}
				sequence, _, _, err := cell.Decode(w.Body.Bytes())
				if err != nil || w.Code != 200 || w.Body.Len() != capacity || sequence != before || s.down != before+1 {
					t.Fatal("event sequence/body")
				}
			}
			if module.stats.IdleCompleted != uint64(index+1) {
				t.Fatal("completion accounting")
			}
		}
		want := uint64(0)
		if profile == "continuous-bulk-idle-events" {
			want = 2
		}
		if module.stats.IdleHeartbeats != want {
			t.Fatal("heartbeat accounting")
		}
		peer.Close()
		module.Cleanup()
	}
}

func TestIdleTimeoutDoesNotDiscardLaterWork(t *testing.T) {
	peer := mux.New(nil)
	defer peer.Close()
	s := &session{peer: peer, wake: make(chan struct{}, 1)}
	if ready, err := waitIdleEvent(context.Background(), s, time.Millisecond); err != nil || ready {
		t.Fatal("timeout outcome")
	}
	app, bridge := net.Pipe()
	defer app.Close()
	if _, err := peer.Open(bridge, "localhost:9"); err != nil {
		t.Fatal(err)
	}
	if ready, err := waitIdleEvent(context.Background(), s, time.Second); err != nil || !ready {
		t.Fatal("late work lost")
	}
}
