package mux

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"

	"github.com/incident201/naivefox-transport/internal/cell"
)

type DialFunc func(context.Context, string) (net.Conn, error)

type Stats struct {
	ReceiveWindow uint32 `json:"receive_window"`
	Opened        uint64 `json:"opened"`
	Reset         uint64 `json:"reset"`
	Received      uint64 `json:"received"`
	Sent          uint64 `json:"sent"`
	Delivered     uint64 `json:"delivered"`
	PeakStreams   int    `json:"peak_streams"`
}

type stream struct {
	id               uint32
	ctx              context.Context
	cancel           context.CancelFunc
	connMu           sync.Mutex
	conn             net.Conn
	input            chan cell.Frame
	output           chan cell.Frame
	pending          *cell.Frame
	open             *cell.Frame
	ack              bool
	reset            bool
	credit           uint32
	budget           uint32
	grant            uint32
	nextIn           uint32
	remoteFin        bool
	remoteFinWritten bool
	localFinSent     bool
	queuedBytes      atomic.Int64
}

func (s *stream) close() {
	s.cancel()
	s.connMu.Lock()
	if s.conn != nil {
		s.conn.Close()
	}
	s.connMu.Unlock()
}

type Peer struct {
	mu      sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	dial    DialFunc
	window  uint32
	streams map[uint32]*stream
	order   []uint32
	cursor  int
	highest uint32
	closed  bool
	stats   Stats
	changes chan struct{}
}

func New(dial DialFunc) *Peer {
	peer, _ := NewWithWindow(dial, 0)
	return peer
}

// NewWithWindow requires identical private configuration at both peers. Zero
// retains the original window; only the bounded experimental double is allowed.
func NewWithWindow(dial DialFunc, window uint32) (*Peer, error) {
	if window == 0 {
		window = cell.Window
	}
	if window != cell.Window && window != 2*cell.Window {
		return nil, errors.New("unsupported receive window")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Peer{ctx: ctx, cancel: cancel, dial: dial, window: window, stats: Stats{ReceiveWindow: window}, streams: make(map[uint32]*stream), changes: make(chan struct{}, 1)}, nil
}

func (p *Peer) notify() {
	select {
	case p.changes <- struct{}{}:
	default:
	}
}

func (p *Peer) Changes() <-chan struct{} { return p.changes }
func (p *Peer) Done() <-chan struct{}    { return p.ctx.Done() }

type Pressure struct {
	Streams  int   `json:"streams"`
	Readable int   `json:"readable"`
	Bytes    int64 `json:"bytes"`
	Queued   int64 `json:"queued"`
	Controls int   `json:"controls"`
}

func (p *Peer) Pressure() Pressure {
	p.mu.Lock()
	defer p.mu.Unlock()
	state := Pressure{Streams: len(p.streams)}
	for _, s := range p.streams {
		if !s.localFinSent && !s.reset {
			state.Readable++
		}
		queued := s.queuedBytes.Load()
		state.Bytes += min(queued, int64(s.credit))
		state.Queued += queued
		if s.open != nil || s.ack || s.reset || s.grant > 0 || (s.queuedBytes.Load() == 0 && (s.pending != nil || len(s.output) > 0)) {
			state.Controls++
		}
	}
	return state
}

func (p *Peer) newStream(id uint32) *stream {
	ctx, cancel := context.WithCancel(p.ctx)
	s := &stream{id: id, ctx: ctx, cancel: cancel, input: make(chan cell.Frame, 128), output: make(chan cell.Frame, 16), credit: p.window, budget: p.window}
	p.streams[id] = s
	p.order = append(p.order, id)
	p.stats.Opened++
	if len(p.streams) > p.stats.PeakStreams {
		p.stats.PeakStreams = len(p.streams)
	}
	return s
}

// The caller holds mu. Retired IDs must not accumulate when the peer only
// uploads OPEN/RESET cells and never calls Take to read a response.
func (p *Peer) removeStream(id uint32) {
	delete(p.streams, id)
	for index, value := range p.order {
		if value != id {
			continue
		}
		p.order = append(p.order[:index], p.order[index+1:]...)
		if index < p.cursor {
			p.cursor--
		}
		if p.cursor >= len(p.order) {
			p.cursor = 0
		}
		return
	}
}

func (p *Peer) Open(conn net.Conn, authority string) (uint32, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.dial != nil || len(p.streams) >= cell.MaxStreams || len(authority) > 512 || p.highest == ^uint32(0) {
		return 0, errors.New("stream limit")
	}
	p.highest++
	s := p.newStream(p.highest)
	s.open = &cell.Frame{Kind: cell.Open, Stream: s.id, Body: []byte(authority)}
	p.attach(s, conn)
	p.notify()
	return s.id, nil
}

func (p *Peer) attach(s *stream, conn net.Conn) {
	s.connMu.Lock()
	if s.ctx.Err() != nil {
		conn.Close()
		s.connMu.Unlock()
		return
	}
	s.conn = conn
	s.connMu.Unlock()
	p.wg.Add(2)
	go p.read(s, conn)
	go p.write(s, conn)
}

func (p *Peer) fail(s *stream) {
	p.mu.Lock()
	if !s.reset {
		s.reset = true
		p.stats.Reset++
	}
	p.mu.Unlock()
	s.close()
	p.notify()
}

func (p *Peer) read(s *stream, conn net.Conn) {
	defer p.wg.Done()
	var sequence uint32
	for {
		body := make([]byte, 16*1024)
		n, err := conn.Read(body)
		if n > 0 {
			if uint64(sequence)+uint64(n) > uint64(^uint32(0)) {
				p.fail(s)
				return
			}
			f := cell.Frame{Kind: cell.Data, Stream: s.id, Sequence: sequence, Body: body[:n]}
			s.queuedBytes.Add(int64(n))
			select {
			case s.output <- f:
				sequence += uint32(n)
				p.notify()
			case <-s.ctx.Done():
				s.queuedBytes.Add(-int64(n))
				return
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				select {
				case s.output <- cell.Frame{Kind: cell.Fin, Stream: s.id, Sequence: sequence}:
					p.notify()
				case <-s.ctx.Done():
				}
			} else if s.ctx.Err() == nil {
				p.fail(s)
			}
			return
		}
	}
}

func (p *Peer) write(s *stream, conn net.Conn) {
	defer p.wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case f := <-s.input:
			if f.Kind == cell.Fin {
				if half, ok := conn.(interface{ CloseWrite() error }); ok {
					if err := half.CloseWrite(); err != nil {
						p.fail(s)
						return
					}
				}
				p.mu.Lock()
				s.remoteFinWritten = true
				p.mu.Unlock()
				return
			}
			body := f.Body
			for len(body) > 0 {
				n, err := conn.Write(body)
				if err != nil || n == 0 {
					p.fail(s)
					return
				}
				body = body[n:]
			}
			p.mu.Lock()
			s.budget += uint32(len(f.Body))
			s.grant += uint32(len(f.Body))
			p.stats.Delivered += uint64(len(f.Body))
			p.mu.Unlock()
			p.notify()
		}
	}
}

func (p *Peer) Receive(frames []cell.Frame) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return errors.New("closed peer")
	}
	for _, f := range frames {
		if f.Stream == 0 {
			return errors.New("zero stream")
		}
		if f.Kind == cell.Open {
			if p.dial == nil || f.Stream <= p.highest || f.Sequence != 0 || len(f.Body) == 0 || len(f.Body) > 512 || len(p.streams) >= cell.MaxStreams {
				return errors.New("invalid stream open")
			}
			if _, _, err := net.SplitHostPort(string(f.Body)); err != nil {
				return errors.New("invalid authority")
			}
			p.highest = f.Stream
			s := p.newStream(f.Stream)
			target := string(f.Body)
			p.wg.Add(1)
			go func() {
				defer p.wg.Done()
				conn, err := p.dial(s.ctx, target)
				if err != nil {
					p.fail(s)
					return
				}
				p.mu.Lock()
				s.ack = true
				p.attach(s, conn)
				p.mu.Unlock()
				p.notify()
			}()
			continue
		}
		s := p.streams[f.Stream]
		if s == nil {
			if f.Stream <= p.highest && f.Kind != cell.Auth {
				continue
			}
			return errors.New("unknown stream")
		}
		switch f.Kind {
		case cell.Data:
			if s.remoteFin || f.Sequence != s.nextIn || len(f.Body) == 0 || uint64(len(f.Body)) > uint64(s.budget) || uint64(s.nextIn)+uint64(len(f.Body)) > uint64(^uint32(0)) {
				return errors.New("data sequence or credit")
			}
			select {
			case s.input <- f:
			default:
				return errors.New("input frame bound")
			}
			s.nextIn += uint32(len(f.Body))
			s.budget -= uint32(len(f.Body))
			p.stats.Received += uint64(len(f.Body))
		case cell.Fin:
			if s.remoteFin || len(f.Body) != 0 || f.Sequence != s.nextIn {
				return errors.New("fin sequence")
			}
			select {
			case s.input <- f:
			default:
				return errors.New("input frame bound")
			}
			s.remoteFin = true
		case cell.Reset:
			if len(f.Body) != 0 || f.Sequence != 0 {
				return errors.New("reset format")
			}
			s.close()
			p.removeStream(s.id)
			p.stats.Reset++
		case cell.Credit:
			if len(f.Body) != 4 || f.Sequence != 0 {
				return errors.New("credit format")
			}
			grant := binary.BigEndian.Uint32(f.Body)
			if grant == 0 || grant > p.window-s.credit {
				return errors.New("credit overflow")
			}
			s.credit += grant
		case cell.Opened:
			if p.dial != nil || f.Sequence != 0 || len(f.Body) != 0 {
				return errors.New("open acknowledgement")
			}
		default:
			return errors.New("unexpected frame")
		}
	}
	if len(frames) > 0 {
		p.notify()
	}
	return nil
}

func (p *Peer) Take(budget int) []cell.Frame {
	p.mu.Lock()
	defer p.mu.Unlock()
	frames := []cell.Frame{}
	if p.closed {
		return frames
	}
	misses := 0
	for budget >= cell.FrameHeader && len(p.order) > 0 && misses < len(p.order) && len(frames) < 4096 {
		p.cursor %= len(p.order)
		id := p.order[p.cursor]
		p.cursor++
		s := p.streams[id]
		if s == nil {
			misses++
			continue
		}
		if s.localFinSent && s.remoteFinWritten && s.grant == 0 {
			s.close()
			p.removeStream(id)
			continue
		}
		var f *cell.Frame
		switch {
		case s.reset:
			f = &cell.Frame{Kind: cell.Reset, Stream: id}
			p.removeStream(id)
		case s.open != nil:
			if s.open.Size() <= budget {
				f = s.open
				s.open = nil
			}
		case s.ack:
			f = &cell.Frame{Kind: cell.Opened, Stream: id}
			s.ack = false
		case s.grant > 0 && budget >= cell.FrameHeader+4:
			f = &cell.Frame{Kind: cell.Credit, Stream: id, Body: cell.Uint32(s.grant)}
			s.grant = 0
		default:
			if s.pending == nil {
				select {
				case value := <-s.output:
					s.pending = &value
				default:
				}
			}
			if s.pending != nil {
				if s.pending.Kind == cell.Data {
					n := min(len(s.pending.Body), budget-cell.FrameHeader, int(s.credit))
					if n > 0 {
						value := *s.pending
						value.Body = append([]byte(nil), value.Body[:n]...)
						f = &value
						s.pending.Body = s.pending.Body[n:]
						s.pending.Sequence += uint32(n)
						s.credit -= uint32(n)
						s.queuedBytes.Add(-int64(n))
						p.stats.Sent += uint64(n)
						if len(s.pending.Body) == 0 {
							s.pending = nil
						}
					}
				} else {
					f = s.pending
					s.pending = nil
					s.localFinSent = true
				}
			}
		}
		if f == nil {
			misses++
			continue
		}
		frames = append(frames, *f)
		budget -= f.Size()
		misses = 0
		if s.localFinSent && s.remoteFinWritten && s.grant == 0 {
			s.close()
			p.removeStream(id)
		}
	}
	return frames
}

func (p *Peer) Snapshot() Stats { p.mu.Lock(); defer p.mu.Unlock(); return p.stats }

func (p *Peer) Close() {
	p.mu.Lock()
	p.closed = true
	p.cancel()
	for _, s := range p.streams {
		s.close()
	}
	p.mu.Unlock()
	p.wg.Wait()
}
