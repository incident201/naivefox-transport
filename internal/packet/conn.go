package packet

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"math"
	"net"
	"os"
	"sync"
	"time"
)

const (
	MaxBlock     = 64 * 1024
	UploadSlots  = 8
	UploadLimit  = UploadSlots * MaxBlock
	ReplayLimit  = 2 * 1024 * 1024
	ReceiptCount = 2 * UploadSlots
	ReplaySlots  = 128
)

var (
	ErrSequence   = errors.New("packet sequence outside window")
	ErrConflict   = errors.New("conflicting packet retry")
	ErrFull       = errors.New("packet queue full")
	ErrGone       = errors.New("packet replay unavailable")
	ErrGeneration = errors.New("stale packet download")
)

type Block struct {
	Sequence uint64
	Body     []byte
}

type upload struct {
	body []byte
	hash [32]byte
}

// Conn is a bounded reliable byte stream below TLS; HTTP request lifetimes do
// not own it. A caller authenticates all operations before mutating its state.
type Conn struct {
	mu                          sync.Mutex
	changed                     chan struct{}
	closed                      bool
	err                         error
	input                       [][]byte
	inputOffset                 int
	inputBytes                  int
	uploadNext                  uint64
	pending                     map[uint64]upload
	receipts                    map[uint64][32]byte
	output                      []Block
	outputBytes                 int
	outputNext                  uint64
	outputFloor                 uint64
	generation                  uint64
	attached                    bool
	readDeadline, writeDeadline time.Time
	readWaiting                 bool
}

func New() *Conn {
	return &Conn{changed: make(chan struct{}), pending: make(map[uint64]upload), receipts: make(map[uint64][32]byte)}
}

func (c *Conn) signal() {
	close(c.changed)
	c.changed = make(chan struct{})
}

func (c *Conn) Put(sequence uint64, body []byte) (uint64, error) {
	if len(body) == 0 || len(body) > MaxBlock {
		return 0, ErrSequence
	}
	hash := sha256.Sum256(body)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return c.uploadNext, c.closeError()
	}
	if sequence < c.uploadNext {
		previous, ok := c.receipts[sequence]
		if !ok {
			return c.uploadNext, ErrGone
		}
		if previous != hash {
			return c.uploadNext, ErrConflict
		}
		return c.uploadNext, nil
	}
	if sequence == math.MaxUint64 || sequence-c.uploadNext >= UploadSlots {
		return c.uploadNext, ErrSequence
	}
	if previous, ok := c.pending[sequence]; ok {
		if previous.hash != hash {
			return c.uploadNext, ErrConflict
		}
		return c.uploadNext, nil
	}
	if len(body) > UploadLimit-c.inputBytes {
		return c.uploadNext, ErrFull
	}
	saved := append([]byte(nil), body...)
	c.pending[sequence] = upload{saved, hash}
	c.inputBytes += len(saved)
	for {
		current, ok := c.pending[c.uploadNext]
		if !ok {
			break
		}
		for len(current.body) > 0 {
			if len(c.input) == 0 || len(c.input[len(c.input)-1]) == 16384 {
				c.input = append(c.input, make([]byte, 0, 16384))
			}
			last := len(c.input) - 1
			count := min(len(current.body), 16384-len(c.input[last]))
			c.input[last] = append(c.input[last], current.body[:count]...)
			current.body = current.body[count:]
		}
		c.receipts[c.uploadNext] = current.hash
		delete(c.pending, c.uploadNext)
		c.uploadNext++
		if c.uploadNext > ReceiptCount {
			delete(c.receipts, c.uploadNext-ReceiptCount-1)
		}
	}
	c.signal()
	return c.uploadNext, nil
}

func (c *Conn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for {
		if c.closed {
			return 0, c.closeError()
		}
		if deadlinePassed(c.readDeadline) {
			return 0, os.ErrDeadlineExceeded
		}
		if len(c.input) > 0 {
			n := copy(p, c.input[0][c.inputOffset:])
			c.inputOffset += n
			c.inputBytes -= n
			if c.inputOffset == len(c.input[0]) {
				c.input[0] = nil
				c.input = c.input[1:]
				c.inputOffset = 0
			}
			c.readWaiting = false
			c.signal()
			return n, nil
		}
		c.readWaiting = true
		c.signal()
		err := c.waitLocked(context.Background(), c.readDeadline)
		c.readWaiting = false
		if err != nil {
			return 0, err
		}
	}
}

func (c *Conn) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	written := 0
	for len(p) > 0 {
		if c.closed {
			return written, c.closeError()
		}
		if deadlinePassed(c.writeDeadline) {
			return written, os.ErrDeadlineExceeded
		}
		n := min(len(p), MaxBlock)
		if n > ReplayLimit-c.outputBytes || len(c.output) >= ReplaySlots {
			if err := c.waitLocked(context.Background(), c.writeDeadline); err != nil {
				return written, err
			}
			continue
		}
		if c.outputNext == math.MaxUint64 {
			return written, ErrSequence
		}
		body := append([]byte(nil), p[:n]...)
		c.output = append(c.output, Block{c.outputNext, body})
		c.outputNext++
		c.outputBytes += n
		written += n
		p = p[n:]
		c.signal()
	}
	return written, nil
}

func (c *Conn) Ack(next uint64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if next < c.outputFloor {
		return nil
	}
	return c.ackLocked(next)
}

func (c *Conn) ackLocked(next uint64) error {
	if c.closed {
		return c.closeError()
	}
	if next > c.outputNext {
		return ErrSequence
	}
	if next < c.outputFloor {
		return ErrGone
	}
	for len(c.output) > 0 && c.output[0].Sequence < next {
		c.outputBytes -= len(c.output[0].Body)
		c.output[0].Body = nil
		c.output = c.output[1:]
	}
	c.outputFloor = next
	c.signal()
	return nil
}

func (c *Conn) Attach(generation, next uint64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if generation == 0 || (c.attached && generation <= c.generation) {
		return ErrGeneration
	}
	if err := c.ackLocked(next); err != nil {
		return err
	}
	c.generation = generation
	c.attached = true
	c.signal()
	return nil
}

func (c *Conn) Next(ctx context.Context, generation, next uint64) (Block, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for {
		if c.closed {
			return Block{}, c.closeError()
		}
		if !c.attached || generation != c.generation {
			return Block{}, ErrGeneration
		}
		if next < c.outputFloor {
			return Block{}, ErrGone
		}
		if next > c.outputNext {
			return Block{}, ErrSequence
		}
		if next < c.outputNext {
			saved := c.output[next-c.outputFloor]
			// The response writer owns one bounded copy, so ACK can release retention.
			return Block{saved.Sequence, append([]byte(nil), saved.Body...)}, nil
		}
		if err := c.waitLocked(ctx, time.Time{}); err != nil {
			return Block{}, err
		}
	}
}

// Flight returns only when TLS has emitted output and is waiting for more input.
// It is used for the finite, unauthenticated TLS handshake exchanges.
func (c *Conn) Flight(ctx context.Context, next uint64) ([]Block, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for {
		if c.closed {
			return nil, c.closeError()
		}
		if next < c.outputFloor {
			return nil, ErrGone
		}
		if next > c.outputNext {
			return nil, ErrSequence
		}
		if c.readWaiting && next < c.outputNext {
			var result []Block
			for _, b := range c.output[next-c.outputFloor:] {
				result = append(result, Block{b.Sequence, append([]byte(nil), b.Body...)})
			}
			return result, nil
		}
		if err := c.waitLocked(ctx, time.Time{}); err != nil {
			return nil, err
		}
	}
}

func (c *Conn) Usage() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.inputBytes, c.outputBytes
}

func (c *Conn) Uploaded() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.uploadNext
}

func (c *Conn) waitLocked(ctx context.Context, deadline time.Time) error {
	changed := c.changed
	c.mu.Unlock()
	var timer *time.Timer
	var timeout <-chan time.Time
	if !deadline.IsZero() {
		timer = time.NewTimer(time.Until(deadline))
		timeout = timer.C
	}
	var err error
	select {
	case <-changed:
	case <-ctx.Done():
		err = ctx.Err()
	case <-timeout:
		err = os.ErrDeadlineExceeded
	}
	if timer != nil {
		timer.Stop()
	}
	c.mu.Lock()
	return err
}

func deadlinePassed(deadline time.Time) bool {
	return !deadline.IsZero() && !time.Now().Before(deadline)
}
func (c *Conn) closeError() error {
	if c.err != nil {
		return c.err
	}
	return net.ErrClosed
}
func (c *Conn) Close() error { return c.CloseWithError(nil) }
func (c *Conn) CloseWithError(err error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.closed = true
		c.err = err
		c.input = nil
		c.pending = nil
		c.receipts = nil
		c.output = nil
		c.inputBytes = 0
		c.outputBytes = 0
		c.signal()
	}
	return nil
}
func (c *Conn) LocalAddr() net.Addr  { return packetAddr{} }
func (c *Conn) RemoteAddr() net.Addr { return packetAddr{} }
func (c *Conn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readDeadline = t
	c.writeDeadline = t
	c.signal()
	return nil
}
func (c *Conn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readDeadline = t
	c.signal()
	return nil
}
func (c *Conn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writeDeadline = t
	c.signal()
	return nil
}

type packetAddr struct{}

func (packetAddr) Network() string { return "naivefox-packet" }
func (packetAddr) String() string  { return "naivefox-packet" }

var _ net.Conn = (*Conn)(nil)
var _ io.ReadWriteCloser = (*Conn)(nil)
