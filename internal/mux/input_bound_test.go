package mux

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/incident201/naivefox-transport/internal/cell"
)

type observedHalfClose struct {
	net.Conn
	closed chan struct{}
	once   sync.Once
}

func (c *observedHalfClose) CloseWrite() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func TestTinyFramesRespectByteWindowAndOrderedHalfClose(t *testing.T) {
	const window = 2 * cell.Window
	peer, err := NewWithWindow(nil, window)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	app, pipe := net.Pipe()
	defer app.Close()
	conn := &observedHalfClose{Conn: pipe, closed: make(chan struct{})}
	id, err := peer.Open(conn, "localhost:443")
	if err != nil {
		t.Fatal(err)
	}
	peer.Take(1024)
	var sequence uint32
	for sequence < 4096 {
		if err := peer.Receive([]cell.Frame{{Kind: cell.Data, Stream: id, Sequence: sequence, Body: []byte{7}}}); err != nil {
			t.Fatalf("valid tiny frame %d rejected before byte window exhaustion: %v", sequence, err)
		}
		sequence++
	}
	for sequence < window {
		length := min(16384, window-int(sequence))
		if err := peer.Receive([]cell.Frame{{Kind: cell.Data, Stream: id, Sequence: sequence, Body: bytes.Repeat([]byte{7}, length)}}); err != nil {
			t.Fatal(err)
		}
		sequence += uint32(length)
	}
	if err := peer.Receive([]cell.Frame{{Kind: cell.Data, Stream: id, Sequence: sequence, Body: []byte{7}}}); err == nil {
		t.Fatal("byte window exceeded")
	}
	if err := peer.Receive([]cell.Frame{{Kind: cell.Fin, Stream: id, Sequence: sequence}}); err != nil {
		t.Fatal(err)
	}
	peer.mu.Lock()
	s := peer.streams[id]
	bounded := len(s.input) <= window/streamChunk+1 && cap(s.input) == window/streamChunk+1 && s.budget == 0 && s.grant == 0
	for _, frame := range s.input {
		bounded = bounded && len(frame.Body) <= streamChunk
	}
	peer.mu.Unlock()
	if !bounded {
		t.Fatal("coalesced input exceeded its byte/chunk bounds")
	}
	if peer.Snapshot().Delivered != 0 {
		t.Fatal("blocked socket granted delivery credit")
	}
	select {
	case <-conn.closed:
		t.Fatal("FIN overtook queued data")
	default:
	}
	if err := app.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	received := make([]byte, window)
	if _, err := io.ReadFull(app, received); err != nil || !bytes.Equal(received, bytes.Repeat([]byte{7}, window)) {
		t.Fatalf("coalesced payload: %v", err)
	}
	select {
	case <-conn.closed:
	case <-time.After(time.Second):
		t.Fatal("FIN not delivered after queued bytes")
	}
	frames := peer.Take(cell.FrameHeader + 4)
	if len(frames) != 1 || frames[0].Kind != cell.Credit || binary.BigEndian.Uint32(frames[0].Body) != window || peer.Snapshot().Delivered != window {
		t.Fatal("credit did not track actual socket writes")
	}
}

func TestCancellationReleasesBlockedCoalescedWriter(t *testing.T) {
	peer := New(nil)
	app, pipe := net.Pipe()
	defer app.Close()
	id, err := peer.Open(pipe, "localhost:443")
	if err != nil {
		t.Fatal(err)
	}
	for sequence := uint32(0); sequence < 4096; sequence++ {
		if err := peer.Receive([]cell.Frame{{Kind: cell.Data, Stream: id, Sequence: sequence, Body: []byte{1}}}); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan struct{})
	go func() { peer.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not release blocked writer")
	}
}
