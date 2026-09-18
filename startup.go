package transport

import (
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/incident201/naivefox-transport/internal/cell"
)

const startupJournalLimit = 64 << 20

var startupJournalBytes atomic.Int64

// The journal and each in-flight writer retain one reservation reference.
type startupResult struct {
	refs   atomic.Int32
	digest [32]byte
	body   []byte
	status int
}

func reserveStartupBytes(size int64) bool {
	for {
		current := startupJournalBytes.Load()
		if size > startupJournalLimit-current {
			return false
		}
		if startupJournalBytes.CompareAndSwap(current, current+size) {
			return true
		}
	}
}

func (result *startupResult) release() {
	if result.refs.Add(-1) == 0 {
		startupJournalBytes.Add(-int64(len(result.body)))
	}
}

func (s *session) clearStartupLocked() {
	for _, result := range s.startupJournal {
		if result != nil {
			result.release()
		}
	}
	s.startupBytes = 0
	s.startupJournal = [40]*startupResult{}
}

func (t *Transport) startup(w http.ResponseWriter, r *http.Request, s *session, body []byte, sequence uint32, frames []cell.Frame, filler int) error {
	w.Header().Set("Cache-Control", responseCacheControl(r, false))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	round := int(sequence)
	upload := r.Method == http.MethodPost && r.URL.Path == "/api/sync" && r.URL.RawQuery == ""
	if !upload {
		var err error
		round, err = strconv.Atoi(r.URL.Query().Get("seq"))
		if err != nil || round < 0 || round >= len(startupSlots) || r.Method != http.MethodGet ||
			r.URL.RawQuery != "seq="+strconv.Itoa(round) || r.URL.Path != startupPath(round) {
			t.reject(w)
			return nil
		}
	} else if sequence >= uint32(len(startupSlots)) {
		t.reject(w)
		return nil
	}
	index := round * 2
	if !upload {
		index++
	}
	digest := sha256.Sum256(body)
	s.mu.Lock()
	reject := func() error {
		s.mu.Unlock()
		t.reject(w)
		return nil
	}
	if s.realtime || s.startupInvalid || r.Context().Err() != nil {
		return reject()
	}
	select {
	case <-s.peer.Done():
		return reject()
	default:
	}
	if !s.startupDeadline.IsZero() && !time.Now().Before(s.startupDeadline) {
		s.startupInvalid = true
		s.clearStartupLocked()
		s.peer.Close()
		return reject()
	}
	if result := s.startupJournal[index]; result != nil {
		if upload && result.digest != digest {
			return reject()
		}
		result.refs.Add(1)
		s.mu.Unlock()
		t.mu.Lock()
		t.stats.StartupReplays++
		t.mu.Unlock()
		return t.writeStartup(w, result)
	}
	if index != s.startupSteps || (upload && sequence != s.up) || (!upload && uint32(round) != s.down) {
		return reject()
	}
	result := &startupResult{digest: digest, status: http.StatusNoContent}
	useful, opens := uint64(0), uint64(0)
	if upload {
		if len(frames) > 0 && frames[0].Kind == cell.Auth {
			f := frames[0]
			if s.authed || len(frames) != 1 || f.Stream != 0 || f.Sequence != 0 || !t.authenticate(f.Body) {
				return reject()
			}
			s.authed = true
			frames = frames[1:]
		}
		if !s.authed || (len(frames) > 0 && s.startupSteps < 2) {
			return reject()
		}
		for _, f := range frames {
			if f.Kind == cell.Data {
				useful += uint64(len(f.Body))
			}
			if f.Kind == cell.Open {
				opens++
			}
		}
		if err := s.peer.Receive(frames); err != nil {
			s.peer.Close()
			s.startupInvalid = true
			s.clearStartupLocked()
			return reject()
		}
		s.up++
		select {
		case s.wake <- struct{}{}:
		default:
		}
	} else {
		capacity := startupSlots[round]
		if !reserveStartupBytes(int64(capacity)) {
			s.mu.Unlock()
			w.WriteHeader(http.StatusServiceUnavailable)
			return nil
		}
		if s.down == 0 {
			frames = []cell.Frame{{Kind: cell.Hello, Body: []byte(transportIdentity + "\n" + t.application.identity)}}
		} else {
			frames = s.peer.Take(capacity - cell.Header)
		}
		used := cell.Header
		for _, f := range frames {
			used += f.Size()
			if f.Kind == cell.Data {
				useful += uint64(len(f.Body))
			}
		}
		var err error
		result.body, err = cell.Encode(s.down, capacity, frames)
		if err != nil {
			startupJournalBytes.Add(-int64(capacity))
			s.startupInvalid = true
			s.clearStartupLocked()
			s.peer.Close()
			s.mu.Unlock()
			return err
		}
		filler = capacity - used
		result.status = http.StatusOK
		s.startupBytes += int64(capacity)
		s.down++
	}
	if s.startupDeadline.IsZero() {
		s.startupDeadline = time.Now().Add(2 * time.Minute)
	}
	result.refs.Store(2)
	s.startupJournal[index] = result
	s.startupSteps++
	completed := s.startupSteps == 40
	s.mu.Unlock()

	t.mu.Lock()
	if completed {
		t.stats.StartupCompleted++
	}
	if upload {
		t.stats.UploadBytes += uint64(len(body))
		t.stats.UploadFiller += uint64(filler)
		t.stats.UploadUseful += useful
		t.stats.Opens += opens
	} else {
		t.stats.DownloadBytes += uint64(len(result.body))
		t.stats.DownloadFiller += uint64(filler)
		t.stats.DownloadUseful += useful
		t.stats.CellCapacities[strconv.Itoa(len(result.body))]++
	}
	t.mu.Unlock()
	return t.writeStartup(w, result)
}

func (t *Transport) writeStartup(w http.ResponseWriter, result *startupResult) error {
	defer result.release()
	if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(30 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	if result.status == http.StatusNoContent {
		w.WriteHeader(result.status)
		return nil
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(result.body)))
	n, err := w.Write(result.body)
	if err == nil && n != len(result.body) {
		err = io.ErrShortWrite
	}
	if err != nil {
		t.mu.Lock()
		t.stats.WriteErrors++
		t.mu.Unlock()
	}
	return err
}
