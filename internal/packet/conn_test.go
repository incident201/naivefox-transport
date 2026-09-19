package packet

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"testing"
	"time"
)

func TestOrderedAndDuplicateUploads(t *testing.T) {
	c := New()
	defer c.Close()
	for i := UploadSlots - 1; i >= 0; i-- {
		body := bytes.Repeat([]byte{byte(i + 1)}, MaxBlock)
		next, err := c.Put(uint64(i), body)
		if err != nil {
			t.Fatal(err)
		}
		if i > 0 && next != 0 {
			t.Fatal("acknowledged gap")
		}
		if i == 0 && next != UploadSlots {
			t.Fatal("missing contiguous acknowledgement")
		}
		if _, err := c.Put(uint64(i), body); err != nil {
			t.Fatal(err)
		}
	}
	result := make([]byte, UploadLimit)
	if _, err := io.ReadFull(c, result); err != nil {
		t.Fatal(err)
	}
	for i, b := range result {
		if b != byte(i/MaxBlock+1) {
			t.Fatal("reordered stream")
		}
	}
	if in, out := c.Usage(); in != 0 || out != 0 {
		t.Fatal(in, out)
	}
	if _, err := c.Put(0, []byte("conflict")); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err := c.Put(c.Uploaded()+UploadSlots, []byte{1}); !errors.Is(err, ErrSequence) {
		t.Fatal(err)
	}
}

func TestUploadBoundAndOwnership(t *testing.T) {
	c := New()
	defer c.Close()
	body := bytes.Repeat([]byte{7}, MaxBlock)
	for i := 0; i < UploadSlots; i++ {
		if _, err := c.Put(uint64(i), body); err != nil {
			t.Fatal(err)
		}
	}
	body[0] = 9
	if _, err := c.Put(UploadSlots, []byte{1}); !errors.Is(err, ErrFull) {
		t.Fatal(err)
	}
	result := make([]byte, MaxBlock)
	if _, err := io.ReadFull(c, result); err != nil || result[0] != 7 {
		t.Fatal(err)
	}
	if _, err := c.Put(UploadSlots, body); err != nil {
		t.Fatal(err)
	}
	if in, _ := c.Usage(); in != UploadLimit {
		t.Fatal(in)
	}
}

func TestTinyUploadsCoalesceAndReceiptsExpire(t *testing.T) {
	c := New()
	defer c.Close()
	for i := 0; i < 1000; i++ {
		if _, err := c.Put(uint64(i), []byte{1}); err != nil {
			t.Fatal(err)
		}
	}
	if len(c.input) != 1 || len(c.receipts) != ReceiptCount {
		t.Fatal("unbounded metadata", len(c.input), len(c.receipts))
	}
	if _, err := c.Put(0, []byte{1}); !errors.Is(err, ErrGone) {
		t.Fatal(err)
	}
	if _, err := c.Put(999, []byte{1}); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentRetryIsAppliedOnce(t *testing.T) {
	c := New()
	defer c.Close()
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Put(0, []byte("once")); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if in, _ := c.Usage(); in != 4 {
		t.Fatal(in)
	}
}

func TestDownloadReconnectAndAuthenticatedCursor(t *testing.T) {
	c := New()
	defer c.Close()
	data := bytes.Repeat([]byte("ciphertext"), 1000)
	if _, err := c.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := c.Attach(1, 0); err != nil {
		t.Fatal(err)
	}
	first, err := c.Next(context.Background(), 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	first.Body[0] ^= 1
	if err := c.Attach(2, 0); err != nil {
		t.Fatal(err)
	}
	replay, err := c.Next(context.Background(), 2, 0)
	if err != nil || !bytes.Equal(replay.Body, data) {
		t.Fatal("retry changed ciphertext", err)
	}
	if _, err := c.Next(context.Background(), 1, 0); !errors.Is(err, ErrGeneration) {
		t.Fatal(err)
	}
	if err := c.Attach(1, 0); !errors.Is(err, ErrGeneration) {
		t.Fatal(err)
	}
	if err := c.Ack(2); !errors.Is(err, ErrSequence) {
		t.Fatal(err)
	}
	if err := c.Ack(1); err != nil {
		t.Fatal(err)
	}
	if err := c.Attach(3, 0); !errors.Is(err, ErrGone) {
		t.Fatal(err)
	}
	if _, out := c.Usage(); out != 0 {
		t.Fatal(out)
	}
}

func TestReplayBackpressure(t *testing.T) {
	c := New()
	defer c.Close()
	if _, err := c.Write(make([]byte, ReplayLimit)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := c.Write([]byte{9}); done <- err }()
	select {
	case err := <-done:
		t.Fatal("did not backpressure", err)
	case <-time.After(20 * time.Millisecond):
	}
	if err := c.Ack(1); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("writer did not resume")
	}
	if _, out := c.Usage(); out > ReplayLimit {
		t.Fatal(out)
	}
}

func TestTinyOutputHasRecordBound(t *testing.T) {
	c := New()
	defer c.Close()
	for i := 0; i < ReplaySlots; i++ {
		if _, err := c.Write([]byte{1}); err != nil {
			t.Fatal(err)
		}
	}
	c.SetWriteDeadline(time.Now().Add(10 * time.Millisecond))
	if _, err := c.Write([]byte{2}); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatal(err)
	}
	if len(c.output) != ReplaySlots {
		t.Fatal(len(c.output))
	}
}

func TestRequestCancellationDoesNotCloseTLSStream(t *testing.T) {
	c := New()
	defer c.Close()
	if err := c.Attach(1, 0); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Next(ctx, 1, 0); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := c.Put(0, []byte("alive")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 5)
	if _, err := io.ReadFull(c, got); err != nil || string(got) != "alive" {
		t.Fatal(err)
	}
}

func TestNewDownloadWakesOldRequest(t *testing.T) {
	c := New()
	defer c.Close()
	if err := c.Attach(1, 0); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := c.Next(context.Background(), 1, 0); done <- err }()
	if err := c.Attach(2, 0); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrGeneration) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("old request stalled")
	}
}

func TestCloseReleasesEverythingAndUnblocks(t *testing.T) {
	c := New()
	done := make(chan error, 1)
	go func() { _, err := c.Read(make([]byte, 1)); done <- err }()
	c.Close()
	select {
	case err := <-done:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("reader stalled")
	}
	if _, err := c.Put(0, []byte{1}); !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}
	if _, err := c.Write([]byte{1}); !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}
	if in, out := c.Usage(); in != 0 || out != 0 {
		t.Fatal(in, out)
	}
}

func TestDeadlineChangesWakeBlockedRead(t *testing.T) {
	c := New()
	defer c.Close()
	done := make(chan error, 1)
	go func() { _, err := c.Read(make([]byte, 1)); done <- err }()
	c.SetReadDeadline(time.Now().Add(-time.Second))
	select {
	case err := <-done:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("read ignored deadline")
	}
	c.SetReadDeadline(time.Time{})
	if _, err := c.Put(0, []byte{1}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Read(make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
}

func TestMACBindsOperationSessionCountersAndExactBody(t *testing.T) {
	secret := bytes.Repeat([]byte{7}, 32)
	body := []byte("ciphertext")
	tag := MAC(secret, "upload", "session", 1, 3, body)
	if !Verify(secret, "upload", "session", 1, 3, body, tag) {
		t.Fatal("valid MAC rejected")
	}
	for _, bad := range []struct {
		op, id      string
		seq, cursor uint64
		body        []byte
	}{
		{"download", "session", 1, 3, body}, {"upload", "other", 1, 3, body},
		{"upload", "session", 2, 3, body}, {"upload", "session", 1, 4, body},
		{"upload", "session", 1, 3, []byte("modified")},
	} {
		if Verify(secret, bad.op, bad.id, bad.seq, bad.cursor, bad.body, tag) {
			t.Fatal("forgery accepted")
		}
	}
	if Verify(nil, "upload", "session", 1, 3, body, tag) {
		t.Fatal("missing session secret")
	}
	if Verify(secret, "upload", "session", 1, 3, body, tag[:31]) {
		t.Fatal("truncated MAC")
	}
}
