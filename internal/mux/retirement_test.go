package mux

import (
	"context"
	"net"
	"reflect"
	"testing"

	"github.com/incident201/naivefox-transport/internal/cell"
)

func TestSequentialResetKeepsSchedulerBoundedWithoutTake(t *testing.T) {
	peer := New(func(ctx context.Context, _ string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	defer peer.Close()
	const connections = 10000
	for id := uint32(1); id <= connections; id++ {
		if err := peer.Receive([]cell.Frame{
			{Kind: cell.Open, Stream: id, Body: []byte("localhost:443")},
			{Kind: cell.Reset, Stream: id},
		}); err != nil {
			t.Fatal(err)
		}
		if len(peer.order) != 0 || len(peer.streams) != 0 || cap(peer.order) > cell.MaxStreams {
			t.Fatalf("retired connection %d retained scheduler state: ids=%d active=%d capacity=%d", id, len(peer.order), len(peer.streams), cap(peer.order))
		}
	}
	if stats := peer.Snapshot(); stats.Opened != connections || stats.PeakStreams != 1 {
		t.Fatalf("sequential accounting: %+v", stats)
	}
}

func TestResetPreservesRoundRobinSuccessor(t *testing.T) {
	for _, test := range []struct {
		removed uint32
		want    []uint32
	}{
		{1, []uint32{2, 3, 4}},
		{2, []uint32{3, 4, 1}},
		{3, []uint32{2, 4, 1}},
		{4, []uint32{2, 3, 1}},
	} {
		peer := New(nil)
		for id := uint32(1); id <= 4; id++ {
			peer.newStream(id).grant = 1
		}
		first := peer.Take(cell.FrameHeader + 4)
		if len(first) != 1 || first[0].Stream != 1 {
			t.Fatal("initial scheduling")
		}
		peer.streams[1].grant = 1
		if err := peer.Receive([]cell.Frame{{Kind: cell.Reset, Stream: test.removed}}); err != nil {
			t.Fatal(err)
		}
		var got []uint32
		for _, frame := range peer.Take(3 * (cell.FrameHeader + 4)) {
			got = append(got, frame.Stream)
		}
		if !reflect.DeepEqual(got, test.want) || len(peer.order) != 3 {
			t.Fatalf("removed %d: got %v want %v", test.removed, got, test.want)
		}
		peer.Close()
	}
}

func TestTakeRetirementKeepsRemainingStreamServiceable(t *testing.T) {
	for _, mode := range []string{"reset", "already-finished", "final-fin"} {
		t.Run(mode, func(t *testing.T) {
			peer := New(nil)
			defer peer.Close()
			first := peer.newStream(1)
			peer.newStream(2).grant = 1
			switch mode {
			case "reset":
				first.reset = true
			case "already-finished":
				first.localFinSent, first.remoteFinWritten = true, true
			case "final-fin":
				first.remoteFinWritten = true
				first.pending = &cell.Frame{Kind: cell.Fin, Stream: 1}
			}
			frames := peer.Take(2*cell.FrameHeader + 4)
			if len(peer.order) != 1 || len(peer.streams) != 1 || peer.order[0] != 2 {
				t.Fatal("retired stream retained in scheduler")
			}
			if len(frames) == 0 || frames[len(frames)-1].Kind != cell.Credit || frames[len(frames)-1].Stream != 2 {
				t.Fatal("retiring first stream skipped a ready successor")
			}
		})
	}
}

func TestChurnDoesNotRelaxActiveStreamLimit(t *testing.T) {
	peer := New(func(ctx context.Context, _ string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	defer peer.Close()
	var highest uint32
	for round := 0; round < 100; round++ {
		var resets []cell.Frame
		for range cell.MaxStreams {
			highest++
			if err := peer.Receive([]cell.Frame{{Kind: cell.Open, Stream: highest, Body: []byte("localhost:443")}}); err != nil {
				t.Fatal(err)
			}
			resets = append(resets, cell.Frame{Kind: cell.Reset, Stream: highest})
		}
		if len(peer.order) != cell.MaxStreams || cap(peer.order) > cell.MaxStreams {
			t.Fatal("active scheduler bound")
		}
		if err := peer.Receive([]cell.Frame{{Kind: cell.Open, Stream: highest + 1, Body: []byte("localhost:443")}}); err == nil {
			t.Fatal("active stream limit bypassed")
		}
		if err := peer.Receive(resets); err != nil {
			t.Fatal(err)
		}
		if len(peer.order) != 0 || peer.cursor != 0 {
			t.Fatal("empty scheduler retained state")
		}
	}
}
