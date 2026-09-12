package mux

import (
	"bytes"
	"testing"

	"github.com/incident201/naivefox-transport/internal/cell"
)

func TestCreditFloorDoesNotEmitTinyWindowButAllowsTailAndFin(t *testing.T) {
	peer := New(nil)
	defer peer.Close()
	s := peer.newStream(1)
	s.credit = 1024
	s.pending = &cell.Frame{Kind: cell.Data, Stream: 1, Body: bytes.Repeat([]byte{9}, 16384)}
	s.queuedBytes.Store(16384)
	const floor = 8192 - cell.Header - cell.FrameHeader
	if peer.Pressure().Bytes != 1024 || peer.PressureWithCreditFloor(floor).Bytes != 0 {
		t.Fatal("small exhausted window was not withheld")
	}
	s.grant = 64
	frames := peer.TakeControls(512 - cell.Header)
	if len(frames) != 1 || frames[0].Kind != cell.Credit || s.credit != 1024 || len(s.pending.Body) != 16384 {
		t.Fatal("control response consumed withheld data")
	}
	if err := peer.Receive([]cell.Frame{{Kind: cell.Credit, Stream: 1, Body: cell.Uint32(8192)}}); err != nil {
		t.Fatal(err)
	}
	if peer.PressureWithCreditFloor(floor).Bytes != 9216 {
		t.Fatal("new credit did not wake data")
	}
	frames = peer.Take(8192 - cell.Header)
	if len(frames) != 1 || len(frames[0].Body) != 8160 || s.credit != 1056 {
		t.Fatal("small-cell credit consumption differs")
	}
	if peer.PressureWithCreditFloor(floor).Bytes != 0 {
		t.Fatal("tiny remainder was emitted")
	}
	if err := peer.Receive([]cell.Frame{{Kind: cell.Credit, Stream: 1, Body: cell.Uint32(7168)}}); err != nil {
		t.Fatal(err)
	}
	peer.Take(8192)
	if pressure := peer.PressureWithCreditFloor(floor); pressure.Bytes != 48 || pressure.Queued != 48 {
		t.Fatalf("final short tail was withheld: %+v", pressure)
	}
	frames = peer.Take(512 - cell.Header)
	if len(frames) != 1 || len(frames[0].Body) != 48 || s.credit != 0 {
		t.Fatal("tail was not drained")
	}
	s.output <- cell.Frame{Kind: cell.Fin, Stream: 1, Sequence: 16384}
	frames = peer.TakeControls(512 - cell.Header)
	if len(frames) != 1 || frames[0].Kind != cell.Fin {
		t.Fatal("FIN depended on credit")
	}
}

func TestFramingEstimateIncludesPartialDataAndPendingControls(t *testing.T) {
	peer := New(nil)
	defer peer.Close()
	s := peer.newStream(1)
	s.credit = 261840
	s.queuedBytes.Store(282924)
	s.grant = 64
	s.ack = true
	pressure := peer.PressureWithCreditFloor(8160)
	if pressure.Bytes != 261840 || pressure.FrameOverhead != 17*cell.FrameHeader+cell.FrameHeader+cell.FrameHeader+4 {
		t.Fatalf("framing estimate lost control or partial frame: %+v", pressure)
	}
	s.reset = true
	if peer.PressureWithCreditFloor(8160).Bytes != 0 {
		t.Fatal("reset data remained sendable")
	}
}
