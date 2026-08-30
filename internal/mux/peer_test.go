package mux

import (
	"bytes"
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/incident201/naivefox-transport/internal/cell"
)

func TestPressureNotificationsAndPartialTake(t *testing.T) {
	peer := New(nil)
	defer peer.Close()
	app, transport := net.Pipe()
	defer app.Close()
	if _, err := peer.Open(transport, "localhost:443"); err != nil {
		t.Fatal(err)
	}
	if peer.Pressure().Controls != 1 || peer.Pressure().Streams != 1 {
		t.Fatal("open pressure")
	}
	peer.Take(4080)
	select {
	case <-peer.Changes():
	default:
		t.Fatal("missing open notification")
	}
	go app.Write(bytes.Repeat([]byte{7}, 8192))
	deadline := time.After(time.Second)
	for peer.Pressure().Bytes != 8192 {
		select {
		case <-peer.Changes():
		case <-deadline:
			t.Fatal("missing data pressure")
		}
	}
	frames := peer.Take(cell.FrameHeader + 1000)
	if len(frames) != 1 || frames[0].Kind != cell.Data || len(frames[0].Body) != 1000 || peer.Pressure().Bytes != 7192 {
		t.Fatal("partial pressure accounting")
	}
	peer.Take(10000)
	if peer.Pressure().Bytes != 0 {
		t.Fatal("drained pressure")
	}
	for range 1000 {
		peer.notify()
	}
	if len(peer.changes) != 1 {
		t.Fatal("notifications not coalesced")
	}
}

func TestQueuedDataDoesNotBypassCredit(t *testing.T) {
	peer := New(nil)
	defer peer.Close()
	s := peer.newStream(1)
	s.credit = 0
	s.pending = &cell.Frame{Kind: cell.Data, Stream: 1, Body: bytes.Repeat([]byte{7}, 65536)}
	s.queuedBytes.Store(65536)
	if pressure := peer.Pressure(); pressure.Bytes != 0 || pressure.Queued != 65536 || pressure.Controls != 0 {
		t.Fatalf("blocked pressure %+v", pressure)
	}
	if frames := peer.Take(262128); len(frames) != 0 {
		t.Fatal("backlog bypassed credit")
	}
	if err := peer.Receive([]cell.Frame{{Kind: cell.Credit, Stream: 1, Body: cell.Uint32(32768)}}); err != nil {
		t.Fatal(err)
	}
	if pressure := peer.Pressure(); pressure.Bytes != 32768 || pressure.Queued != 65536 {
		t.Fatalf("credit pressure %+v", pressure)
	}
	frames := peer.Take(262128)
	if len(frames) != 1 || len(frames[0].Body) != 32768 || peer.Pressure().Queued != 32768 || peer.Pressure().Bytes != 0 {
		t.Fatal("credit consumption")
	}
}

func TestReadablePressureExcludesFinishedAndResetStreams(t *testing.T) {
	peer := New(nil)
	defer peer.Close()
	s := peer.newStream(1)
	if peer.Pressure().Readable != 1 {
		t.Fatal("readable")
	}
	s.localFinSent = true
	if peer.Pressure().Readable != 0 {
		t.Fatal("EOF")
	}
	s.localFinSent = false
	s.reset = true
	if peer.Pressure().Readable != 0 {
		t.Fatal("reset")
	}
}

func TestConfiguredWindowBoundsWithoutWriterProgress(t *testing.T) {
	for _, window := range []uint32{0, cell.Window, 2 * cell.Window} {
		peer, err := NewWithWindow(nil, window)
		if err != nil {
			t.Fatal(err)
		}
		want := window
		if want == 0 {
			want = cell.Window
		}
		s := peer.newStream(1) // No attached writer: no delivery or credit grant.
		if s.credit != want || s.budget != want || peer.Snapshot().ReceiveWindow != want || cap(s.output) != 16 || cap(s.input) != int(want)/streamChunk+1 || cap(s.inputReady) != 1 {
			t.Fatal("window or queue bound")
		}
		for offset := uint32(0); offset < want; offset += 16384 {
			if err := peer.Receive([]cell.Frame{{Kind: cell.Data, Stream: 1, Sequence: offset, Body: make([]byte, 16384)}}); err != nil {
				t.Fatal(err)
			}
		}
		if s.budget != 0 || s.grant != 0 {
			t.Fatal("credit without delivery")
		}
		if err := peer.Receive([]cell.Frame{{Kind: cell.Data, Stream: 1, Sequence: want, Body: []byte{1}}}); err == nil {
			t.Fatal("receive bound bypass")
		}
		if err := peer.Receive([]cell.Frame{{Kind: cell.Credit, Stream: 1, Body: cell.Uint32(1)}}); err == nil {
			t.Fatal("full window overflow")
		}
		s.credit = 0
		if err := peer.Receive([]cell.Frame{{Kind: cell.Credit, Stream: 1, Body: cell.Uint32(want)}}); err != nil {
			t.Fatal(err)
		}
		if err := peer.Receive([]cell.Frame{{Kind: cell.Credit, Stream: 1, Body: cell.Uint32(1)}}); err == nil {
			t.Fatal("restored window overflow")
		}
		peer.Close()
	}
	for _, window := range []uint32{1, cell.Window - 1, cell.Window + 1, 2*cell.Window + 1, ^uint32(0)} {
		if peer, err := NewWithWindow(nil, window); err == nil || peer != nil {
			t.Fatal("unsupported window")
		}
	}
}

func TestMultiplexCreditAndHalfClose(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				body, err := io.ReadAll(io.LimitReader(conn, 2*cell.Window))
				if err == nil {
					conn.Write(body)
				}
			}()
		}
	}()
	server := New(func(ctx context.Context, target string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", target)
	})
	client := New(nil)
	defer server.Close()
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errors := make(chan error, 1)
	go func() {
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			if err := server.Receive(client.Take(4080)); err != nil {
				errors <- err
				return
			}
			if err := client.Receive(server.Take(24560)); err != nil {
				errors <- err
				return
			}
		}
	}()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			local, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Error(err)
				return
			}
			defer local.Close()
			app, err := net.Dial("tcp", local.Addr().String())
			if err != nil {
				t.Error(err)
				return
			}
			defer app.Close()
			app.SetDeadline(time.Now().Add(8 * time.Second))
			bridge, err := local.Accept()
			if err != nil {
				t.Error(err)
				return
			}
			if _, err := client.Open(bridge, listener.Addr().String()); err != nil {
				bridge.Close()
				t.Error(err)
				return
			}
			body := bytes.Repeat([]byte{byte(index + 1)}, 300000)
			if _, err := app.Write(body); err != nil {
				t.Error(err)
				return
			}
			app.(*net.TCPConn).CloseWrite()
			got, err := io.ReadAll(app)
			if err != nil || !bytes.Equal(got, body) {
				t.Errorf("stream %d bytes=%d err=%v", index, len(got), err)
			}
		}(i)
	}
	wg.Wait()
	select {
	case err := <-errors:
		t.Fatal(err)
	default:
	}
	if client.Snapshot().PeakStreams < 2 || client.Snapshot().Delivered != 1200000 {
		t.Fatalf("stats %+v", client.Snapshot())
	}
}

func TestInvalidSequenceCreditAndCancel(t *testing.T) {
	for _, frame := range []cell.Frame{
		{Kind: cell.Data, Stream: 1, Sequence: 1, Body: []byte{1}},
		{Kind: cell.Credit, Stream: 1, Body: cell.Uint32(1)},
		{Kind: cell.Fin, Stream: 1, Sequence: 1},
		{Kind: cell.Reset, Stream: 1, Body: []byte{1}},
	} {
		peer := New(nil)
		app, bridge := net.Pipe()
		if _, err := peer.Open(bridge, "localhost:443"); err != nil {
			t.Fatal(err)
		}
		if err := peer.Receive([]cell.Frame{frame}); err == nil {
			t.Fatalf("accepted %d", frame.Kind)
		}
		app.Close()
		peer.Close()
	}
}
