package transport

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"

	"github.com/incident201/naivefox-transport/internal/cell"
	"github.com/incident201/naivefox-transport/internal/mux"
)

func coalesceSession(t *testing.T) (*session, net.Conn) {
	t.Helper()
	peer := mux.New(nil)
	app, target := net.Pipe()
	if _, err := peer.Open(target, "localhost:443"); err != nil {
		t.Fatal(err)
	}
	peer.Take(4096)
	t.Cleanup(func() { app.Close(); peer.Close() })
	return &session{peer: peer, wake: make(chan struct{}, 1)}, app
}

func TestCoalesceWakesWhenDataFillsCell(t *testing.T) {
	s, app := coalesceSession(t)
	if _, err := app.Write(bytes.Repeat([]byte{1}, cell.MaxCell/2)); err != nil {
		t.Fatal(err)
	}
	timeout := time.After(time.Second)
	for s.peer.Pressure().Bytes < cell.MaxCell/2 {
		select {
		case <-s.peer.Changes():
		case <-timeout:
			t.Fatal("initial data was not queued")
		}
	}
	done := make(chan bool, 1)
	go func() { done <- waitRealtimeCoalesce(context.Background(), s, time.Second) }()
	select {
	case <-done:
		t.Fatal("partial cell did not coalesce")
	case <-time.After(10 * time.Millisecond):
	}
	if _, err := app.Write(bytes.Repeat([]byte{2}, cell.MaxCell/2-cell.Header-cell.FrameHeader)); err != nil {
		t.Fatal(err)
	}
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("full cell cancelled")
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("full cell waited for original timer")
	}
}

func TestCoalescePreservesSmallTierAggregation(t *testing.T) {
	s, app := coalesceSession(t)
	if _, err := app.Write(bytes.Repeat([]byte{1}, 8192)); err != nil {
		t.Fatal(err)
	}
	timeout := time.After(time.Second)
	for s.peer.Pressure().Bytes < 8192 {
		select {
		case <-s.peer.Changes():
		case <-timeout:
			t.Fatal("initial data was not queued")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan bool, 1)
	go func() { done <- waitRealtimeCoalesce(ctx, s, time.Second) }()
	select {
	case <-done:
		t.Fatal("small tier interrupted aggregation")
	case <-time.After(10 * time.Millisecond):
	}
	cancel()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("cancelled wait succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation stalled")
	}
}

func TestCoalesceWakeDoesNotRestartDeadline(t *testing.T) {
	s, app := coalesceSession(t)
	if _, err := app.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	timeout := time.After(time.Second)
	for s.peer.Pressure().Bytes == 0 {
		select {
		case <-s.peer.Changes():
		case <-timeout:
			t.Fatal("initial data was not queued")
		}
	}
	done := make(chan bool, 1)
	go func() { done <- waitRealtimeCoalesce(context.Background(), s, 20*time.Millisecond) }()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	deadline := time.After(250 * time.Millisecond)
	for {
		select {
		case ok := <-done:
			if !ok {
				t.Fatal("partial cell cancelled")
			}
			return
		case <-tick.C:
			select {
			case s.wake <- struct{}{}:
			default:
			}
		case <-deadline:
			t.Fatal("wake notifications extended the coalescing deadline")
		}
	}
}

func TestCoalesceCancellation(t *testing.T) {
	s, app := coalesceSession(t)
	if _, err := app.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	timeout := time.After(time.Second)
	for s.peer.Pressure().Bytes == 0 {
		select {
		case <-s.peer.Changes():
		case <-timeout:
			t.Fatal("initial data was not queued")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if waitRealtimeCoalesce(ctx, s, time.Hour) {
		t.Fatal("cancelled wait succeeded")
	}
}
