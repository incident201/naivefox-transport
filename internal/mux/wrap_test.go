package mux

import (
	"bytes"
	"net"
	"testing"
	"time"

	"github.com/incident201/naivefox-transport/internal/cell"
)

func TestByteOffsetsWrapWithSequenceAndCreditChecks(t *testing.T) {
	peer := New(nil)
	defer peer.Close()
	s := peer.newStream(1)
	s.nextIn = ^uint32(0) - 3
	if err := peer.Receive([]cell.Frame{{Kind: cell.Data, Stream: 1, Sequence: s.nextIn, Body: []byte("abcdefgh")}}); err != nil {
		t.Fatal(err)
	}
	if s.nextIn != 4 || s.budget != cell.Window-8 {
		t.Fatal("wrapped sequence or credit")
	}
	if err := peer.Receive([]cell.Frame{{Kind: cell.Data, Stream: 1, Sequence: 3, Body: []byte("x")}}); err == nil {
		t.Fatal("bad offset accepted after wrap")
	}
	if err := peer.Receive([]cell.Frame{{Kind: cell.Fin, Stream: 1, Sequence: 4}}); err != nil {
		t.Fatal(err)
	}
}

func TestReadAndPartialFramesCrossByteOffsetWrap(t *testing.T) {
	peer := New(nil)
	defer peer.Close()
	s := peer.newStream(1)
	s.nextOut = ^uint32(0) - 3
	app, transport := net.Pipe()
	defer app.Close()
	peer.attach(s, transport)
	go func() { app.Write([]byte("abcdefgh")); app.Close() }()
	deadline := time.After(time.Second)
	var data []byte
	offset := ^uint32(0) - 3
	for {
		for _, frame := range peer.Take(cell.FrameHeader + 3) {
			switch frame.Kind {
			case cell.Data:
				if frame.Sequence != offset {
					t.Fatal("partial frame sequence across wrap")
				}
				data = append(data, frame.Body...)
				offset += uint32(len(frame.Body))
			case cell.Fin:
				if frame.Sequence != 4 || !bytes.Equal(data, []byte("abcdefgh")) {
					t.Fatal("FIN or bytes after wrap")
				}
				return
			case cell.Reset:
				t.Fatal("stream reset at byte wrap")
			}
		}
		if pressure := peer.Pressure(); pressure.Bytes > 0 || pressure.Controls > 0 {
			continue
		}
		select {
		case <-peer.Changes():
		case <-deadline:
			t.Fatal("no frame after byte wrap")
		}
	}
}
