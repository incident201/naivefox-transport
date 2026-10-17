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

type packetBatchCell struct {
	capacity int
	frames   []cell.Frame
	at       time.Time
}

func packetBatchWriter(t *testing.T, s *session, interval time.Duration) <-chan packetBatchCell {
	t.Helper()
	previous := packetBatchInterval
	packetBatchInterval = interval
	module := &Transport{stats: counters{WSCellCapacities: make(map[string]uint64), CellCapacities: make(map[string]uint64)}}
	ctx, cancel := context.WithCancel(context.Background())
	cells := make(chan packetBatchCell, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		module.writeCells(ctx, s, time.Hour, func(body []byte) error {
			_, frames, _, err := cell.Decode(body)
			if err != nil {
				return err
			}
			cells <- packetBatchCell{len(body), frames, time.Now()}
			return nil
		})
	}()
	t.Cleanup(func() { cancel(); <-done; packetBatchInterval = previous })
	// The packet downlink starts with one immediate idle cell.
	select {
	case first := <-cells:
		if len(first.frames) != 0 || first.capacity != 512 {
			t.Fatal("initial packet heartbeat")
		}
	case <-time.After(time.Second):
		t.Fatal("missing initial packet heartbeat")
	}
	return cells
}

func TestPacketBatchDrainsFullCellsWithoutPerCellWindows(t *testing.T) {
	const size = 50000
	const interval = 100 * time.Millisecond
	peer := mux.NewHTTP3(nil)
	app, target := net.Pipe()
	if _, err := peer.Open(target, "localhost:443"); err != nil {
		t.Fatal(err)
	}
	peer.Take(4096)
	t.Cleanup(func() { app.Close(); peer.Close() })
	s := &session{peer: peer, wake: make(chan struct{}, 1), packet: true, realtime: true}
	cells := packetBatchWriter(t, s, interval)
	start := time.Now()
	go app.Write(bytes.Repeat([]byte{1}, size))
	received := 0
	for received < size {
		select {
		case next := <-cells:
			if next.capacity != 8192 {
				t.Fatalf("partial backlog changed cell capacity: %d", next.capacity)
			}
			for _, frame := range next.frames {
				if frame.Kind == cell.Data {
					received += len(frame.Body)
				}
			}
			if received >= size {
				elapsed := next.at.Sub(start)
				// Six full cells follow the first window and the partial tail
				// waits one more; each cell previously restarted the window.
				if elapsed < 2*interval || elapsed >= 4*interval {
					t.Fatalf("backlog drained after %v, want two %v windows", elapsed, interval)
				}
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("backlog stalled after %d bytes", received)
		}
	}
}

func TestPacketOpenedBypassesBatch(t *testing.T) {
	local, remote := net.Pipe()
	t.Cleanup(func() { local.Close(); remote.Close() })
	peer := mux.NewHTTP3(func(context.Context, string) (net.Conn, error) { return local, nil })
	t.Cleanup(peer.Close)
	s := &session{peer: peer, wake: make(chan struct{}, 1), packet: true, realtime: true}
	cells := packetBatchWriter(t, s, time.Hour)
	if err := peer.Receive([]cell.Frame{{Kind: cell.Open, Stream: 1, Body: []byte("localhost:443")}}); err != nil {
		t.Fatal(err)
	}
	select {
	case next := <-cells:
		if len(next.frames) != 1 || next.frames[0].Kind != cell.Opened || next.frames[0].Stream != 1 {
			t.Fatal("OPENED cell")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OPENED waited for the packet batch window")
	}
}

func TestPacketAcknowledgementStillBatches(t *testing.T) {
	peer := mux.NewHTTP3(nil)
	t.Cleanup(peer.Close)
	s := &session{peer: peer, wake: make(chan struct{}, 1), packet: true, realtime: true}
	cells := packetBatchWriter(t, s, time.Hour)
	s.mu.Lock()
	s.ackPending, s.ackSequence = true, 1
	s.mu.Unlock()
	s.wake <- struct{}{}
	select {
	case <-cells:
		t.Fatal("ACK-only cell bypassed the packet batch window")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestPacketCreditLimitedBacklogKeepsPerCellBatching(t *testing.T) {
	const interval = 200 * time.Millisecond
	peer := mux.NewHTTP3(nil)
	app, target := net.Pipe()
	id, err := peer.Open(target, "localhost:443")
	if err != nil {
		t.Fatal(err)
	}
	peer.Take(4096)
	t.Cleanup(func() { app.Close(); peer.Close() })
	s := &session{peer: peer, wake: make(chan struct{}, 1), packet: true, realtime: true}
	cells := packetBatchWriter(t, s, interval)
	go app.Write(bytes.Repeat([]byte{1}, cell.HTTP3Window+100000))
	received := 0
	collect := func(limit int) time.Time {
		for {
			select {
			case next := <-cells:
				for _, frame := range next.frames {
					if frame.Kind == cell.Data {
						received += len(frame.Body)
					}
				}
				if received >= limit {
					return next.at
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("credit-limited transfer stalled after %d bytes", received)
			}
		}
	}
	// The remaining credit falls below the floor, which blocks the stream.
	collect(cell.HTTP3Window - realtimeCreditFloor)
	granted, before := time.Now(), received
	if err := peer.Receive([]cell.Frame{{Kind: cell.Credit, Stream: id, Body: cell.Uint32(20000)}}); err != nil {
		t.Fatal(err)
	}
	// A small grant still coalesces: both resulting full cells wait.
	if elapsed := collect(before + 16000).Sub(granted); elapsed < 3*interval/2 {
		t.Fatalf("credit-limited cells were sent after %v without per-cell batching", elapsed)
	}
}
