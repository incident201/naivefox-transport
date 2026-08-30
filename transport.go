package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
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
	"naivefox.local/transport/internal/cell"
	"naivefox.local/transport/internal/mux"
)

//go:embed site/*
var site embed.FS

func init() { caddy.RegisterModule(Transport{}) }

type Transport struct {
	Profile        string   `json:"profile,omitempty"`
	AppendMode     bool     `json:"append_mode,omitempty"`
	StatsPath      string   `json:"stats_path,omitempty"`
	Key            string   `json:"key"`
	AllowedTargets []string `json:"allowed_targets"`
	mu             sync.Mutex
	sessions       map[string]*session
	stats          counters
	stop           chan struct{}
	done           chan struct{}
}

type counters struct {
	CreditHintOpportunities uint64            `json:"credit_hint_opportunities"`
	CreditHintPromotions    uint64            `json:"credit_hint_promotions"`
	CellCapacities          map[string]uint64 `json:"cell_capacities,omitempty"`
	IdleStarted             uint64            `json:"idle_started"`
	IdleCompleted           uint64            `json:"idle_completed"`
	IdleCancelled           uint64            `json:"idle_cancelled"`
	WriteErrors             uint64            `json:"write_errors"`
	Peers                   []mux.Stats       `json:"peers,omitempty"`
	Requests                map[string]uint64 `json:"requests"`
	Protocols               map[string]uint64 `json:"protocols"`
	UploadBytes             uint64            `json:"upload_bytes"`
	DownloadBytes           uint64            `json:"download_bytes"`
	UploadFiller            uint64            `json:"upload_filler"`
	DownloadFiller          uint64            `json:"download_filler"`
	UploadUseful            uint64            `json:"upload_useful"`
	DownloadUseful          uint64            `json:"download_useful"`
	Opens                   uint64            `json:"opens"`
	Rejected                uint64            `json:"rejected"`
	Connect                 uint64            `json:"connect"`
}

type session struct {
	mu         sync.Mutex
	ip         string
	last       time.Time
	authed     bool
	appendMode bool
	up         uint32
	down       uint32
	peer       *mux.Peer
	idle       bool
	wake       chan struct{}
}

func (Transport) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "http.handlers.naivefox_transport", New: func() caddy.Module { return new(Transport) }}
}

func (t *Transport) Provision(ctx caddy.Context) error {
	if t.Profile != "" {
		if _, ok := profiles[t.Profile]; !ok {
			return errors.New("unknown application profile")
		}
	}
	if len(t.Key) < 32 || len(t.AllowedTargets) == 0 {
		return errors.New("transport requires private key and target allowlist")
	}
	for _, target := range t.AllowedTargets {
		if _, _, err := net.SplitHostPort(target); err != nil {
			return errors.New("invalid allowed target")
		}
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
			s.peer.Close()
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
		t.stats.Peers = append(t.stats.Peers, s.peer.Snapshot())
		s.peer.Close()
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
	if cookie, err := r.Cookie("app_session"); err == nil {
		if s := t.sessions[cookie.Value]; s != nil && s.ip == ip {
			s.mu.Lock()
			s.last = time.Now()
			s.mu.Unlock()
			return s, nil
		}
	}
	if r.URL.Path != "/" || len(t.sessions) >= 128 {
		return nil, errors.New("session")
	}
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return nil, err
	}
	s := &session{ip: ip, last: time.Now(), appendMode: t.AppendMode, wake: make(chan struct{}, 1)}
	s.peer = mux.New(func(ctx context.Context, target string) (net.Conn, error) {
		allowed := false
		for _, value := range t.AllowedTargets {
			if value == target {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, errors.New("target denied")
		}
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", target)
	})
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
	if strings.HasPrefix(r.URL.Path, "/__lab/") {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+t.Key)) != 1 {
			w.WriteHeader(404)
			return nil
		}
		t.mu.Lock()
		defer t.mu.Unlock()
		if r.URL.Path == "/__lab/stats" && r.Method == "GET" {
			w.Header().Set("Content-Type", "application/json")
			return json.NewEncoder(w).Encode(t.stats)
		}
		if r.URL.Path == "/__lab/sessions" && r.Method == "DELETE" {
			for id, s := range t.sessions {
				s.peer.Close()
				delete(t.sessions, id)
			}
			w.WriteHeader(204)
			return nil
		}
		w.WriteHeader(404)
		return nil
	}
	if r.Method == "CONNECT" {
		t.mu.Lock()
		t.stats.Connect++
		t.mu.Unlock()
		return next.ServeHTTP(w, r)
	}
	path := r.URL.Path
	_, asset := assetDefinition(path)
	bulk := t.appProfile().Bulk && (path == "/api/sync/bulk" || path == "/api/data/bulk")
	exchange := t.appProfile().LiveDuplex && (path == "/api/exchange/interactive" || path == "/api/exchange/download" || path == "/api/exchange/upload" || path == "/api/exchange/mixed")
	continuousPath := path == "/api/events/idle" || path == "/api/data/interactive" || path == "/api/data/download" || path == "/api/data/upload" || path == "/api/data/mixed"
	carrier := path == "/api/sync" || path == "/api/sync/media" || path == "/api/action" || path == "/api/events" || path == "/api/events/brief" || path == "/api/events/state" || strings.HasPrefix(path, "/media/chunk/") || path == "/api/upload/chunk" || (t.appProfile().Continuous && continuousPath)
	if !asset && !carrier && !exchange && !bulk {
		return next.ServeHTTP(w, r)
	}
	t.mu.Lock()
	t.stats.Requests[r.Method+" "+path]++
	t.stats.Protocols[r.Proto]++
	t.mu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if asset {
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
		}
		body, mime, err := assetBody(path, t.appProfile())
		if err != nil {
			return err
		}
		w.Header().Set("Content-Type", mime)
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, err = w.Write(body)
		return err
	}
	s, err := t.getSession(w, r)
	if err != nil {
		t.reject(w)
		return nil
	}
	if path == "/api/events/idle" {
		if r.Method != "GET" {
			t.reject(w)
			return nil
		}
		t.mu.Lock()
		t.stats.IdleStarted++
		t.mu.Unlock()
		if err := waitIdle(r.Context(), s, 30*time.Second); err != nil {
			if errors.Is(err, context.Canceled) || r.Context().Err() != nil {
				t.mu.Lock()
				t.stats.IdleCancelled++
				t.mu.Unlock()
			} else {
				t.reject(w)
			}
			return nil
		}
		err := t.downstream(w, s, 512)
		if err == nil {
			t.mu.Lock()
			t.stats.IdleCompleted++
			t.mu.Unlock()
		}
		return err
	}
	s.mu.Lock()
	if path == "/api/sync" || path == "/api/sync/media" || path == "/api/upload/chunk" || path == "/api/action" || exchange || path == "/api/sync/bulk" {
		capacity := 4096
		if path == "/api/sync/bulk" {
			capacity = 16384
		}
		if path == "/api/upload/chunk" || path == "/api/exchange/upload" || path == "/api/exchange/mixed" {
			capacity = 131072
		}
		if r.Method != "POST" || (path == "/api/action" && !t.appProfile().Commit) {
			s.mu.Unlock()
			t.reject(w)
			return nil
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, int64(2*capacity+1)))
		sequence, frames, filler, decodeErr := cell.Decode(body)
		used := 0
		for _, f := range frames {
			used += f.Size()
		}
		expected := capacity
		if s.appendMode {
			expected += used
		}
		if err != nil || decodeErr != nil || len(body) != expected || used > capacity-cell.Header || sequence != s.up {
			s.mu.Unlock()
			t.reject(w)
			return nil
		}
		useful, opens := uint64(0), uint64(0)
		if len(frames) > 0 && frames[0].Kind == cell.Auth {
			f := frames[0]
			if s.authed || f.Stream != 0 || f.Sequence != 0 || subtle.ConstantTimeCompare(f.Body, []byte(t.Key)) != 1 {
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
		if t.appProfile().Duplex || path == "/api/action" || exchange {
			down := 24576
			if path == "/api/sync/media" {
				down = t.appProfile().Down
			}
			if path == "/api/action" {
				down = 4096
			}
			if exchange {
				down = 8192
				if path == "/api/exchange/download" || path == "/api/exchange/mixed" {
					down = 65536
				}
			}
			return t.downstream(w, s, down)
		}
		w.WriteHeader(204)
		return nil
	}
	if r.Method != "GET" {
		s.mu.Unlock()
		t.reject(w)
		return nil
	}
	capacity := 24576
	if path == "/api/data/bulk" {
		capacity = 262144
	}
	if path == "/api/events/brief" {
		capacity = 8192
	}
	if path == "/api/events/state" {
		capacity = 32768
	}
	if strings.HasPrefix(path, "/media/chunk/") {
		capacity = t.appProfile().Down
	}
	if path == "/api/data/interactive" || path == "/api/data/upload" {
		capacity = 8192
	}
	if path == "/api/data/download" || path == "/api/data/mixed" {
		capacity = 65536
	}
	s.mu.Unlock()
	return t.downstream(w, s, capacity)
}

func waitIdle(ctx context.Context, s *session, timeout time.Duration) error {
	s.mu.Lock()
	if s.idle {
		s.mu.Unlock()
		return errors.New("idle poll already active")
	}
	s.idle = true
	up := s.up
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.idle = false; s.mu.Unlock() }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		pressure := s.peer.Pressure()
		s.mu.Lock()
		changed := s.up != up
		s.mu.Unlock()
		if changed || pressure.Bytes > 0 || pressure.Controls > 0 {
			return ctx.Err()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.peer.Done():
			return context.Canceled
		case <-timer.C:
			return nil
		case <-s.peer.Changes():
		case <-s.wake:
		}
	}
}

// A completed, substantially useful bulk cell may bridge one credit return.
// Backlog alone must never keep a stalled receiver in an empty bulk loop.
func downstreamState(pressure mux.Pressure, capacity int, useful uint64, preserve bool) (string, bool) {
	opportunity := capacity == 262144 && useful >= 131072 && pressure.Bytes < 32768 && pressure.Queued >= 32768
	if pressure.Bytes >= 32768 || (preserve && opportunity) {
		return "download", opportunity
	}
	if pressure.Bytes > 0 || pressure.Controls > 0 {
		return "interactive", opportunity
	}
	return "idle", opportunity
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
	base := capacity
	if s.appendMode {
		capacity += used
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
	t.stats.CellCapacities[strconv.Itoa(base)]++
	t.mu.Unlock()
	if t.appProfile().Continuous {
		pressure := s.peer.Pressure()
		preserve := t.Profile == "continuous-bulk-ready" || t.Profile == "continuous-bulk-frames"
		state, opportunity := downstreamState(pressure, base, useful, preserve)
		if opportunity {
			t.mu.Lock()
			t.stats.CreditHintOpportunities++
			if preserve {
				t.stats.CreditHintPromotions++
			}
			t.mu.Unlock()
		}
		w.Header().Set("X-App-State", state)
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("X-App-Capacity", strconv.Itoa(base))
	_, err = w.Write(body)
	if err != nil {
		t.mu.Lock()
		t.stats.WriteErrors++
		t.mu.Unlock()
	}
	return err
}

type assetSpec struct {
	file string
	size int
	mime string
}

func assetDefinition(path string) (assetSpec, bool) {
	switch path {
	case "/":
		return assetSpec{"index.html", 4096, "text/html; charset=utf-8"}, true
	case "/assets/site.css":
		return assetSpec{"site.css", 12288, "text/css"}, true
	case "/assets/app.js":
		return assetSpec{"app.js", 24576, "text/javascript"}, true
	}
	for i := 1; i <= 4; i++ {
		if path == fmt.Sprintf("/assets/image-%d.svg", i) {
			return assetSpec{"image.svg", 8192, "image/svg+xml"}, true
		}
	}
	return assetSpec{}, false
}
func assetBody(path string, profile ...appProfile) ([]byte, string, error) {
	spec, ok := assetDefinition(path)
	if !ok {
		return nil, "", errors.New("unknown asset")
	}
	body, err := site.ReadFile("site/" + spec.file)
	if err != nil {
		return nil, "", err
	}
	if path == "/assets/app.js" {
		selected := profiles["v1"]
		if len(profile) > 0 {
			selected = profile[0]
		}
		value, err := json.Marshal(selected)
		if err != nil {
			return nil, "", err
		}
		body = bytes.ReplaceAll(body, []byte("__NFC_PROFILE__"), value)
		reader, err := site.ReadFile("site/read-cell.js")
		if err != nil {
			return nil, "", err
		}
		body = bytes.ReplaceAll(body, []byte("__NFC_READER__"), reader)
		lifecycle, err := site.ReadFile("site/lifecycle.js")
		if err != nil {
			return nil, "", err
		}
		body = bytes.ReplaceAll(body, []byte("__NFC_LIFECYCLE__"), lifecycle)
	}
	if len(body) > spec.size {
		return nil, "", errors.New("asset capacity exceeded")
	}
	body = append(body, []byte(strings.Repeat(" ", spec.size-len(body)))...)
	return body, spec.mime, nil
}

var _ caddy.Provisioner = (*Transport)(nil)
var _ caddy.CleanerUpper = (*Transport)(nil)
var _ caddyhttp.MiddlewareHandler = (*Transport)(nil)
