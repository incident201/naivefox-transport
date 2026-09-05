package transport

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/caddyserver/forwardproxy"
	"github.com/incident201/naivefox-transport/internal/cell"
	"github.com/incident201/naivefox-transport/internal/mux"
)

func init() { caddy.RegisterModule(Transport{}) }

type Transport struct {
	ApplicationRoot string                `json:"application_root,omitempty"`
	Profile         string                `json:"profile,omitempty"`
	StatsPath       string                `json:"stats_path,omitempty"`
	ForwardProxy    *forwardproxy.Handler `json:"forward_proxy"`
	MaxSessions     int                   `json:"max_sessions,omitempty"`
	Diagnostics     bool                  `json:"diagnostics,omitempty"`
	authHashes      [][32]byte
	policy          *tcpPolicy
	// Retain old field names only to reject migrations explicitly, including
	// empty values. They never authorize a session or limit destinations.
	LegacyKey     json.RawMessage `json:"key,omitempty"`
	LegacyTargets json.RawMessage `json:"allowed_targets,omitempty"`
	application   applicationFiles
	mu            sync.Mutex
	sessions      map[string]*session
	stats         counters
	stop          chan struct{}
	done          chan struct{}
}

type counters struct {
	WSOpened         uint64            `json:"ws_opened"`
	WSClosed         uint64            `json:"ws_closed"`
	WSMessagesIn     uint64            `json:"ws_messages_in"`
	WSMessagesOut    uint64            `json:"ws_messages_out"`
	WSCellCapacities map[string]uint64 `json:"ws_cell_capacities,omitempty"`
	WSSubprotocols   map[string]uint64 `json:"ws_subprotocols,omitempty"`
	WSUploadBytes    uint64            `json:"ws_upload_bytes"`
	WSDownloadBytes  uint64            `json:"ws_download_bytes"`
	WSUploadFiller   uint64            `json:"ws_upload_filler"`
	WSDownloadFiller uint64            `json:"ws_download_filler"`
	WSUploadUseful   uint64            `json:"ws_upload_useful"`
	WSDownloadUseful uint64            `json:"ws_download_useful"`
	WSStartupMinUp   uint32            `json:"ws_startup_min_up"`
	WSStartupMinDown uint32            `json:"ws_startup_min_down"`
	StartupCompleted uint64            `json:"startup_completed"`
	IdleHeartbeats   uint64            `json:"idle_heartbeats"`
	CellCapacities   map[string]uint64 `json:"cell_capacities,omitempty"`
	WriteErrors      uint64            `json:"write_errors"`
	Peers            []mux.Stats       `json:"peers,omitempty"`
	Requests         map[string]uint64 `json:"requests"`
	Protocols        map[string]uint64 `json:"protocols"`
	UploadBytes      uint64            `json:"upload_bytes"`
	DownloadBytes    uint64            `json:"download_bytes"`
	UploadFiller     uint64            `json:"upload_filler"`
	DownloadFiller   uint64            `json:"download_filler"`
	UploadUseful     uint64            `json:"upload_useful"`
	DownloadUseful   uint64            `json:"download_useful"`
	Opens            uint64            `json:"opens"`
	Rejected         uint64            `json:"rejected"`
	Connect          uint64            `json:"connect"`
}

type session struct {
	mu             sync.Mutex
	ip             string
	last           time.Time
	authed         bool
	up             uint32
	down           uint32
	peer           *mux.Peer
	wake           chan struct{}
	startupSteps   int
	startupInvalid bool
	httpActive     int
	realtime       bool
	realtimeConn   io.Closer
	ackPending     bool
	ackSequence    uint32
	wsStartupUp    uint32
	wsStartupDown  uint32
}

func (s *session) close() {
	s.mu.Lock()
	conn := s.realtimeConn
	s.mu.Unlock()
	if conn != nil {
		conn.Close()
	}
	s.peer.Close()
}

func (Transport) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "http.handlers.naivefox_transport", New: func() caddy.Module { return new(Transport) }}
}

func (t *Transport) Provision(ctx caddy.Context) error {
	if t.MaxSessions < 0 {
		return errors.New("max_sessions must be positive")
	}
	if t.MaxSessions == 0 {
		t.MaxSessions = 128
	}
	if t.Profile != "" && t.Profile != defaultProfile {
		return errors.New("unsupported application profile")
	}
	if t.LegacyKey != nil || t.LegacyTargets != nil {
		return errors.New("key and allowed_targets were removed; move forward_proxy inside naivefox_transport and configure basic_auth once for both transports")
	}
	application, err := loadApplication(t.ApplicationRoot)
	if err != nil {
		return fmt.Errorf("load application: %w", err)
	}
	t.application = application
	if err := t.provisionForwardProxy(ctx); err != nil {
		return err
	}
	t.sessions = make(map[string]*session)
	t.stats = counters{Requests: make(map[string]uint64), Protocols: make(map[string]uint64), CellCapacities: make(map[string]uint64)}
	t.stop = make(chan struct{})
	t.done = make(chan struct{})
	go func() {
		defer close(t.done)
		tick := time.NewTicker(15 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-t.stop:
				return
			case now := <-tick.C:
				t.expire(now)
			}
		}
	}()
	return nil
}

func (t *Transport) expire(now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, s := range t.sessions {
		s.mu.Lock()
		expired := now.Sub(s.last) > 2*time.Minute
		s.mu.Unlock()
		if expired {
			s.close()
			delete(t.sessions, id)
		}
	}
}

func (t *Transport) Cleanup() error {
	if t.stop != nil {
		close(t.stop)
		<-t.done
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, s := range t.sessions {
		peerStats := s.peer.Snapshot()
		s.mu.Lock()
		peerStats.WebSocket, peerStats.StartupUp, peerStats.StartupDown = s.realtime, s.wsStartupUp, s.wsStartupDown
		s.mu.Unlock()
		t.stats.Peers = append(t.stats.Peers, peerStats)
		s.close()
		delete(t.sessions, id)
	}
	if t.StatsPath != "" {
		body, err := json.Marshal(t.stats)
		if err != nil {
			return err
		}
		return os.WriteFile(t.StatsPath, body, 0600)
	}
	return nil
}

func (t *Transport) getSession(w http.ResponseWriter, r *http.Request) (*session, error) {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return nil, errors.New("peer")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	select {
	case <-t.stop:
		return nil, errors.New("closed transport")
	default:
	}
	if cookie, err := r.Cookie("app_session"); err == nil {
		if s := t.sessions[cookie.Value]; s != nil && s.ip == ip {
			s.mu.Lock()
			s.last = time.Now()
			s.mu.Unlock()
			return s, nil
		}
	}
	if r.URL.Path != "/" {
		return nil, errors.New("session")
	}
	for len(t.sessions) >= t.MaxSessions {
		// Anonymous page visits must not reserve every slot for the full TTL.
		// Evict the oldest unauthenticated session, never an authenticated one.
		var oldestID string
		var oldest time.Time
		for id, candidate := range t.sessions {
			candidate.mu.Lock()
			if !candidate.authed && (oldestID == "" || candidate.last.Before(oldest)) {
				oldestID, oldest = id, candidate.last
			}
			candidate.mu.Unlock()
		}
		if oldestID == "" {
			return nil, errors.New("authenticated session capacity reached")
		}
		candidate := t.sessions[oldestID]
		candidate.mu.Lock()
		if !candidate.authed {
			candidate.peer.Close()
			delete(t.sessions, oldestID)
		}
		candidate.mu.Unlock()
	}
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return nil, err
	}
	s := &session{ip: ip, last: time.Now(), wake: make(chan struct{}, 1)}
	s.peer, err = mux.NewWithWindow(func(ctx context.Context, target string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(target)
		if err != nil || host == "" || strings.ContainsAny(host, "\x00\r\n\t /?#@") || port == "" {
			return nil, errors.New("invalid TCP destination")
		}
		for _, digit := range port {
			if digit < '0' || digit > '9' {
				return nil, errors.New("invalid TCP destination port")
			}
		}
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return nil, errors.New("invalid TCP destination port")
		}
		s.mu.Lock()
		authed := s.authed
		s.mu.Unlock()
		if !authed {
			return nil, errors.New("unauthenticated stream")
		}
		return t.policy.DialContext(ctx, target)
	}, 512*1024)
	if err != nil {
		return nil, err
	}
	id := hex.EncodeToString(token)
	t.sessions[id] = s
	http.SetCookie(w, &http.Cookie{Name: "app_session", Value: id, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	return s, nil
}

func (t *Transport) reject(w http.ResponseWriter) {
	t.mu.Lock()
	t.stats.Rejected++
	t.mu.Unlock()
	w.WriteHeader(http.StatusBadRequest)
}

func (t *Transport) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	if r.URL.Path == "/api/realtime" {
		return t.realtime(w, r)
	}
	if strings.HasPrefix(r.URL.Path, "/__lab/") {
		if !t.Diagnostics || r.URL.Path != "/__lab/stats" || r.Method != http.MethodGet || !t.authenticate([]byte(r.Header.Get("Authorization"))) {
			w.WriteHeader(404)
			return nil
		}
		t.mu.Lock()
		defer t.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		return json.NewEncoder(w).Encode(t.stats)
	}
	if r.Method == "CONNECT" {
		t.mu.Lock()
		t.stats.Connect++
		t.mu.Unlock()
		return t.ForwardProxy.ServeHTTP(w, r, next)
	}
	path := r.URL.Path
	_, isAsset := t.application.asset(path)
	carrier := path == "/api/sync" || path == "/api/events/brief" ||
		path == "/api/events/state" || strings.HasPrefix(path, "/media/chunk/")
	if !isAsset && !carrier {
		return t.ForwardProxy.ServeHTTP(w, r, next)
	}
	methodLabel, pathLabel, protocolLabel := r.Method, path, r.Proto
	if methodLabel != "GET" && methodLabel != "POST" {
		methodLabel = "OTHER"
	}
	if strings.HasPrefix(pathLabel, "/media/chunk/") {
		pathLabel = "/media/chunk/*"
	}
	switch protocolLabel {
	case "HTTP/1.0", "HTTP/1.1", "HTTP/2.0", "HTTP/3.0":
	default:
		protocolLabel = "OTHER"
	}
	t.mu.Lock()
	t.stats.Requests[methodLabel+" "+pathLabel]++
	t.stats.Protocols[protocolLabel]++
	t.mu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if isAsset {
		if path != "/" {
			w.Header().Set("Cache-Control", "public, max-age=3600")
		}
		if r.Method != "GET" {
			t.reject(w)
			return nil
		}
		if path == "/" {
			if _, err := t.getSession(w, r); err != nil {
				t.reject(w)
				return nil
			}
			// The native client must reject a different profile before sending
			// authentication or opening streams: receive windows are not negotiated.
			w.Header().Set("X-App-Profile", defaultProfile)
			w.Header().Set("X-App-Auth", "basic")
			w.Header().Set("X-App-Realtime", "websocket-v1")
		}
		asset, ok := t.application.asset(path)
		if !ok {
			return errors.New("application asset unavailable")
		}
		w.Header().Set("Content-Type", asset.mime)
		w.Header().Set("Content-Length", strconv.Itoa(len(asset.body)))
		_, err := w.Write(asset.body)
		return err
	}
	s, err := t.getSession(w, r)
	if err != nil {
		t.reject(w)
		return nil
	}
	observer, ok := s.beginHTTP(w, r)
	if !ok {
		t.reject(w)
		return nil
	}
	w = observer
	defer t.finishHTTP(s, observer, r)
	if path == "/api/sync" {
		const capacity = 4096
		if r.Method != "POST" {
			t.reject(w)
			return nil
		}
		if r.Context().Err() != nil {
			t.reject(w)
			return nil
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, int64(capacity+1)))
		sequence, frames, filler, decodeErr := cell.Decode(body)
		used := 0
		for _, f := range frames {
			used += f.Size()
		}
		if err != nil || decodeErr != nil || len(body) != capacity || used > capacity-cell.Header {
			t.reject(w)
			return nil
		}
		// Body reads belong to this request, never to the shared session lock.
		// Expiry and cleanup must remain able to close a stalled upload's peer.
		s.mu.Lock()
		if r.Context().Err() != nil || sequence != s.up {
			s.mu.Unlock()
			t.reject(w)
			return nil
		}
		select {
		case <-s.peer.Done():
			s.mu.Unlock()
			t.reject(w)
			return nil
		default:
		}
		useful, opens := uint64(0), uint64(0)
		if len(frames) > 0 && frames[0].Kind == cell.Auth {
			f := frames[0]
			if s.authed || f.Stream != 0 || f.Sequence != 0 || !t.authenticate(f.Body) {
				s.mu.Unlock()
				t.reject(w)
				return nil
			}
			s.authed = true
			frames = frames[1:]
		}
		if len(frames) > 0 && !s.authed {
			s.mu.Unlock()
			t.reject(w)
			return nil
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
			s.mu.Unlock()
			t.reject(w)
			return nil
		}
		s.up++
		select {
		case s.wake <- struct{}{}:
		default:
		}
		s.mu.Unlock()
		t.mu.Lock()
		t.stats.UploadBytes += uint64(len(body))
		t.stats.UploadFiller += uint64(filler)
		t.stats.UploadUseful += useful
		t.stats.Opens += opens
		t.mu.Unlock()
		w.WriteHeader(204)
		return nil
	}
	if r.Method != "GET" {
		t.reject(w)
		return nil
	}
	s.mu.Lock()
	round := int(s.down)
	s.mu.Unlock()
	if round >= len(startupSlots) || path != startupPath(round) {
		t.reject(w)
		return nil
	}
	return t.downstream(w, s, startupSlots[round])
}

func (t *Transport) downstream(w http.ResponseWriter, s *session, capacity int) error {
	s.mu.Lock()
	frames := s.peer.Take(capacity - cell.Header)
	used, useful := 0, uint64(0)
	for _, f := range frames {
		used += f.Size()
		if f.Kind == cell.Data {
			useful += uint64(len(f.Body))
		}
	}
	body, err := cell.Encode(s.down, capacity, frames)
	s.down++
	s.mu.Unlock()
	if err != nil {
		return err
	}
	t.mu.Lock()
	t.stats.DownloadBytes += uint64(len(body))
	t.stats.DownloadFiller += uint64(len(body) - cell.Header - used)
	t.stats.DownloadUseful += useful
	t.stats.CellCapacities[strconv.Itoa(capacity)]++
	t.mu.Unlock()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("X-App-Capacity", strconv.Itoa(capacity))
	_, err = w.Write(body)
	if err != nil {
		t.mu.Lock()
		t.stats.WriteErrors++
		t.mu.Unlock()
	}
	return err
}

var _ caddy.Provisioner = (*Transport)(nil)
var _ caddy.CleanerUpper = (*Transport)(nil)
var _ caddyhttp.MiddlewareHandler = (*Transport)(nil)
