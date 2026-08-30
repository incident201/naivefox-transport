package mux

import (
	"bytes"
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"naivefox.local/transport/internal/cell"
)

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
