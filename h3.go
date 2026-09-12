package transport

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/incident201/naivefox-transport/internal/cell"
)

const maxPendingUploads = cell.HTTP3Window / 131072

type pendingUpload struct {
	ctx  context.Context
	body []byte
	done chan struct{}
	err  error
}

type cancelCloser struct{ cancel context.CancelFunc }

func (c cancelCloser) Close() error { c.cancel(); return nil }

func (t *Transport) h3Stream(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	s, err := t.getSession(w, r)
	if err != nil {
		return t.decline(w, r, next)
	}
	s.mu.Lock()
	if !s.authed {
		s.mu.Unlock()
		return t.decline(w, r, next)
	}
	ready := s.h3 && r.Method == http.MethodGet && r.ProtoMajor == 3 && r.TLS != nil &&
		!s.realtime && !s.startupInvalid && s.startupSteps == 40 &&
		s.up >= 20 && s.down >= 20 && s.httpActive == 0
	select {
	case <-s.peer.Done():
		ready = false
	default:
	}
	if !ready {
		s.mu.Unlock()
		t.reject(w)
		return nil
	}
	ctx, cancel := context.WithCancel(r.Context())
	s.realtime, s.h3 = true, true
	s.realtimeConn = cancelCloser{cancel}
	s.pendingUploads = make(map[uint32]*pendingUpload)
	s.wsStartupUp, s.wsStartupDown = s.up, s.down
	s.mu.Unlock()
	defer cancel()
	defer s.close()
	t.mu.Lock()
	t.stats.H3Opened++
	t.stats.Requests["GET /api/stream"]++
	t.stats.Protocols["HTTP/3.0"]++
	t.mu.Unlock()
	defer func() { t.mu.Lock(); t.stats.H3Closed++; t.mu.Unlock() }()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	controller := http.NewResponseController(w)
	if err := controller.Flush(); err != nil {
		return err
	}
	t.writeCells(ctx, s, 25*time.Second, func(body []byte) error {
		if err := controller.SetWriteDeadline(time.Now().Add(30 * time.Second)); err != nil {
			return err
		}
		var prefix [4]byte
		binary.BigEndian.PutUint32(prefix[:], uint32(len(body)))
		if n, err := w.Write(prefix[:]); err != nil {
			return err
		} else if n != len(prefix) {
			return io.ErrShortWrite
		}
		if n, err := w.Write(body); err != nil {
			return err
		} else if n != len(body) {
			return io.ErrShortWrite
		}
		return controller.Flush()
	})
	return nil
}

func (t *Transport) h3Upload(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	s, err := t.getSession(w, r)
	if err != nil {
		return t.decline(w, r, next)
	}
	s.mu.Lock()
	authed := s.authed
	ready := s.h3 && s.realtime && s.h3Active < maxPendingUploads
	if ready {
		s.h3Active++
	}
	s.mu.Unlock()
	if ready {
		defer func() { s.mu.Lock(); s.h3Active--; s.mu.Unlock() }()
	}
	if !authed {
		return t.decline(w, r, next)
	}
	if !ready || r.Method != http.MethodPost || r.ProtoMajor != 3 || r.TLS == nil {
		t.reject(w)
		return nil
	}
	controller := http.NewResponseController(w)
	if err := controller.SetReadDeadline(time.Now().Add(45 * time.Second)); err != nil {
		return err
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 131073))
	r.Body.Close()
	if err != nil {
		s.close()
		return err
	}
	if err := t.orderedUpload(r.Context(), s, body); err != nil {
		s.close()
		t.reject(w)
		return nil
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// A POST slot is released only after all preceding sequences have been applied.
// This bounds out-of-order retention even when an earlier QUIC stream stalls.
func (t *Transport) orderedUpload(ctx context.Context, s *session, body []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(body) != 512 && len(body) != 4096 && len(body) != 16384 && len(body) != 131072 {
		return errors.New("upload capacity")
	}
	sequence, _, _, err := cell.Decode(body)
	if err != nil {
		return err
	}
	pending := &pendingUpload{ctx: ctx, body: body, done: make(chan struct{})}
	s.uploadMu.Lock()
	if err := ctx.Err(); err != nil {
		s.uploadMu.Unlock()
		return err
	}
	s.mu.Lock()
	next := s.up
	s.mu.Unlock()
	if sequence < next || uint64(sequence)-uint64(next) >= maxPendingUploads ||
		len(s.pendingUploads) >= maxPendingUploads || s.pendingUploads[sequence] != nil {
		s.uploadMu.Unlock()
		return errors.New("upload sequence outside pipeline")
	}
	s.pendingUploads[sequence] = pending
	defer func() {
		s.uploadMu.Lock()
		if s.pendingUploads[sequence] == pending {
			delete(s.pendingUploads, sequence)
		}
		pending.body = nil
		s.uploadMu.Unlock()
	}()
	for {
		current := s.pendingUploads[next]
		if current == nil {
			break
		}
		current.err = current.ctx.Err()
		if current.err == nil {
			current.err = t.receiveRealtime(s, current.body)
		}
		current.body = nil
		delete(s.pendingUploads, next)
		close(current.done)
		if current.err != nil {
			s.close()
			break
		}
		next++
	}
	s.uploadMu.Unlock()
	timer := time.NewTimer(45 * time.Second)
	defer timer.Stop()
	select {
	case <-pending.done:
		return pending.err
	case <-ctx.Done():
		return ctx.Err()
	case <-s.peer.Done():
		return errors.New("carrier closed")
	case <-timer.C:
		return errors.New("upload sequence timeout")
	}
}
