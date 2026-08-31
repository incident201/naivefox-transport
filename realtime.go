package transport

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
	"github.com/incident201/naivefox-transport/internal/cell"
)

const realtimeProtocol = "nfc1.hybrid.v1"

type observedResponse struct {
	http.ResponseWriter
	status int
	failed bool
}

func (w *observedResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *observedResponse) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(body)
	w.failed = w.failed || err != nil || n != len(body)
	return n, err
}

func (w *observedResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (s *session) beginHTTP(w http.ResponseWriter, r *http.Request) (*observedResponse, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.realtime {
		return nil, false
	}
	s.httpActive++
	return &observedResponse{ResponseWriter: w}, true
}

func startupPath(round int) string {
	if round < 4 || round >= 18 {
		return "/api/events/brief"
	}
	if round < 6 {
		return "/api/events/state"
	}
	return "/media/chunk/" + strconv.Itoa(round)
}

func (t *Transport) finishHTTP(s *session, w *observedResponse, r *http.Request) {
	s.mu.Lock()
	s.httpActive--
	completed := false
	if !s.startupInvalid && s.startupSteps < 40 {
		method, path, status := http.MethodPost, "/api/sync", http.StatusNoContent
		if s.startupSteps%2 == 1 {
			method, path, status = http.MethodGet, startupPath(s.startupSteps/2), http.StatusOK
		}
		if r.Method != method || r.URL.Path != path || w.failed || w.status != status {
			s.startupInvalid = true
		} else {
			s.startupSteps++
			completed = s.startupSteps == 40
		}
	}
	s.mu.Unlock()
	if completed {
		t.mu.Lock()
		t.stats.StartupCompleted++
		t.mu.Unlock()
	}
}

func (t *Transport) realtime(w http.ResponseWriter, r *http.Request) error {
	if t.profileName() != defaultProfile || t.AppendMode || r.Method != http.MethodGet || r.ProtoMajor != 1 || r.TLS == nil {
		t.reject(w)
		return nil
	}
	s, err := t.getSession(w, r)
	if err != nil {
		t.reject(w)
		return nil
	}
	supported := false
	for _, protocol := range websocket.Subprotocols(r) {
		supported = supported || protocol == realtimeProtocol
	}
	s.mu.Lock()
	ready := supported && !s.realtime && !s.startupInvalid && s.startupSteps == 40 && s.up >= 20 && s.down >= 20 && s.httpActive == 0
	select {
	case <-s.peer.Done():
		ready = false
	default:
	}
	if ready {
		s.realtime = true
	}
	s.mu.Unlock()
	if !ready {
		t.reject(w)
		return nil
	}
	upgrader := websocket.Upgrader{
		Subprotocols:   []string{realtimeProtocol},
		ReadBufferSize: 4096, WriteBufferSize: 4096,
		EnableCompression: false,
		CheckOrigin: func(request *http.Request) bool {
			origin, err := url.Parse(request.Header.Get("Origin"))
			return err == nil && origin.Scheme == "https" && origin.Host == request.Host && origin.User == nil && origin.Path == "" && origin.RawQuery == "" && origin.Fragment == ""
		},
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.close()
		return nil
	}
	s.mu.Lock()
	s.realtimeConn = conn
	s.wsStartupUp, s.wsStartupDown = s.up, s.down
	startupUp, startupDown := s.up, s.down
	s.mu.Unlock()
	t.mu.Lock()
	if t.stats.WSOpened == 0 {
		t.stats.WSStartupMinUp, t.stats.WSStartupMinDown = startupUp, startupDown
	} else {
		t.stats.WSStartupMinUp = min(t.stats.WSStartupMinUp, startupUp)
		t.stats.WSStartupMinDown = min(t.stats.WSStartupMinDown, startupDown)
	}
	t.stats.WSOpened++
	if t.stats.WSCellCapacities == nil {
		t.stats.WSCellCapacities = make(map[string]uint64)
	}
	t.stats.Requests["GET /api/realtime"]++
	t.stats.Protocols["HTTP/1.1"]++
	t.mu.Unlock()
	defer func() { t.mu.Lock(); t.stats.WSClosed++; t.mu.Unlock() }()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	defer s.close()
	conn.SetReadLimit(cell.MaxCell)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		defer cancel()
		defer conn.Close()
		for {
			conn.SetReadDeadline(time.Now().Add(75 * time.Second))
			kind, body, err := conn.ReadMessage()
			if err != nil || kind != websocket.BinaryMessage || t.receiveRealtime(s, body) != nil {
				return
			}
		}
	}()
	t.writeRealtime(ctx, conn, s)
	conn.Close()
	<-readerDone
	return nil
}

func (t *Transport) receiveRealtime(s *session, body []byte) error {
	if len(body) != 512 && len(body) != 65536 && len(body) != cell.MaxCell {
		return errors.New("realtime capacity")
	}
	sequence, frames, filler, err := cell.Decode(body)
	if err != nil {
		return err
	}
	useful, opens := uint64(0), uint64(0)
	for _, frame := range frames {
		if frame.Kind == cell.Auth || frame.Kind == cell.Ack || frame.Stream == 0 {
			return errors.New("realtime control")
		}
		if frame.Kind == cell.Data {
			useful += uint64(len(frame.Body))
		}
		if frame.Kind == cell.Open {
			opens++
		}
	}
	s.mu.Lock()
	if sequence != s.up || s.up == ^uint32(0) || (!s.authed && len(frames) != 0) {
		s.mu.Unlock()
		return errors.New("realtime sequence or authentication")
	}
	if err := s.peer.Receive(frames); err != nil {
		s.mu.Unlock()
		return err
	}
	s.up++
	s.last = time.Now()
	if len(frames) != 0 {
		s.ackPending, s.ackSequence = true, sequence
	}
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
	t.mu.Lock()
	t.stats.WSMessagesIn++
	t.stats.WSCellCapacities["in "+strconv.Itoa(len(body))]++
	t.stats.UploadBytes += uint64(len(body))
	t.stats.UploadFiller += uint64(filler)
	t.stats.UploadUseful += useful
	t.stats.Opens += opens
	t.mu.Unlock()
	return nil
}

func (t *Transport) writeRealtime(ctx context.Context, conn *websocket.Conn, s *session) {
	heartbeat := time.NewTimer(25 * time.Second)
	defer heartbeat.Stop()
	for {
		pressure := s.peer.Pressure()
		s.mu.Lock()
		ack := s.ackPending
		s.mu.Unlock()
		if pressure.Bytes == 0 && pressure.Controls == 0 && !ack {
			select {
			case <-ctx.Done():
				return
			case <-s.peer.Done():
				return
			case <-heartbeat.C:
			case <-s.peer.Changes():
				continue
			case <-s.wake:
				continue
			}
		} else if pressure.Bytes > 0 {
			coalesce := time.NewTimer(2 * time.Millisecond)
			select {
			case <-ctx.Done():
				coalesce.Stop()
				return
			case <-s.peer.Done():
				coalesce.Stop()
				return
			case <-coalesce.C:
			}
		}
		pressure = s.peer.Pressure()
		capacity := 512
		if pressure.Bytes >= 131072 {
			capacity = cell.MaxCell
		} else if pressure.Bytes > 0 {
			capacity = 65536
		}
		s.mu.Lock()
		if s.down == ^uint32(0) {
			s.mu.Unlock()
			return
		}
		frames := []cell.Frame{}
		budget := capacity - cell.Header
		if s.ackPending {
			frames = append(frames, cell.Frame{Kind: cell.Ack, Sequence: s.ackSequence})
			budget -= cell.FrameHeader
			s.ackPending = false
		}
		frames = append(frames, s.peer.Take(budget)...)
		body, err := cell.Encode(s.down, capacity, frames)
		if err == nil {
			s.down++
			s.last = time.Now()
		}
		s.mu.Unlock()
		if err != nil {
			return
		}
		conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
		if err := conn.WriteMessage(websocket.BinaryMessage, body); err != nil {
			return
		}
		used, useful := cell.Header, uint64(0)
		for _, frame := range frames {
			used += frame.Size()
			if frame.Kind == cell.Data {
				useful += uint64(len(frame.Body))
			}
		}
		t.mu.Lock()
		t.stats.WSMessagesOut++
		t.stats.WSCellCapacities["out "+strconv.Itoa(capacity)]++
		t.stats.DownloadBytes += uint64(len(body))
		t.stats.DownloadFiller += uint64(len(body) - used)
		t.stats.DownloadUseful += useful
		t.stats.CellCapacities[strconv.Itoa(capacity)]++
		t.mu.Unlock()
		heartbeat.Reset(25 * time.Second)
	}
}
