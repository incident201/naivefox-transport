package transport

import (
	"context"
	"errors"
	"net"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/incident201/naivefox-transport/internal/cell"
)

func TestHTTP3UploadReorderingAndBound(t *testing.T) {
	f := newRealtimeFixture(t)
	f.bootstrap(true, nil)
	s := f.module.sessions[f.cookie.Value]
	s.h3 = true
	s.pendingUploads = make(map[uint32]*pendingUpload)
	body := func(sequence uint32) []byte {
		b, err := cell.Encode(sequence, 512, nil)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	first := s.up
	done := make(chan error, maxPendingUploads-1)
	for n := uint32(1); n < maxPendingUploads; n++ {
		b := body(first + n)
		go func() { done <- f.module.orderedUpload(context.Background(), s, b) }()
	}
	deadline := time.Now().Add(time.Second)
	for {
		s.uploadMu.Lock()
		count := len(s.pendingUploads)
		s.uploadMu.Unlock()
		if count == maxPendingUploads-1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("uploads were not queued")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case err := <-done:
		t.Fatalf("out-of-order upload acknowledged: %v", err)
	default:
	}
	if err := f.module.orderedUpload(context.Background(), s, body(first+maxPendingUploads)); err == nil {
		t.Fatal("pipeline bound ignored")
	}
	if err := f.module.orderedUpload(context.Background(), s, body(first+1)); err == nil {
		t.Fatal("duplicate accepted")
	}
	if err := f.module.orderedUpload(context.Background(), s, body(first)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxPendingUploads-1; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("ordered upload stalled")
		}
	}
	if s.up != first+maxPendingUploads || len(s.pendingUploads) != 0 {
		t.Fatal("sequence or retention mismatch")
	}
	if err := f.module.orderedUpload(context.Background(), s, body(first)); err == nil {
		t.Fatal("replay accepted")
	}
}

func TestHTTP3CanceledGapReleasesBody(t *testing.T) {
	f := newRealtimeFixture(t)
	f.bootstrap(true, nil)
	s := f.module.sessions[f.cookie.Value]
	s.h3 = true
	s.pendingUploads = make(map[uint32]*pendingUpload)
	body, err := cell.Encode(s.up+1, 131072, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := f.module.orderedUpload(ctx, s, body); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(s.pendingUploads) != 0 {
		t.Fatal("canceled upload retained")
	}
}

func TestHTTP3CanceledHeadDoesNotAdvance(t *testing.T) {
	f := newRealtimeFixture(t)
	f.bootstrap(true, nil)
	s := f.module.sessions[f.cookie.Value]
	s.h3 = true
	s.pendingUploads = make(map[uint32]*pendingUpload)
	sequence := s.up
	body, err := cell.Encode(sequence, 512, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := f.module.orderedUpload(ctx, s, body); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if s.up != sequence || len(s.pendingUploads) != 0 {
		t.Fatal("canceled head was applied")
	}
}

func TestHTTP3CanceledQueuedUploadIsNotApplied(t *testing.T) {
	f := newRealtimeFixture(t)
	f.bootstrap(true, nil)
	s := f.module.sessions[f.cookie.Value]
	s.h3 = true
	s.pendingUploads = make(map[uint32]*pendingUpload)
	sequence := s.up
	later, err := cell.Encode(sequence+1, 512, nil)
	if err != nil {
		t.Fatal(err)
	}
	head, err := cell.Encode(sequence, 512, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pending := &pendingUpload{ctx: ctx, body: later, done: make(chan struct{})}
	s.pendingUploads[sequence+1] = pending
	_ = f.module.orderedUpload(context.Background(), s, head)
	if s.up != sequence+1 || len(s.pendingUploads) != 0 || pending.body != nil {
		t.Fatal("canceled queued body was applied or retained")
	}
	select {
	case <-pending.done:
		if !errors.Is(pending.err, context.Canceled) {
			t.Fatal(pending.err)
		}
	default:
		t.Fatal("canceled waiter not released")
	}
}

func TestHTTP3IdleStreamStartsWithoutHeartbeatDelay(t *testing.T) {
	f := newRealtimeFixture(t)
	f.bootstrap(true, nil)
	s := f.module.sessions[f.cookie.Value]
	s.h3 = true
	first := s.down
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var bodies [][]byte
	f.module.writeCells(ctx, s, 25*time.Second, func(body []byte) error {
		bodies = append(bodies, body)
		cancel()
		return nil
	})
	if len(bodies) != 1 {
		t.Fatal("idle HTTP/3 stream did not publish an initial body")
	}
	sequence, frames, _, err := cell.Decode(bodies[0])
	if err != nil || sequence != first || len(frames) != 0 || len(bodies[0]) != 512 {
		t.Fatal("invalid initial stream cell", err)
	}
}

func TestAuthenticatedSessionCannotChangeCarrierProtocol(t *testing.T) {
	f := newRealtimeFixture(t)
	f.bootstrap(true, nil)
	s := f.module.sessions[f.cookie.Value]
	request := httptest.NewRequest("GET", "https://proxy.test/api/stream", nil)
	request.RemoteAddr = net.JoinHostPort(s.ip, "12345")
	request.AddCookie(f.cookie)
	request.ProtoMajor = 3
	if _, err := f.module.getSession(httptest.NewRecorder(), request); err == nil {
		t.Fatal("H2 session changed to H3")
	}
	s.h3 = true
	request.ProtoMajor = 2
	if _, err := f.module.getSession(httptest.NewRecorder(), request); err == nil {
		t.Fatal("H3 session changed to H2")
	}
	request.ProtoMajor = 3
	if got, err := f.module.getSession(httptest.NewRecorder(), request); err != nil || got != s {
		t.Fatal("matching H3 session rejected", err)
	}
}
