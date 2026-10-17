package mux

import (
	"net"
	"testing"

	"github.com/incident201/naivefox-transport/internal/cell"
)

func TestPacketPeerSendsWithLargerWindowThanItBuffers(t *testing.T) {
	peer := NewPacket(nil)
	defer peer.Close()
	app, target := net.Pipe()
	defer app.Close()
	id, err := peer.Open(target, "localhost:443")
	if err != nil {
		t.Fatal(err)
	}
	peer.mu.Lock()
	credit, budget := peer.streams[id].credit, peer.streams[id].budget
	peer.mu.Unlock()
	if credit != cell.PacketWindow || budget != cell.HTTP3Window {
		t.Fatalf("packet windows send=%d receive=%d", credit, budget)
	}
	if err := peer.Receive([]cell.Frame{{Kind: cell.Opened, Stream: id}}); err != nil {
		t.Fatal(err)
	}
	if err := peer.Receive([]cell.Frame{{Kind: cell.Data, Stream: id, Body: make([]byte, cell.HTTP3Window+1)}}); err == nil {
		t.Fatal("receive budget exceeded the upload buffer")
	}
}
