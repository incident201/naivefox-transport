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

const (
	realtimeProtocol     = "nfc1.hybrid.v1"
	realtimeAsymProtocol = "nfc1.hybrid.a1"
)

type realtimeActivity string

const (
	activityIdle        realtimeActivity = "idle"
	activityInteractive realtimeActivity = "interactive"
	activityDownload    realtimeActivity = "download"
	activityUpload      realtimeActivity = "upload"
	activityMixed       realtimeActivity = "mixed"
)

func realtimePressure(bytes int64, controls int) cell.PressureHint {
	if bytes >= 32768 {
		return cell.PressureBulk
	}
	if bytes > 0 || controls > 0 {
		return cell.PressureInteractive
	}
	return cell.PressureIdle
}

func usefulBytes(frames []cell.Frame) uint64 {
	var result uint64
	for _, frame := range frames {
		if frame.Kind == cell.Data {
			result += uint64(len(frame.Body))
		}
	}
	return result
}

func clientRealtimeActivity(local, peer cell.PressureHint) realtimeActivity {
	if local == cell.PressureBulk && peer == cell.PressureBulk {
		return activityMixed
	}
	if local == cell.PressureBulk {
		return activityUpload
	}
	if peer == cell.PressureBulk {
		return activityDownload
	}
	if local == cell.PressureInteractive || peer == cell.PressureInteractive {
		return activityInteractive
	}
	return activityIdle
}

func realtimeDownCapacity(activity realtimeActivity) int {
	switch activity {
	case activityDownload:
		return cell.MaxCell
	case activityMixed:
		return 65536
	case activityInteractive, activityUpload:
		return 8192
	default:
		return 512
	}
}

func realtimeUpCapacity(activity realtimeActivity) int {
	switch activity {
	case activityDownload:
		return 16384
	case activityUpload, activityMixed:
		return 131072
	case activityInteractive:
		return 4096
	default:
		return 512
	}
}

func realtimePeerActivity(capacity int) realtimeActivity {
	switch capacity {
	case 4096:
		return activityInteractive
	case 16384:
		return activityDownload
	case 131072:
		return activityUpload
	default:
		return activityIdle
	}
}

func realtimeActivityFromHint(hint cell.PressureHint) realtimeActivity {
	if hint == cell.PressureBulk {
		return activityUpload
	}
	if hint == cell.PressureInteractive {
		return activityInteractive
	}
	return activityIdle
}

func serverRealtimeActivity(local cell.PressureHint, peer realtimeActivity) realtimeActivity {
	if local == cell.PressureBulk && (peer == activityUpload || peer == activityMixed) {
		return activityMixed
	}
	if local == cell.PressureBulk {
		return activityDownload
	}
	if peer == activityDownload {
		return activityDownload
	}
	if peer == activityUpload || peer == activityMixed {
		return activityUpload
	}
	if local == cell.PressureInteractive || peer == activityInteractive {
		return activityInteractive
	}
	return activityIdle
}

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
	protocol := ""
	for _, offered := range websocket.Subprotocols(r) {
		if offered == realtimeAsymProtocol {
			protocol = realtimeAsymProtocol
			break
		}
		if offered == realtimeProtocol {
			protocol = realtimeProtocol
		}
	}
	s.mu.Lock()
	ready := protocol != "" && !s.realtime && !s.startupInvalid && s.startupSteps == 40 && s.up >= 20 && s.down >= 20 && s.httpActive == 0
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
		Subprotocols:   []string{protocol},
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
	if t.stats.WSSubprotocols == nil {
		t.stats.WSSubprotocols = make(map[string]uint64)
	}
	if t.stats.WSActivities == nil {
		t.stats.WSActivities = make(map[string]uint64)
	}
	if t.stats.WSHints == nil {
		t.stats.WSHints = make(map[string]uint64)
	}
	t.stats.WSSubprotocols[protocol]++
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
			if err != nil || kind != websocket.BinaryMessage || t.receiveRealtime(s, body, protocol == realtimeAsymProtocol) != nil {
				return
			}
		}
	}()
	t.writeRealtime(ctx, conn, s, 25*time.Second, protocol == realtimeAsymProtocol)
	conn.Close()
	<-readerDone
	return nil
}

func (t *Transport) receiveRealtime(s *session, body []byte, asymmetric bool) error {
	if (!asymmetric && len(body) != 512 && len(body) != 65536 && len(body) != cell.MaxCell) ||
		(asymmetric && len(body) != 512 && len(body) != 4096 && len(body) != 16384 && len(body) != 131072) {
		return errors.New("realtime capacity")
	}
	var sequence uint32
	var frames []cell.Frame
	var filler int
	var err error
	hint := cell.PressureIdle
	if asymmetric {
		sequence, frames, filler, hint, err = cell.DecodeRealtime(body)
	} else {
		sequence, frames, filler, err = cell.Decode(body)
	}
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
	if asymmetric {
		s.wsPeerHint = hint
		s.wsPeerActivity = realtimePeerActivity(len(body))
	}
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
	if asymmetric {
		t.stats.WSHints["in "+strconv.Itoa(int(hint))]++
	}
	t.stats.UploadBytes += uint64(len(body))
	t.stats.UploadFiller += uint64(filler)
	t.stats.UploadUseful += useful
	t.stats.WSUploadBytes += uint64(len(body))
	t.stats.WSUploadFiller += uint64(filler)
	t.stats.WSUploadUseful += useful
	t.stats.Opens += opens
	t.mu.Unlock()
	return nil
}

func (t *Transport) writeRealtime(ctx context.Context, conn *websocket.Conn, s *session, idleInterval time.Duration, asymmetric bool) {
	heartbeat := time.NewTimer(idleInterval)
	defer heartbeat.Stop()
	for {
		idleHeartbeat := false
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
				idleHeartbeat = true
			case <-s.peer.Changes():
				continue
			case <-s.wake:
				continue
			}
		} else if pressure.Bytes > 0 {
			capacity := cell.MaxCell
			if asymmetric {
				s.mu.Lock()
				peerActivity := s.wsPeerActivity
				s.mu.Unlock()
				capacity = realtimeDownCapacity(serverRealtimeActivity(realtimePressure(pressure.Bytes, pressure.Controls), peerActivity))
			}
			if pressure.Bytes < int64(capacity) {
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
		}
		pressure = s.peer.Pressure()
		s.mu.Lock()
		if s.down == ^uint32(0) {
			s.mu.Unlock()
			return
		}
		capacity := 512
		activity := activityIdle
		if asymmetric {
			activity = serverRealtimeActivity(realtimePressure(pressure.Bytes, pressure.Controls), s.wsPeerActivity)
			capacity = realtimeDownCapacity(activity)
		} else if pressure.Bytes >= 131072 {
			capacity = cell.MaxCell
		} else if pressure.Bytes > 0 {
			capacity = 65536
		}
		frames := []cell.Frame{}
		budget := capacity - cell.Header
		if s.ackPending {
			frames = append(frames, cell.Frame{Kind: cell.Ack, Sequence: s.ackSequence})
			budget -= cell.FrameHeader
			s.ackPending = false
		}
		frames = append(frames, s.peer.Take(budget)...)
		hint := cell.PressureIdle
		if asymmetric {
			post := s.peer.Pressure()
			state, _ := downstreamState(post, capacity, usefulBytes(frames), true)
			if state == "download" {
				hint = cell.PressureBulk
			} else if state == "interactive" {
				hint = cell.PressureInteractive
			}
			s.wsPeerActivity = realtimeActivityFromHint(s.wsPeerHint)
		}
		body, err := cell.Encode(s.down, capacity, frames)
		if asymmetric {
			body, err = cell.EncodeRealtime(s.down, capacity, hint, frames)
		}
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
		if idleHeartbeat && len(frames) == 0 {
			t.stats.IdleHeartbeats++
		}
		t.stats.WSMessagesOut++
		t.stats.WSCellCapacities["out "+strconv.Itoa(capacity)]++
		if asymmetric {
			t.stats.WSActivities["out "+string(activity)]++
			t.stats.WSHints["out "+strconv.Itoa(int(hint))]++
		}
		t.stats.DownloadBytes += uint64(len(body))
		t.stats.DownloadFiller += uint64(len(body) - used)
		t.stats.DownloadUseful += useful
		t.stats.WSDownloadBytes += uint64(len(body))
		t.stats.WSDownloadFiller += uint64(len(body) - used)
		t.stats.WSDownloadUseful += useful
		t.stats.CellCapacities[strconv.Itoa(capacity)]++
		t.mu.Unlock()
		heartbeat.Reset(idleInterval)
	}
}
