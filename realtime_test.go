package transport

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/caddyserver/forwardproxy"
	"github.com/gorilla/websocket"
	"github.com/incident201/naivefox-transport/internal/cell"
	"github.com/incident201/naivefox-transport/internal/mux"
)

type realtimeFixture struct {
	t      *testing.T
	module *Transport
	server *httptest.Server
	cookie *http.Cookie
	up     uint32
	down   uint32
}

func newRealtimeFixture(t *testing.T) *realtimeFixture {
	t.Helper()
	module := &Transport{ApplicationRoot: testApplicationRoot(t), ForwardProxy: testForwardProxy()}
	module.ForwardProxy.ACL = []forwardproxy.ACLRule{{Subjects: []string{"127.0.0.1"}, Allow: true}}
	if err := module.Provision(testCaddyContext(t)); err != nil {
		t.Fatal(err)
	}
	next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) error { w.WriteHeader(404); return nil })
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := module.ServeHTTP(w, r, next); err != nil {
			t.Error(err)
		}
	}))
	f := &realtimeFixture{t: t, module: module, server: server}
	t.Cleanup(func() { module.Cleanup(); server.Close() })
	response, err := server.Client().Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || response.Header.Get("X-App-Realtime") != "websocket-v1" {
		t.Fatal("root handshake")
	}
	f.cookie = response.Cookies()[0]
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *realtimeFixture) request(method, path string, body []byte) (int, []byte) {
	f.t.Helper()
	r, err := http.NewRequest(method, f.server.URL+path, bytes.NewReader(body))
	if err != nil {
		f.t.Fatal(err)
	}
	r.AddCookie(f.cookie)
	response, err := f.server.Client().Do(r)
	if err != nil {
		f.t.Fatal(err)
	}
	defer response.Body.Close()
	result, err := io.ReadAll(response.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	return response.StatusCode, result
}

func (f *realtimeFixture) bootstrap(auth bool, extra []cell.Frame) {
	f.t.Helper()
	for round := 0; round < 20; round++ {
		var frames []cell.Frame
		if round == 0 {
			if auth {
				frames = append(frames, cell.Frame{Kind: cell.Auth, Body: []byte(testAuthorization)})
			}
			frames = append(frames, extra...)
		}
		body, err := cell.Encode(f.up, 4096, frames)
		if err != nil {
			f.t.Fatal(err)
		}
		if status, _ := f.request("POST", "/api/sync", body); status != 204 {
			f.t.Fatalf("startup POST %d: %d", round, status)
		}
		f.up++
		status, response := f.request("GET", startupPath(round), nil)
		seq, _, _, err := cell.Decode(response)
		if status != 200 || err != nil || seq != f.down {
			f.t.Fatalf("startup GET %d", round)
		}
		f.down++
	}
}

func (f *realtimeFixture) dial() (*websocket.Conn, *http.Response, error) {
	return f.dialProtocol(realtimeProtocol)
}

func (f *realtimeFixture) dialProtocol(protocol string) (*websocket.Conn, *http.Response, error) {
	dialer := websocket.Dialer{TLSClientConfig: f.server.Client().Transport.(*http.Transport).TLSClientConfig.Clone(), Subprotocols: []string{protocol}}
	headers := http.Header{"Origin": []string{f.server.URL}, "Cookie": []string{f.cookie.String()}}
	return dialer.Dial("wss"+strings.TrimPrefix(f.server.URL, "https")+"/api/realtime", headers)
}

func (f *realtimeFixture) send(conn *websocket.Conn, capacity int, frames []cell.Frame) uint32 {
	f.t.Helper()
	sequence := f.up
	body, err := cell.Encode(sequence, capacity, frames)
	if err != nil {
		f.t.Fatal(err)
	}
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteMessage(websocket.BinaryMessage, body); err != nil {
		f.t.Fatal(err)
	}
	f.up++
	return sequence
}

func (f *realtimeFixture) sendRealtime(conn *websocket.Conn, capacity int, hint cell.PressureHint, frames []cell.Frame) uint32 {
	f.t.Helper()
	sequence := f.up
	body, err := cell.EncodeRealtime(sequence, capacity, hint, frames)
	if err != nil {
		f.t.Fatal(err)
	}
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteMessage(websocket.BinaryMessage, body); err != nil {
		f.t.Fatal(err)
	}
	f.up++
	return sequence
}

func (f *realtimeFixture) receive(conn *websocket.Conn) []cell.Frame {
	f.t.Helper()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	kind, body, err := conn.ReadMessage()
	if err != nil {
		f.t.Fatal(err)
	}
	if kind != websocket.BinaryMessage || (len(body) != 512 && len(body) != 65536 && len(body) != 262144) {
		f.t.Fatal("unshaped websocket message")
	}
	sequence, frames, _, err := cell.Decode(body)
	if err != nil || sequence != f.down {
		f.t.Fatal("downstream cell ordering")
	}
	f.down++
	return frames
}

func TestRealtimeRequiresCompleteOrderedBootstrap(t *testing.T) {
	for _, wrong := range []bool{false, true} {
		f := newRealtimeFixture(t)
		if wrong {
			f.request("GET", "/api/events/brief", nil)
			f.down++
			f.bootstrap(true, nil)
		}
		conn, response, err := f.dial()
		if conn != nil {
			conn.Close()
		}
		if err == nil || response == nil || response.StatusCode != 400 {
			t.Fatal("early or invalid bootstrap accepted")
		}
	}
}

func TestRealtimeAsymmetricDirectionalCapacities(t *testing.T) {
	clientCases := []struct {
		local, peer cell.PressureHint
		activity    realtimeActivity
		capacity    int
	}{
		{cell.PressureIdle, cell.PressureIdle, activityIdle, 512},
		{cell.PressureInteractive, cell.PressureIdle, activityInteractive, 4096},
		{cell.PressureBulk, cell.PressureIdle, activityUpload, 131072},
		{cell.PressureIdle, cell.PressureBulk, activityDownload, 16384},
		{cell.PressureBulk, cell.PressureBulk, activityMixed, 131072},
	}
	for _, tc := range clientCases {
		activity := clientRealtimeActivity(tc.local, tc.peer)
		if activity != tc.activity || realtimeUpCapacity(activity) != tc.capacity {
			t.Fatalf("client capacity: local=%d peer=%d activity=%s capacity=%d", tc.local, tc.peer, activity, realtimeUpCapacity(activity))
		}
	}
	serverCases := []struct {
		local    cell.PressureHint
		peer     realtimeActivity
		activity realtimeActivity
		capacity int
	}{
		{cell.PressureIdle, activityIdle, activityIdle, 512},
		{cell.PressureInteractive, activityIdle, activityInteractive, 8192},
		{cell.PressureBulk, activityIdle, activityDownload, 262144},
		{cell.PressureIdle, activityUpload, activityUpload, 8192},
		{cell.PressureBulk, activityUpload, activityMixed, 65536},
		{cell.PressureIdle, activityDownload, activityDownload, 262144},
	}
	for _, tc := range serverCases {
		activity := serverRealtimeActivity(tc.local, tc.peer)
		if activity != tc.activity || realtimeDownCapacity(activity) != tc.capacity {
			t.Fatalf("server capacity: local=%d peer=%s activity=%s capacity=%d", tc.local, tc.peer, activity, realtimeDownCapacity(activity))
		}
	}
}

func TestRealtimeAsymmetricNegotiationAndHint(t *testing.T) {
	f := newRealtimeFixture(t)
	f.bootstrap(true, nil)
	conn, _, err := f.dialProtocol(realtimeAsymProtocol)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if conn.Subprotocol() != realtimeAsymProtocol || len(conn.Subprotocol()) != len(realtimeProtocol) {
		t.Fatal("asymmetric subprotocol")
	}
	f.sendRealtime(conn, 4096, cell.PressureBulk, []cell.Frame{{Kind: cell.Open, Stream: 1, Body: []byte("127.0.0.1:9")}})
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	kind, body, err := conn.ReadMessage()
	if err != nil || kind != websocket.BinaryMessage || len(body) != 8192 {
		t.Fatalf("asymmetric interactive response: bytes=%d error=%v", len(body), err)
	}
	sequence, frames, _, hint, err := cell.DecodeRealtime(body)
	if err != nil || sequence != f.down || hint > cell.PressureBulk || len(frames) == 0 {
		t.Fatal("asymmetric realtime response")
	}
	f.down++
	deadline := time.Now().Add(time.Second)
	for {
		f.module.mu.Lock()
		ready := f.module.stats.WSSubprotocols[realtimeAsymProtocol] == 1 &&
			f.module.stats.WSCellCapacities["in 4096"] == 1 &&
			f.module.stats.WSCellCapacities["out 8192"] == 1 &&
			f.module.stats.WSActivities["out interactive"] == 1 &&
			f.module.stats.WSHints["in 2"] == 1
		f.module.mu.Unlock()
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("asymmetric telemetry did not settle")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestRealtimeAsymmetricRejectsGenericCapacity(t *testing.T) {
	f := newRealtimeFixture(t)
	f.bootstrap(true, nil)
	conn, _, err := f.dialProtocol(realtimeAsymProtocol)
	if err != nil {
		t.Fatal(err)
	}
	body, err := cell.Encode(f.up, 65536, nil)
	if err != nil {
		t.Fatal(err)
	}
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteMessage(websocket.BinaryMessage, body); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("asymmetric websocket accepted generic uplink capacity")
	}
}

func TestRealtimeStreamSurvivesBootstrapAndHalfClose(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	targetDone := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			targetDone <- err
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(10 * time.Second))
		body, err := io.ReadAll(conn)
		if err == nil {
			_, err = conn.Write(body)
		}
		if err == nil {
			err = conn.(*net.TCPConn).CloseWrite()
		}
		targetDone <- err
	}()
	f := newRealtimeFixture(t)
	f.bootstrap(true, []cell.Frame{{Kind: cell.Open, Stream: 1, Body: []byte(listener.Addr().String())}})
	conn, _, err := f.dial()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if conn.Subprotocol() != realtimeProtocol {
		t.Fatal("subprotocol")
	}
	if duplicate, _, err := f.dial(); err == nil {
		duplicate.Close()
		t.Fatal("second websocket accepted")
	}
	if status, _ := f.request("GET", "/api/events/brief", nil); status != 400 {
		t.Fatal("HTTP after upgrade")
	}
	payload := bytes.Repeat([]byte("bounded-half-close"), 40000)
	credit := uint32(524288)
	lastAck := uint32(0)
	var result []byte
	finished := false
	consume := func(frames []cell.Frame) {
		for _, frame := range frames {
			switch frame.Kind {
			case cell.Ack:
				if frame.Stream != 0 || len(frame.Body) != 0 || frame.Sequence >= f.up || frame.Sequence < lastAck {
					t.Fatal("ACK contract")
				}
				lastAck = frame.Sequence
			case cell.Credit:
				credit += uint32(frame.Body[0])<<24 | uint32(frame.Body[1])<<16 | uint32(frame.Body[2])<<8 | uint32(frame.Body[3])
			case cell.Data:
				if frame.Sequence != uint32(len(result)) {
					t.Fatal("data order")
				}
				result = append(result, frame.Body...)
				f.send(conn, 512, []cell.Frame{{Kind: cell.Credit, Stream: 1, Body: cell.Uint32(uint32(len(frame.Body)))}})
			case cell.Fin:
				if frame.Sequence != uint32(len(result)) {
					t.Fatal("FIN order")
				}
				finished = true
			case cell.Opened:
			default:
				t.Fatalf("unexpected frame kind %d", frame.Kind)
			}
		}
	}
	for offset := 0; offset < len(payload); {
		n := min(64000, len(payload)-offset)
		for credit < uint32(n) {
			consume(f.receive(conn))
		}
		f.send(conn, 65536, []cell.Frame{{Kind: cell.Data, Stream: 1, Sequence: uint32(offset), Body: payload[offset : offset+n]}})
		credit -= uint32(n)
		offset += n
	}
	finSequence := f.send(conn, 512, []cell.Frame{{Kind: cell.Fin, Stream: 1, Sequence: uint32(len(payload))}})
	for !finished || lastAck < finSequence {
		consume(f.receive(conn))
	}
	if !bytes.Equal(result, payload) {
		t.Fatal("byte-exact echo")
	}
	if err := <-targetDone; err != nil {
		t.Fatal(err)
	}
	f.module.mu.Lock()
	defer f.module.mu.Unlock()
	if f.module.stats.WSOpened != 1 || f.module.stats.WSStartupMinUp != 20 || f.module.stats.WSStartupMinDown != 20 || f.module.stats.Opens != 1 {
		t.Fatal("milestone or stream reuse counters")
	}
}

func TestRealtimeRejectsInvalidCellsAndAnonymousProxyFrames(t *testing.T) {
	cases := []struct {
		name     string
		auth     bool
		capacity int
		kind     int
		frame    *cell.Frame
		sequence uint32
	}{
		{"anonymous-open", false, 512, websocket.BinaryMessage, &cell.Frame{Kind: cell.Open, Stream: 1, Body: []byte("127.0.0.1:9")}, 20},
		{"anonymous-data", false, 512, websocket.BinaryMessage, &cell.Frame{Kind: cell.Data, Stream: 1, Body: []byte("x")}, 20},
		{"anonymous-ack", false, 512, websocket.BinaryMessage, &cell.Frame{Kind: cell.Ack, Sequence: 20}, 20},
		{"re-auth", true, 512, websocket.BinaryMessage, &cell.Frame{Kind: cell.Auth, Body: []byte(testAuthorization)}, 20},
		{"client-ack", true, 512, websocket.BinaryMessage, &cell.Frame{Kind: cell.Ack, Sequence: 20}, 20},
		{"capacity", true, 4096, websocket.BinaryMessage, nil, 20},
		{"replay", true, 512, websocket.BinaryMessage, nil, 19},
		{"text", true, 512, websocket.TextMessage, nil, 20},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRealtimeFixture(t)
			f.bootstrap(tc.auth, nil)
			conn, _, err := f.dial()
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			var frames []cell.Frame
			if tc.frame != nil {
				frames = append(frames, *tc.frame)
			}
			body, err := cell.Encode(tc.sequence, tc.capacity, frames)
			if err != nil {
				t.Fatal(err)
			}
			if err := conn.WriteMessage(tc.kind, body); err != nil {
				t.Fatal(err)
			}
			conn.SetReadDeadline(time.Now().Add(time.Second))
			if _, _, err := conn.ReadMessage(); err == nil {
				t.Fatal("invalid cell accepted")
			}
			f.module.mu.Lock()
			opens := f.module.stats.Opens
			f.module.mu.Unlock()
			if opens != 0 {
				t.Fatal("invalid frame opened a target")
			}
		})
	}
}

func TestRealtimeCleanupClosesBlockedReader(t *testing.T) {
	f := newRealtimeFixture(t)
	f.bootstrap(false, nil)
	conn, _, err := f.dial()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	s := f.module.sessions[f.cookie.Value]
	s.close()
	conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("session close kept websocket reader alive")
	}
}

func TestRealtimeIdleAccountingExcludesAcknowledgements(t *testing.T) {
	peer, err := mux.NewWithWindow(nil, 524288)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	s := &session{peer: peer, wake: make(chan struct{}, 1), down: 20, ackPending: true, ackSequence: 20}
	module := &Transport{ApplicationRoot: testApplicationRoot(t), stats: counters{WSCellCapacities: make(map[string]uint64), CellCapacities: make(map[string]uint64)}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writerDone := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			close(writerDone)
			return
		}
		defer conn.Close()
		module.writeRealtime(ctx, conn, s, 10*time.Millisecond, false)
		close(writerDone)
	}))
	defer server.Close()
	dialer := websocket.Dialer{TLSClientConfig: server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()}
	conn, _, err := dialer.Dial("wss"+strings.TrimPrefix(server.URL, "https"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for index := range 3 {
		conn.SetReadDeadline(time.Now().Add(time.Second))
		kind, body, err := conn.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		sequence, frames, _, err := cell.Decode(body)
		if kind != websocket.BinaryMessage || len(body) != 512 || err != nil || sequence != uint32(20+index) {
			t.Fatal("idle application cell")
		}
		if index == 0 {
			if len(frames) != 1 || frames[0].Kind != cell.Ack {
				t.Fatal("initial ACK control")
			}
		} else if len(frames) != 0 {
			t.Fatal("idle timeout contained application frames")
		}
	}
	cancel()
	select {
	case <-writerDone:
	case <-time.After(time.Second):
		t.Fatal("idle writer did not cancel")
	}
	module.mu.Lock()
	defer module.mu.Unlock()
	if module.stats.IdleHeartbeats < 2 || module.stats.WSMessagesOut != module.stats.IdleHeartbeats+1 || module.stats.WSCellCapacities["out 512"] != module.stats.WSMessagesOut {
		t.Fatal("ACK and idle heartbeat accounting")
	}
}
