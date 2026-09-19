package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/incident201/naivefox-transport/internal/cell"
	"github.com/incident201/naivefox-transport/internal/mux"
	"go.uber.org/zap"
)

func init() { caddy.RegisterModule(Transport{}) }

type Transport struct {
	PacketCertificate string `json:"packet_certificate,omitempty"`
	PacketKey         string `json:"packet_key,omitempty"`
	packetTLS         *tls.Config
	packetAdmission   chan struct{}
	packetCount       int
	packetMu          sync.Mutex
	packets           map[string]*packetSession
	packetNonces      map[[32]byte]*packetSession
	ApplicationRoot   string       `json:"application_root,omitempty"`
	StatsPath         string       `json:"stats_path,omitempty"`
	Access            AccessConfig `json:"access"`
	MaxSessions       int          `json:"max_sessions,omitempty"`
	Diagnostics       bool         `json:"diagnostics,omitempty"`
	authHashes        [][32]byte
	policy            *tcpPolicy
	application       applicationFiles
	mu                sync.Mutex
	sessions          map[string]*session
	stats             counters
	stop              chan struct{}
	done              chan struct{}
}

type counters struct {
	PacketPeaks     [33]uint64 `json:"packet_peaks"`
	PacketOpened    uint64     `json:"packet_opened"`
	PacketUploads   uint64     `json:"packet_uploads"`
	PacketDownloads uint64     `json:"packet_downloads"`

	StartupReplays   uint64            `json:"startup_replays"`
	H3Opened         uint64            `json:"h3_opened"`
	H3Closed         uint64            `json:"h3_closed"`
	H3Uploads        uint64            `json:"h3_uploads"`
	H3Downloads      uint64            `json:"h3_downloads"`
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
}

type session struct {
	mu              sync.Mutex
	ip              string
	last            time.Time
	authed          bool
	up              uint32
	down            uint32
	peer            *mux.Peer
	wake            chan struct{}
	startupSteps    int
	startupInvalid  bool
	startupDeadline time.Time
	startupJournal  [40]*startupResult
	startupBytes    int64
	h3Active        int
	h3              bool
	packet          bool
	uploadMu        sync.Mutex
	pendingUploads  map[uint32]*pendingUpload
	realtime        bool
	realtimeConn    io.Closer
	ackPending      bool
	ackSequence     uint32
	wsStartupUp     uint32
	wsStartupDown   uint32
}

func (s *session) close() {
	s.mu.Lock()
	conn := s.realtimeConn
	s.startupInvalid = true
	s.clearStartupLocked()
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
	application, err := loadApplication(t.ApplicationRoot)
	if err != nil {
		return fmt.Errorf("load application: %w", err)
	}
	if err := t.provisionAccess(); err != nil {
		application.close()
		return err
	}
	if err := t.provisionPacket(); err != nil {
		application.close()
		return err
	}
	var retainedBytes uint64
	for _, asset := range application.assets {
		retainedBytes += uint64(len(asset.body))
	}
	ctx.Logger().Info("loaded public site",
		zap.Int("startup_resources", len(application.resources)),
		zap.Uint64("bootstrap_body_bytes", application.bodyBytes),
		zap.Uint64("retained_body_bytes", retainedBytes))
	t.application = application
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
	t.expirePackets(now)
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, s := range t.sessions {
		s.mu.Lock()
		expired := now.Sub(s.last) > 2*time.Minute || (!s.realtime && !s.startupDeadline.IsZero() && !now.Before(s.startupDeadline))
		s.mu.Unlock()
		if expired {
			s.close()
			delete(t.sessions, id)
		}
	}
}

func (t *Transport) Cleanup() (err error) {
	defer func() { err = errors.Join(err, t.application.close()) }()
	if t.stop != nil {
		close(t.stop)
		<-t.done
	}
	t.closePackets()
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, s := range t.sessions {
		peerStats := s.peer.Snapshot()
		s.mu.Lock()
		peerStats.WebSocket, peerStats.HTTP3 = s.realtime && !s.h3, s.h3
		peerStats.StartupUp, peerStats.StartupDown = s.wsStartupUp, s.wsStartupDown
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

func responseCacheControl(r *http.Request, public bool) string {
	value := "no-store"
	if public {
		value = "public, max-age=3600"
	}
	if trusted, _ := caddyhttp.GetVar(r.Context(), caddyhttp.TrustedProxyVarKey).(bool); trusted {
		value += ", no-transform"
	}
	return value
}

func sessionClientIP(r *http.Request) (string, error) {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return "", errors.New("peer")
	}
	if trusted, _ := caddyhttp.GetVar(r.Context(), caddyhttp.TrustedProxyVarKey).(bool); trusted {
		clientIP, ok := caddyhttp.GetVar(r.Context(), caddyhttp.ClientIPVarKey).(string)
		if !ok {
			return "", errors.New("trusted client IP unavailable")
		}
		ip = clientIP
	}
	address, err := netip.ParseAddr(ip)
	if err != nil {
		return "", errors.New("invalid client IP")
	}
	return address.Unmap().String(), nil
}

func (t *Transport) getSession(w http.ResponseWriter, r *http.Request) (*session, error) {
	ip, err := sessionClientIP(r)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	select {
	case <-t.stop:
		return nil, errors.New("closed transport")
	default:
	}
	if cookie, err := r.Cookie("session"); err == nil {
		if s := t.sessions[cookie.Value]; s != nil && s.ip == ip {
			s.mu.Lock()
			if s.authed && (r.ProtoMajor == 3) != s.h3 {
				s.mu.Unlock()
				return nil, errors.New("authenticated carrier protocol changed")
			}
			s.last = time.Now()
			s.mu.Unlock()
			return s, nil
		}
	}
	if r.URL.Path != "/" {
		return nil, errors.New("session")
	}
	for len(t.sessions)+t.packetCount >= t.MaxSessions {
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
	s := &session{ip: ip, last: time.Now(), wake: make(chan struct{}, 1), h3: r.ProtoMajor == 3}
	newPeer := mux.New
	if s.h3 {
		newPeer = mux.NewHTTP3
	}
	s.peer = newPeer(func(ctx context.Context, target string) (net.Conn, error) {
		s.mu.Lock()
		authed := s.authed
		s.mu.Unlock()
		if !authed {
			return nil, errors.New("unauthenticated stream")
		}
		return t.dialTransportDestination(ctx, target)
	})
	id := hex.EncodeToString(token)
	t.sessions[id] = s
	http.SetCookie(w, &http.Cookie{Name: "session", Value: id, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	return s, nil
}

func (t *Transport) reject(w http.ResponseWriter) {
	t.mu.Lock()
	t.stats.Rejected++
	t.mu.Unlock()
	w.WriteHeader(http.StatusBadRequest)
}

func (t *Transport) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	if r.URL.Path == "/api/packet" || strings.HasPrefix(r.URL.Path, "/api/packet/") {
		return t.servePacket(w, r, next)
	}
	if r.URL.Path == "/api/stream" {
		return t.h3Stream(w, r, next)
	}
	if r.URL.Path == "/api/upload" {
		return t.h3Upload(w, r, next)
	}
	if r.URL.Path == "/api/realtime" {
		return t.realtime(w, r, next)
	}
	if strings.HasPrefix(r.URL.Path, "/__lab/") {
		if !t.Diagnostics || r.URL.Path != "/__lab/stats" || r.Method != http.MethodGet || !t.authenticate([]byte(r.Header.Get("Authorization"))) {
			w.WriteHeader(404)
			return nil
		}
		t.mu.Lock()
		defer t.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", responseCacheControl(r, false))
		return json.NewEncoder(w).Encode(t.stats)
	}
	path := r.URL.Path
	_, isAsset := t.application.asset(path)
	carrier := isCarrierPath(path)
	if !isAsset && !carrier {
		if handled, err := t.application.serveStatic(w, r); handled {
			return err
		}
		return next.ServeHTTP(w, r)
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
	if isAsset {
		w.Header().Set("Cache-Control", responseCacheControl(r, false))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if path != "/" {
			w.Header().Set("Cache-Control", responseCacheControl(r, true))
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			return t.decline(w, r, next)
		}
		if path == "/" && r.Method == "GET" {
			if _, err := t.getSession(w, r); err != nil {
				t.reject(w)
				return nil
			}
		}
		asset, ok := t.application.asset(path)
		if !ok {
			return errors.New("application asset unavailable")
		}
		w.Header().Set("Content-Type", asset.mime)
		w.Header().Set("Content-Length", strconv.Itoa(len(asset.body)))
		if r.Method == "HEAD" {
			w.WriteHeader(http.StatusOK)
			return nil
		}
		_, err := w.Write(asset.body)
		return err
	}
	s, err := t.getSession(w, r)
	if err != nil {
		return t.decline(w, r, next)
	}
	s.mu.Lock()
	authed := s.authed
	s.mu.Unlock()
	if !authed && (path != "/api/sync" || r.Method != "POST") {
		return t.decline(w, r, next)
	}
	var body []byte
	var frames []cell.Frame
	var sequence uint32
	var filler int
	if path == "/api/sync" && r.Method == "POST" {
		body, err = io.ReadAll(io.LimitReader(r.Body, 4097))
		var decodeErr error
		sequence, frames, filler, decodeErr = cell.Decode(body)
		valid := err == nil && decodeErr == nil && len(body) == 4096 && r.Context().Err() == nil
		if !authed {
			valid = valid && sequence == 0 && len(frames) == 1 &&
				frames[0].Kind == cell.Auth && frames[0].Stream == 0 &&
				frames[0].Sequence == 0 && t.authenticate(frames[0].Body)
			if !valid {
				r.Body = struct {
					io.Reader
					io.Closer
				}{io.MultiReader(bytes.NewReader(body), r.Body), r.Body}
				return t.decline(w, r, next)
			}
		} else if !valid {
			t.reject(w)
			return nil
		}
	}
	return t.startup(w, r, s, body, sequence, frames, filler)
}

var _ caddy.Provisioner = (*Transport)(nil)
var _ caddy.CleanerUpper = (*Transport)(nil)
var _ caddyhttp.MiddlewareHandler = (*Transport)(nil)

func (t *Transport) decline(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	t.mu.Lock()
	t.stats.Rejected++
	t.mu.Unlock()
	return next.ServeHTTP(w, r)
}

func (t *Transport) dialTransportDestination(ctx context.Context, target string) (net.Conn, error) {
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

	return t.policy.DialContext(ctx, target)
}
