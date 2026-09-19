package transport

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/incident201/naivefox-transport/internal/cell"
	"github.com/incident201/naivefox-transport/internal/mux"
	"github.com/incident201/naivefox-transport/internal/packet"
)

const packetSetupLimit = 65536
const maxPacketSessions = 32
const maxPacketProvisional = 16

type packetSession struct {
	owner                       *Transport
	mu                          sync.Mutex
	id                          string
	nonce, beginHash, authHash  [32]byte
	created, last               time.Time
	ctx                         context.Context
	cancel                      context.CancelFunc
	raw                         *packet.Conn
	tls                         *tls.Conn
	secret                      []byte
	authed, closed, authStarted bool
	active                      int
	beginNext                   uint64
	authCursor                  uint64
	authBody, authTag           []byte
	beginReply, authReply       []byte
	beginDone, authDone         chan struct{}
	writer                      sync.Once
	state                       *session
}

func (t *Transport) provisionPacket() error {
	if t.PacketCertificate == "" && t.PacketKey == "" {
		return nil
	}
	if t.PacketCertificate == "" || t.PacketKey == "" {
		return errors.New("packet_tls requires certificate and private key")
	}
	certificate, err := tls.LoadX509KeyPair(t.PacketCertificate, t.PacketKey)
	if err != nil {
		return errors.New("load packet TLS certificate/key")
	}
	t.packetTLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, SessionTicketsDisabled: true}
	t.packetAdmission = make(chan struct{}, 64)
	t.packets = make(map[string]*packetSession)
	t.packetNonces = make(map[[32]byte]*packetSession)
	return nil
}

func (p *packetSession) close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	state := p.state
	p.authBody = nil
	p.authTag = nil
	p.beginReply = nil
	p.authReply = nil
	p.mu.Unlock()
	p.cancel()
	p.raw.Close()
	if state != nil {
		stats := state.peer.Snapshot()
		p.owner.mu.Lock()
		if stats.Opened > 0 && stats.PeakStreams <= 32 {
			p.owner.stats.PacketPeaks[stats.PeakStreams]++
		}
		p.owner.mu.Unlock()
		state.close()
	}
}

func (t *Transport) expirePackets(now time.Time) {
	t.packetMu.Lock()
	defer t.packetMu.Unlock()
	for id, p := range t.packets {
		p.mu.Lock()
		expired := p.closed || (!p.authed && now.Sub(p.created) > 30*time.Second) ||
			(p.authed && now.Sub(p.last) > 90*time.Second)
		p.mu.Unlock()
		if expired {
			p.close()
			delete(t.packets, id)
			delete(t.packetNonces, p.nonce)
			t.mu.Lock()
			t.packetCount--
			t.mu.Unlock()
		}
	}
}

func (t *Transport) closePackets() {
	t.packetMu.Lock()
	defer t.packetMu.Unlock()
	for id, p := range t.packets {
		p.close()
		delete(t.packets, id)
		delete(t.packetNonces, p.nonce)
		t.mu.Lock()
		t.packetCount--
		t.mu.Unlock()
	}
}

func packetNumber(value string) (uint64, error) {
	n, err := strconv.ParseUint(value, 10, 64)
	if err != nil || strconv.FormatUint(n, 10) != value {
		return 0, packet.ErrSequence
	}
	return n, nil
}
func packetHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store, no-transform")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Accel-Buffering", "no")
}
func packetReply(w http.ResponseWriter, r *http.Request, status int, body []byte) error {
	packetHeaders(w)
	controller := http.NewResponseController(w)
	if err := controller.SetWriteDeadline(time.Now().Add(30 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	n, err := w.Write(body)
	if err == nil && n != len(body) {
		err = io.ErrShortWrite
	}
	return err
}
func packetBody(w http.ResponseWriter, r *http.Request, limit int) ([]byte, error) {
	if r.ContentLength == 0 || r.ContentLength > int64(limit) ||
		r.Header.Get("Content-Type") != "application/octet-stream" ||
		(r.Header.Get("Content-Encoding") != "" && r.Header.Get("Content-Encoding") != "identity") {
		return nil, errors.New("packet body envelope")
	}
	controller := http.NewResponseController(w)
	if err := controller.SetReadDeadline(time.Now().Add(15 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return nil, err
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, int64(limit)+1))
	r.Body.Close()
	if err != nil || len(body) == 0 || len(body) > limit ||
		(r.ContentLength >= 0 && int64(len(body)) != r.ContentLength) {
		return nil, errors.New("packet body length")
	}
	return body, r.Context().Err()
}

func (t *Transport) servePacket(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	if t.packetTLS == nil || r.TLS == nil || (r.ProtoMajor != 1 && r.ProtoMajor != 2) {
		return t.decline(w, r, next)
	}
	select {
	case t.packetAdmission <- struct{}{}:
		defer func() { <-t.packetAdmission }()
	default:
		w.WriteHeader(http.StatusServiceUnavailable)
		return nil
	}
	if r.URL.Path == "/api/packet" && r.Method == http.MethodPost && r.URL.RawQuery == "" {
		return t.beginPacket(w, r)
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/packet/"), "/")
	if len(parts) < 2 || len(parts[0]) != 64 {
		w.WriteHeader(http.StatusNotFound)
		return nil
	}
	id := parts[0]
	decoded, err := hex.DecodeString(id)
	if err != nil || hex.EncodeToString(decoded) != id {
		w.WriteHeader(http.StatusNotFound)
		return nil
	}
	t.packetMu.Lock()
	p := t.packets[id]
	t.packetMu.Unlock()
	if p == nil {
		w.WriteHeader(http.StatusGone)
		return nil
	}
	p.mu.Lock()
	allowed := !p.closed && p.active < 12
	if allowed {
		p.active++
	}
	p.mu.Unlock()
	if !allowed {
		w.WriteHeader(http.StatusServiceUnavailable)
		return nil
	}
	defer func() { p.mu.Lock(); p.active--; p.mu.Unlock() }()
	if len(parts) == 2 && parts[1] == "auth" && r.Method == http.MethodPost && r.URL.RawQuery == "" {
		return t.authenticatePacket(w, r, p)
	}
	p.mu.Lock()
	authed := p.authed
	secret := p.secret
	p.mu.Unlock()
	if !authed {
		w.WriteHeader(http.StatusForbidden)
		return nil
	}
	if len(parts) == 3 && parts[1] == "upload" && r.Method == http.MethodPost && r.URL.RawQuery == "" {
		sequence, err := packetNumber(parts[2])
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return nil
		}
		return t.uploadPacket(w, r, p, secret, sequence)
	}
	if len(parts) == 2 && parts[1] == "download" && r.Method == http.MethodGet {
		return t.downloadPacket(w, r, p, secret)
	}
	w.WriteHeader(http.StatusNotFound)
	return nil
}

func (t *Transport) beginPacket(w http.ResponseWriter, r *http.Request) error {
	t.expirePackets(time.Now())
	body, err := packetBody(w, r, packetSetupLimit)
	if err != nil || len(body) <= 32 {
		w.WriteHeader(http.StatusBadRequest)
		return nil
	}
	var nonce [32]byte
	copy(nonce[:], body[:32])
	hash := sha256.Sum256(body)
	t.packetMu.Lock()
	select {
	case <-t.stop:
		t.packetMu.Unlock()
		w.WriteHeader(http.StatusServiceUnavailable)
		return nil
	default:
	}
	p := t.packetNonces[nonce]
	if p == nil {
		provisional := 0
		for _, candidate := range t.packets {
			candidate.mu.Lock()
			if !candidate.authed {
				provisional++
			}
			candidate.mu.Unlock()
		}
		if len(t.packets) >= min(t.MaxSessions, maxPacketSessions) || provisional >= maxPacketProvisional {
			t.packetMu.Unlock()
			w.WriteHeader(http.StatusServiceUnavailable)
			return nil
		}
		var token [32]byte
		if _, err := rand.Read(token[:]); err != nil {
			t.packetMu.Unlock()
			return err
		}
		if !t.reservePacketSession(r) {
			t.packetMu.Unlock()
			w.WriteHeader(http.StatusServiceUnavailable)
			return nil
		}
		ctx, cancel := context.WithCancel(context.Background())
		now := time.Now()
		p = &packetSession{owner: t, id: hex.EncodeToString(token[:]), nonce: nonce, beginHash: hash, created: now, last: now, ctx: ctx, cancel: cancel,
			raw: packet.New(), beginDone: make(chan struct{}), authDone: make(chan struct{})}
		p.tls = tls.Server(p.raw, t.packetTLS)
		p.raw.SetDeadline(now.Add(30 * time.Second))
		t.packets[p.id] = p
		t.packetNonces[nonce] = p
		if _, err := p.raw.Put(0, body[32:]); err != nil {
			t.packetMu.Unlock()
			p.close()
			return err
		}
		go t.runPacket(p)
		go p.prepareBegin()
	}
	t.packetMu.Unlock()
	if p.beginHash != hash {
		w.WriteHeader(http.StatusConflict)
		return nil
	}
	select {
	case <-p.beginDone:
	case <-p.ctx.Done():
		w.WriteHeader(http.StatusGone)
		return nil
	case <-r.Context().Done():
		return r.Context().Err()
	}
	p.mu.Lock()
	reply := p.beginReply
	p.mu.Unlock()
	if reply == nil {
		w.WriteHeader(http.StatusGone)
		return nil
	}
	return packetReply(w, r, http.StatusOK, reply)
}

func (t *Transport) reservePacketSession(r *http.Request) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if cookie, err := r.Cookie("session"); err == nil {
		if visitor := t.sessions[cookie.Value]; visitor != nil {
			visitor.mu.Lock()
			authed := visitor.authed
			visitor.mu.Unlock()
			if !authed {
				visitor.close()
				delete(t.sessions, cookie.Value)
			}
		}
	}
	for len(t.sessions)+t.packetCount >= t.MaxSessions {
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
			return false
		}
		t.sessions[oldestID].close()
		delete(t.sessions, oldestID)
	}
	t.packetCount++
	return true
}

func packetFlight(blocks []packet.Block) (uint64, []byte, error) {
	var body []byte
	var next uint64
	for _, block := range blocks {
		if len(block.Body) > packetSetupLimit-len(body) {
			return 0, nil, errors.New("TLS setup flight too large")
		}
		body = append(body, block.Body...)
		next = block.Sequence + 1
	}
	if len(body) == 0 {
		return 0, nil, errors.New("empty TLS flight")
	}
	return next, body, nil
}

func (p *packetSession) prepareBegin() {
	defer close(p.beginDone)
	ctx, cancel := context.WithTimeout(p.ctx, 30*time.Second)
	defer cancel()
	blocks, err := p.raw.Flight(ctx, 0)
	if err != nil {
		p.close()
		return
	}
	next, flight, err := packetFlight(blocks)
	if err != nil {
		p.close()
		return
	}
	id, _ := hex.DecodeString(p.id)
	reply := make([]byte, 44+len(flight))
	copy(reply, id)
	binary.BigEndian.PutUint64(reply[32:40], next)
	binary.BigEndian.PutUint32(reply[40:44], uint32(len(flight)))
	copy(reply[44:], flight)
	p.mu.Lock()
	p.beginNext = next
	p.beginReply = reply
	p.mu.Unlock()
}

func (t *Transport) authenticatePacket(w http.ResponseWriter, r *http.Request, p *packetSession) error {
	body, err := packetBody(w, r, packetSetupLimit)
	cursor, numberErr := packetNumber(r.Header.Get("NaiveFox-Cursor"))
	tag, tagErr := hex.DecodeString(r.Header.Get("NaiveFox-MAC"))
	if err != nil || numberErr != nil || tagErr != nil || len(tag) != 32 {
		w.WriteHeader(http.StatusBadRequest)
		return nil
	}
	hash := sha256.Sum256(body)
	p.mu.Lock()
	if p.beginReply == nil || cursor != p.beginNext {
		p.mu.Unlock()
		w.WriteHeader(http.StatusConflict)
		return nil
	}
	if p.authStarted {
		valid := p.authHash == hash && p.authCursor == cursor && string(p.authTag) == string(tag)
		p.mu.Unlock()
		if !valid {
			w.WriteHeader(http.StatusConflict)
			return nil
		}
	} else {
		p.authStarted = true
		p.authHash = hash
		p.authBody = body
		p.authTag = tag
		p.authCursor = cursor
		p.mu.Unlock()
		if _, err := p.raw.Put(1, body); err != nil {
			p.close()
			return err
		}
		go p.prepareAuth()
	}
	select {
	case <-p.authDone:
	case <-p.ctx.Done():
		w.WriteHeader(http.StatusGone)
		return nil
	case <-r.Context().Done():
		return r.Context().Err()
	}
	p.mu.Lock()
	reply := p.authReply
	p.mu.Unlock()
	if reply == nil {
		w.WriteHeader(http.StatusGone)
		return nil
	}
	return packetReply(w, r, http.StatusOK, reply)
}

func (p *packetSession) prepareAuth() {
	defer close(p.authDone)
	ctx, cancel := context.WithTimeout(p.ctx, 30*time.Second)
	defer cancel()
	p.mu.Lock()
	first := p.beginNext
	p.mu.Unlock()
	blocks, err := p.raw.Flight(ctx, first)
	if err != nil {
		p.close()
		return
	}
	next, flight, err := packetFlight(blocks)
	if err != nil {
		p.close()
		return
	}
	p.mu.Lock()
	secret := p.secret
	authed := p.authed
	p.mu.Unlock()
	if !authed {
		p.close()
		return
	}
	reply := make([]byte, 44+len(flight))
	binary.BigEndian.PutUint64(reply[:8], next)
	copy(reply[8:40], packet.MAC(secret, "auth-reply", p.id, 2, next, flight))
	binary.BigEndian.PutUint32(reply[40:44], uint32(len(flight)))
	copy(reply[44:], flight)
	p.mu.Lock()
	p.authReply = reply
	p.mu.Unlock()
}

func readPacketCell(reader io.Reader) ([]byte, error) {
	var prefix [4]byte
	if _, err := io.ReadFull(reader, prefix[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(prefix[:])
	if size != 512 && size != 4096 && size != 8192 && size != 16384 && size != 131072 {
		return nil, errors.New("packet cell capacity")
	}
	body := make([]byte, size)
	_, err := io.ReadFull(reader, body)
	return body, err
}
func writePacketCell(writer io.Writer, body []byte) error {
	// A separate length-prefix write creates another TLS record and flush.
	framed := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(framed[:4], uint32(len(body)))
	copy(framed[4:], body)
	n, err := writer.Write(framed)
	if err == nil && n != len(framed) {
		return io.ErrShortWrite
	}
	return err
}

func (t *Transport) runPacket(p *packetSession) {
	defer p.close()
	ctx, cancel := context.WithTimeout(p.ctx, 30*time.Second)
	err := p.tls.HandshakeContext(ctx)
	cancel()
	if err != nil {
		return
	}
	secret, err := packet.Secret(p.tls.ConnectionState())
	if err != nil {
		return
	}
	body, err := readPacketCell(p.tls)
	if err != nil || len(body) != 4096 {
		return
	}
	sequence, frames, _, err := cell.Decode(body)
	if err != nil || sequence != 0 || len(frames) != 1 || frames[0].Kind != cell.Auth || frames[0].Stream != 0 || frames[0].Sequence != 0 {
		return
	}
	p.mu.Lock()
	valid := p.authStarted && packet.Verify(secret, "auth", p.id, 1, p.authCursor, p.authBody, p.authTag)
	p.mu.Unlock()
	if !valid || !t.authenticate(frames[0].Body) {
		return
	}
	if err := p.raw.Ack(p.authCursor); err != nil {
		return
	}
	state := &session{authed: true, packet: true, realtime: true, up: 1, down: 1, wake: make(chan struct{}, 1), last: time.Now()}
	state.peer = mux.NewHTTP3(t.dialTransportDestination)
	state.realtimeConn = cancelCloser{p.cancel}
	hello, err := cell.Encode(0, 512, []cell.Frame{{Kind: cell.Hello, Body: []byte(transportIdentity + "\n" + t.application.identity + "\ncdn\n" + p.id)}})
	if err != nil {
		state.close()
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		state.close()
		return
	}
	p.secret = secret
	p.authed = true
	p.state = state
	p.last = time.Now()
	p.mu.Unlock()
	if err := writePacketCell(p.tls, hello); err != nil {
		return
	}
	p.raw.SetDeadline(time.Time{})
	t.mu.Lock()
	t.stats.PacketOpened++
	t.mu.Unlock()
	for {
		body, err := readPacketCell(p.tls)
		if err != nil || t.receiveRealtime(state, body) != nil {
			return
		}
	}
}

func (t *Transport) uploadPacket(w http.ResponseWriter, r *http.Request, p *packetSession, secret []byte, sequence uint64) error {
	body, err := packetBody(w, r, packet.MaxBlock)
	cursor, numberErr := packetNumber(r.Header.Get("NaiveFox-Cursor"))
	tag, tagErr := hex.DecodeString(r.Header.Get("NaiveFox-MAC"))
	if err != nil || numberErr != nil || tagErr != nil || !packet.Verify(secret, "upload", p.id, sequence, cursor, body, tag) {
		w.WriteHeader(http.StatusForbidden)
		return nil
	}
	if err := p.raw.Ack(cursor); err != nil {
		p.close()
		w.WriteHeader(http.StatusGone)
		return nil
	}
	previous := p.raw.Uploaded()
	next, err := p.raw.Put(sequence, body)
	if errors.Is(err, packet.ErrFull) {
		w.WriteHeader(http.StatusServiceUnavailable)
		return nil
	}
	if errors.Is(err, packet.ErrGone) {
		w.WriteHeader(http.StatusGone)
		return nil
	}
	if err != nil {
		p.close()
		w.WriteHeader(http.StatusConflict)
		return nil
	}
	p.mu.Lock()
	if next > previous {
		p.last = time.Now()
	}
	p.mu.Unlock()
	reply := make([]byte, 48)
	binary.BigEndian.PutUint64(reply[:8], next)
	binary.BigEndian.PutUint64(reply[8:16], sequence)
	copy(reply[16:], packet.MAC(secret, "upload-reply", p.id, next, sequence, body))
	return packetReply(w, r, http.StatusOK, reply)
}

func (t *Transport) downloadPacket(w http.ResponseWriter, r *http.Request, p *packetSession, secret []byte) error {
	query := r.URL.Query()
	if len(query) != 2 || len(query["generation"]) != 1 || len(query["cursor"]) != 1 {
		w.WriteHeader(http.StatusBadRequest)
		return nil
	}
	generation, e1 := packetNumber(query.Get("generation"))
	cursor, e2 := packetNumber(query.Get("cursor"))
	tag, e3 := hex.DecodeString(r.Header.Get("NaiveFox-MAC"))
	if e1 != nil || e2 != nil || e3 != nil || !packet.Verify(secret, "download", p.id, generation, cursor, nil, tag) {
		w.WriteHeader(http.StatusForbidden)
		return nil
	}
	if err := p.raw.Attach(generation, cursor); err != nil {
		if errors.Is(err, packet.ErrGone) {
			p.close()
			w.WriteHeader(http.StatusGone)
		} else {
			w.WriteHeader(http.StatusConflict)
		}
		return nil
	}
	p.mu.Lock()
	p.last = time.Now()
	state := p.state
	p.mu.Unlock()
	p.writer.Do(func() {
		go func() {
			defer p.close()
			t.writeCells(p.ctx, state, 5*time.Second, func(body []byte) error { return writePacketCell(p.tls, body) })
		}()
	})
	packetHeaders(w)
	w.WriteHeader(http.StatusOK)
	controller := http.NewResponseController(w)
	if err := controller.Flush(); err != nil {
		return err
	}
	for {
		block, err := p.raw.Next(r.Context(), generation, cursor)
		if err != nil {
			return nil
		}
		if err := controller.SetWriteDeadline(time.Now().Add(30 * time.Second)); err != nil {
			return err
		}
		envelope := make([]byte, 44+len(block.Body))
		binary.BigEndian.PutUint64(envelope[:8], block.Sequence)
		binary.BigEndian.PutUint32(envelope[8:12], uint32(len(block.Body)))
		copy(envelope[12:44], packet.MAC(secret, "down", p.id, block.Sequence, 0, block.Body))
		copy(envelope[44:], block.Body)
		if n, err := w.Write(envelope); err != nil {
			return err
		} else if n != len(envelope) {
			return io.ErrShortWrite
		}
		if err := controller.Flush(); err != nil {
			return err
		}
		cursor++
	}
}

func waitPacketBatch(ctx context.Context, s *session) bool {
	timer := time.NewTimer(2 * time.Millisecond)
	defer timer.Stop()
	for {
		pressure := s.peer.PressureWithCreditFloor(realtimeCreditFloor)
		if realtimeFramedBytes(pressure) >= int64(s.maxDownCell()) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-s.peer.Done():
			return false
		case <-timer.C:
			return true
		case <-s.peer.Changes():
		case <-s.wake:
		}
	}
}
