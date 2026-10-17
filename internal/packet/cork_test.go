package packet

import (
	"bytes"
	"testing"
)

func TestCorkSplitsOneWriteIntoEvenBlocks(t *testing.T) {
	c := New()
	defer c.Close()
	c.Cork()
	for _, record := range []int{16406, 16406, 16406, 16406, 26} {
		if n, err := c.Write(bytes.Repeat([]byte{1}, record)); err != nil || n != record {
			t.Fatal(n, err)
		}
	}
	if c.outputNext != 0 {
		t.Fatal("corked write produced a block")
	}
	if err := c.Uncork(); err != nil {
		t.Fatal(err)
	}
	if len(c.output) != 2 || len(c.output[0].Body) != 32825 || len(c.output[1].Body) != 32825 {
		t.Fatalf("blocks %d", len(c.output))
	}
	if n, err := c.Write([]byte{2}); err != nil || n != 1 || len(c.output) != 3 {
		t.Fatal("uncorked write was buffered")
	}
}

func TestReadyReportsQueuedBlocksWithoutWaiting(t *testing.T) {
	c := New()
	defer c.Close()
	if c.Ready(1, 0) {
		t.Fatal("unattached connection reported a block")
	}
	if err := c.Attach(1, 0); err != nil {
		t.Fatal(err)
	}
	if c.Ready(1, 0) {
		t.Fatal("empty output reported a block")
	}
	if _, err := c.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	if !c.Ready(1, 0) || c.Ready(1, 1) || c.Ready(2, 0) {
		t.Fatal("ready state differs from queued output")
	}
}
