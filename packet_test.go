package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/incident201/naivefox-transport/internal/cell"
	"github.com/incident201/naivefox-transport/internal/packet"
)

type packetTestClient struct {
	t               *testing.T
	server          *httptest.Server
	module          *Transport
	inner           *tls.Conn
	raw             *packet.Conn
	secret          []byte
	id              string
	up, down, input uint64
	ctx             context.Context
	stop            context.CancelFunc
	received        chan []cell.Frame
	errors          chan error
	downMu          sync.Mutex
}

func newPacketTest(t *testing.T, h2 bool) *packetTestClient {
	t.Helper()
	cert, key, roots := testCertificate(t, t.TempDir())
	module := &Transport{ApplicationRoot: testApplicationRoot(t), Access: testAccess(), PacketCertificate: cert, PacketKey: key}
	module.Access.ACL = []ACLRule{{Subjects: []string{"127.0.0.1"}, Allow: true}}
	if err := module.Provision(testCaddyContext(t)); err != nil {
		t.Fatal(err)
	}
	next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) error { w.WriteHeader(404); return nil })
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Deliberately alternate intermediary addresses; neither is authenticated.
		r.RemoteAddr = fmt.Sprintf("192.0.2.%d:1234", 1+len(r.URL.Path)%2)
		if err := module.ServeHTTP(w, r, next); err != nil && r.Context().Err() == nil {
			t.Logf("packet response ended: %v", err)
		}
	}))
	server.EnableHTTP2 = h2
	server.StartTLS()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	client := &packetTestClient{t: t, server: server, module: module, raw: packet.New(), ctx: ctx, stop: cancel, received: make(chan []cell.Frame, 64), errors: make(chan error, 1)}
	client.raw.SetDeadline(time.Now().Add(15 * time.Second))
	client.inner = tls.Client(client.raw, &tls.Config{RootCAs: roots, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, SessionTicketsDisabled: true})
	t.Cleanup(func() { cancel(); client.raw.Close(); module.Cleanup(); server.Close() })
	return client
}

func (c *packetTestClient) request(path string, body []byte, cursor uint64, tag []byte) (int, []byte) {
	c.t.Helper()
	request, err := http.NewRequestWithContext(c.ctx, http.MethodPost, c.server.URL+path, bytes.NewReader(body))
	if err != nil {
		c.t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	if tag != nil {
		request.Header.Set("NaiveFox-Cursor", strconv.FormatUint(cursor, 10))
		request.Header.Set("NaiveFox-MAC", hex.EncodeToString(tag))
	}
	response, err := c.server.Client().Do(request)
	if err != nil {
		if c.ctx.Err() != nil {
			return 0, nil
		}
		c.t.Fatal(err)
	}
	defer response.Body.Close()
	result, err := io.ReadAll(response.Body)
	if err != nil {
		if c.ctx.Err() != nil {
			return 0, nil
		}
		c.t.Fatal(err)
	}
	return response.StatusCode, result
}

func (c *packetTestClient) flight(next uint64) (uint64, []byte) {
	c.t.Helper()
	blocks, err := c.raw.Flight(c.ctx, next)
	if err != nil {
		c.t.Fatal(err)
	}
	end, body, err := packetFlight(blocks)
	if err != nil {
		c.t.Fatal(err)
	}
	return end, body
}

func (c *packetTestClient) bootstrap() {
	c.t.Helper()
	handshake := make(chan error, 1)
	go func() {
		if err := c.inner.HandshakeContext(c.ctx); err != nil {
			handshake <- err
			return
		}
		secret, err := packet.Secret(c.inner.ConnectionState())
		if err != nil {
			handshake <- err
			return
		}
		c.secret = secret
		auth, _ := cell.Encode(0, 4096, []cell.Frame{{Kind: cell.Auth, Body: []byte(testAuthorization)}})
		if err := writePacketCell(c.inner, auth); err != nil {
			handshake <- err
			return
		}
		handshake <- nil
		for sequence := uint32(0); ; sequence++ {
			body, err := readPacketDownCell(c.inner)
			if err != nil {
				select {
				case c.errors <- err:
				default:
				}
				return
			}
			seq, frames, _, err := cell.Decode(body)
			if err != nil || seq != sequence {
				select {
				case c.errors <- fmt.Errorf("inner sequence %d expected %d: %v", seq, sequence, err):
				default:
				}
				return
			}
			select {
			case c.received <- frames:
			case <-c.ctx.Done():
				return
			}
		}
	}()
	next, flight := c.flight(0)
	begin := make([]byte, 32+len(flight))
	rand.Read(begin[:32])
	copy(begin[32:], flight)
	status, response := c.request("/api/packet", begin, 0, nil)
	if status != 200 || len(response) < 44 {
		c.t.Fatal("begin", status)
	}
	status, again := c.request("/api/packet", begin, 0, nil)
	if status != 200 || !bytes.Equal(response, again) {
		c.t.Fatal("begin retry changed server flight")
	}
	c.id = hex.EncodeToString(response[:32])
	c.down = binary.BigEndian.Uint64(response[32:40])
	if int(binary.BigEndian.Uint32(response[40:44])) != len(response)-44 {
		c.t.Fatal("begin length")
	}
	if _, err := c.raw.Put(0, response[44:]); err != nil {
		c.t.Fatal(err)
	}
	c.raw.Ack(next)
	select {
	case err := <-handshake:
		if err != nil {
			c.t.Fatal(err)
		}
	case <-c.ctx.Done():
		c.t.Fatal("client handshake timeout")
	}
	last, auth := c.flight(next)
	tag := packet.MAC(c.secret, "auth", c.id, 1, c.down, auth)
	status, response = c.request("/api/packet/"+c.id+"/auth", auth, c.down, tag)
	if status != 200 || len(response) < 44 {
		c.t.Fatal("auth", status)
	}
	status, again = c.request("/api/packet/"+c.id+"/auth", auth, c.down, tag)
	if status != 200 || !bytes.Equal(response, again) {
		c.t.Fatal("lost auth response is not replayable")
	}
	c.down = binary.BigEndian.Uint64(response[:8])
	if !packet.Verify(c.secret, "auth-reply", c.id, 2, c.down, response[44:], response[8:40]) {
		c.t.Fatal("auth reply MAC")
	}
	if _, err := c.raw.Put(1, response[44:]); err != nil {
		c.t.Fatal(err)
	}
	c.raw.Ack(last)
	c.input = 2
	c.up = 2
	frames := c.take()
	if len(frames) != 1 || frames[0].Kind != cell.Hello ||
		string(frames[0].Body) != "naivefox\n"+c.module.application.identity+"\ncdn\n"+c.id {
		c.t.Fatal("encrypted carrier confirmation")
	}
	if bytes.Contains(begin, []byte(testAuthorization)) || bytes.Contains(auth, []byte(testAuthorization)) {
		c.t.Fatal("plaintext auth visible")
	}
	if err := c.raw.Attach(1, last); err != nil {
		c.t.Fatal(err)
	}
	go func() {
		for cursor := last; ; cursor++ {
			block, err := c.raw.Next(c.ctx, 1, cursor)
			if err != nil {
				return
			}
			c.downMu.Lock()
			down := c.down
			c.downMu.Unlock()
			tag := packet.MAC(c.secret, "upload", c.id, c.up, down, block.Body)
			status, reply := c.request("/api/packet/"+c.id+"/upload/"+strconv.FormatUint(c.up, 10), block.Body, down, tag)
			if c.ctx.Err() != nil {
				return
			}
			if status != 200 || len(reply) != 48 {
				c.errors <- fmt.Errorf("upload status %d", status)
				return
			}
			acknowledged := binary.BigEndian.Uint64(reply[:8])
			if acknowledged != c.up+1 || !packet.Verify(c.secret, "upload-reply", c.id, acknowledged, c.up, block.Body, reply[16:]) {
				c.errors <- fmt.Errorf("upload reply")
				return
			}
			// A lost response is retried with precisely the same ciphertext and MAC.
			status, duplicate := c.request("/api/packet/"+c.id+"/upload/"+strconv.FormatUint(c.up, 10), block.Body, down, tag)
			if c.ctx.Err() != nil {
				return
			}
			if status != 200 || !bytes.Equal(reply, duplicate) {
				c.errors <- fmt.Errorf("duplicate upload")
				return
			}
			c.up++
			c.raw.Ack(cursor + 1)
		}
	}()
}

func readPacketDownCell(reader io.Reader) ([]byte, error) {
	var prefix [4]byte
	if _, err := io.ReadFull(reader, prefix[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(prefix[:])
	if size != 512 && size != 8192 && size != 65536 {
		return nil, fmt.Errorf("down cell capacity %d", size)
	}
	body := make([]byte, size)
	_, err := io.ReadFull(reader, body)
	return body, err
}

func (c *packetTestClient) take() []cell.Frame {
	c.t.Helper()
	select {
	case frames := <-c.received:
		return frames
	case err := <-c.errors:
		c.t.Fatal(err)
	case <-c.ctx.Done():
		c.t.Fatal("packet timeout")
	}
	return nil
}

func (c *packetTestClient) download(generation uint64) *http.Response {
	c.t.Helper()
	c.downMu.Lock()
	cursor := c.down
	c.downMu.Unlock()
	path := fmt.Sprintf("/api/packet/%s/download?generation=%d&cursor=%d", c.id, generation, cursor)
	request, _ := http.NewRequestWithContext(c.ctx, http.MethodGet, c.server.URL+path, nil)
	request.Header.Set("NaiveFox-MAC", hex.EncodeToString(packet.MAC(c.secret, "download", c.id, generation, cursor, nil)))
	response, err := c.server.Client().Do(request)
	if err != nil {
		c.t.Fatal(err)
	}
	if response.StatusCode != 200 {
		response.Body.Close()
		c.t.Fatal("download", response.StatusCode)
	}
	return response
}

func TestPacketTLSHTTPResumeAndMux(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		t.Run(fmt.Sprintf("h2=%v", h2), func(t *testing.T) {
			c := newPacketTest(t, h2)
			c.bootstrap()
			first := c.download(1)
			var prefix [44]byte
			if _, err := io.ReadFull(first.Body, prefix[:]); err != nil {
				t.Fatal(err)
			}
			part := make([]byte, int(binary.BigEndian.Uint32(prefix[8:12]))/2)
			if _, err := io.ReadFull(first.Body, part); err != nil {
				t.Fatal(err)
			}
			first.Body.Close()
			second := c.download(2)
			go func() {
				defer second.Body.Close()
				for {
					var header [44]byte
					if _, err := io.ReadFull(second.Body, header[:]); err != nil {
						return
					}
					sequence := binary.BigEndian.Uint64(header[:8])
					size := binary.BigEndian.Uint32(header[8:12])
					if size == 0 || size > packet.MaxBlock {
						c.errors <- fmt.Errorf("download block length")
						return
					}
					body := make([]byte, size)
					if _, err := io.ReadFull(second.Body, body); err != nil {
						return
					}
					c.downMu.Lock()
					expected := c.down
					c.downMu.Unlock()
					if sequence != expected || !packet.Verify(c.secret, "down", c.id, sequence, 0, body, header[12:44]) {
						c.errors <- fmt.Errorf("download MAC/sequence")
						return
					}
					if _, err := c.raw.Put(c.input, body); err != nil {
						c.errors <- err
						return
					}
					c.input++
					c.downMu.Lock()
					c.down++
					c.downMu.Unlock()
				}
			}()
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			targetDone := make(chan []byte, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(10 * time.Second))
				body, _ := io.ReadAll(conn)
				targetDone <- body
				conn.Write(body)
				conn.(*net.TCPConn).CloseWrite()
			}()
			open, _ := cell.Encode(1, 4096, []cell.Frame{{Kind: cell.Open, Stream: 1, Body: []byte(listener.Addr().String())}})
			if err := writePacketCell(c.inner, open); err != nil {
				t.Fatal(err)
			}
			opened := false
			for !opened {
				for _, frame := range c.take() {
					if frame.Kind == cell.Opened {
						opened = true
					}
				}
			}
			payload := bytes.Repeat([]byte("byte-exact packet stream"), 4000)
			data, _ := cell.Encode(2, 131072, []cell.Frame{{Kind: cell.Data, Stream: 1, Body: payload}, {Kind: cell.Fin, Stream: 1, Sequence: uint32(len(payload))}})
			if err := writePacketCell(c.inner, data); err != nil {
				t.Fatal(err)
			}
			var result []byte
			finished := false
			for !finished {
				for _, frame := range c.take() {
					if frame.Kind == cell.Data {
						result = append(result, frame.Body...)
					}
					if frame.Kind == cell.Fin {
						finished = true
					}
				}
			}
			if !bytes.Equal(result, payload) {
				t.Fatal("resumed download duplicated or lost bytes")
			}
			select {
			case actual := <-targetDone:
				if !bytes.Equal(actual, payload) {
					t.Fatal("upload replay duplicated target bytes")
				}
			case <-c.ctx.Done():
				t.Fatal("target timeout")
			}
			c.module.mu.Lock()
			opens := c.module.stats.Opens
			c.module.mu.Unlock()
			if opens != 1 {
				t.Fatal("OPEN replay", opens)
			}
		})
	}
}

func TestPacketForgedRequestCannotMutateSession(t *testing.T) {
	c := newPacketTest(t, true)
	c.bootstrap()
	c.module.packetMu.Lock()
	p := c.module.packets[c.id]
	c.module.packetMu.Unlock()
	beforeIn, beforeOut := p.raw.Usage()
	status, _ := c.request("/api/packet/"+c.id+"/upload/2", []byte("malicious"), 9999, make([]byte, 32))
	if status != 403 {
		t.Fatal(status)
	}
	afterIn, afterOut := p.raw.Usage()
	if beforeIn != afterIn || beforeOut != afterOut || p.raw.Uploaded() != 2 {
		t.Fatal("forged cursor or upload changed state")
	}
}

func TestPacketOldUploadReplayDoesNotRenewLease(t *testing.T) {
	c := newPacketTest(t, true)
	c.bootstrap()
	c.module.packetMu.Lock()
	p := c.module.packets[c.id]
	c.module.packetMu.Unlock()
	p.mu.Lock()
	stale := time.Now().Add(-time.Minute)
	p.last = stale
	body := append([]byte(nil), p.authBody...)
	p.mu.Unlock()
	tag := packet.MAC(c.secret, "upload", c.id, 1, c.down, body)
	status, _ := c.request("/api/packet/"+c.id+"/upload/1", body, c.down, tag)
	if status != 200 {
		t.Fatal(status)
	}
	p.mu.Lock()
	last := p.last
	p.mu.Unlock()
	if !last.Equal(stale) {
		t.Fatal("old authenticated replay renewed inactivity lease")
	}
}

func TestPacketOriginReframingRemainsFiniteAndBounded(t *testing.T) {
	for _, size := range []int{1, packet.MaxBlock, packet.MaxBlock + 1} {
		request := httptest.NewRequest(http.MethodPost, "https://origin.test/api/packet", bytes.NewReader(make([]byte, size)))
		request.Header.Set("Content-Type", "application/octet-stream")
		request.ContentLength = -1
		body, err := packetBody(httptest.NewRecorder(), request, packet.MaxBlock)
		if size <= packet.MaxBlock {
			if err != nil || len(body) != size {
				t.Fatal("finite chunked body rejected", size, err)
			}
		} else if err == nil {
			t.Fatal("unknown-length body escaped byte limit")
		}
	}
}

func TestPacketEightKiBCellKeepsFramingWithinOneUpload(t *testing.T) {
	c := newPacketTest(t, true)
	c.bootstrap()
	c.module.packetMu.Lock()
	p := c.module.packets[c.id]
	c.module.packetMu.Unlock()
	body, err := cell.Encode(1, 8192, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePacketCell(c.inner, body); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		p.state.mu.Lock()
		up := p.state.up
		p.state.mu.Unlock()
		_, pending := c.raw.Usage()
		if up == 2 && pending == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("packet upload capacity rejected")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestPacketAndDirectShareConfiguredSessionQuota(t *testing.T) {
	c := newPacketTest(t, true)
	c.module.MaxSessions = 1
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	c.server.Client().Jar = jar
	root, err := c.server.Client().Get(c.server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, root.Body)
	root.Body.Close()
	if root.StatusCode != 200 {
		t.Fatal(root.StatusCode)
	}
	c.bootstrap()
	c.module.mu.Lock()
	visitors, count := len(c.module.sessions), c.module.packetCount
	c.module.mu.Unlock()
	if visitors != 0 || count != 1 {
		t.Fatal("public visitor was not transferred to packet admission", visitors, count)
	}
	root, err = c.server.Client().Get(c.server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	root.Body.Close()
	if root.StatusCode != 400 {
		t.Fatal("direct admission exceeded shared session quota", root.StatusCode)
	}
	c.module.expirePackets(time.Now().Add(2 * time.Minute))
	c.module.mu.Lock()
	count = c.module.packetCount
	c.module.mu.Unlock()
	if count != 0 {
		t.Fatal("expired packet retained a reservation")
	}
}

func TestStoppedModuleCannotAllocatePacketSession(t *testing.T) {
	certificate, key, _ := testCertificate(t, t.TempDir())
	module := &Transport{ApplicationRoot: testApplicationRoot(t), Access: testAccess(),
		PacketCertificate: certificate, PacketKey: key}
	if err := module.Provision(testCaddyContext(t)); err != nil {
		t.Fatal(err)
	}
	if err := module.Cleanup(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "https://origin.test/api/packet", bytes.NewReader(make([]byte, 64)))
	request.TLS = &tls.ConnectionState{}
	request.Header.Set("Content-Type", "application/octet-stream")
	response := httptest.NewRecorder()
	next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) error { w.WriteHeader(404); return nil })
	if err := module.ServeHTTP(response, request, next); err != nil {
		t.Fatal(err)
	}
	if response.Code != 503 || module.packetCount != 0 || len(module.packets) != 0 {
		t.Fatal("stopped module accepted packet setup", response.Code)
	}
}

func TestExpiredUploadReceiptDoesNotTerminateLiveSession(t *testing.T) {
	c := newPacketTest(t, true)
	c.bootstrap()
	c.module.packetMu.Lock()
	p := c.module.packets[c.id]
	c.module.packetMu.Unlock()
	for sequence := uint32(1); sequence <= 20; sequence++ {
		body, err := cell.Encode(sequence, 512, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := writePacketCell(c.inner, body); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, pending := c.raw.Usage()
		if p.raw.Uploaded() > packet.ReceiptCount+2 && pending == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("receipt window did not advance")
		}
		time.Sleep(time.Millisecond)
	}
	p.mu.Lock()
	body := append([]byte(nil), p.authBody...)
	last := p.last
	p.mu.Unlock()
	tag := packet.MAC(c.secret, "upload", c.id, 1, c.down, body)
	status, _ := c.request("/api/packet/"+c.id+"/upload/1", body, c.down, tag)
	if status != 410 {
		t.Fatal(status)
	}
	p.mu.Lock()
	closed, now := p.closed, p.last
	p.mu.Unlock()
	if closed || !now.Equal(last) {
		t.Fatal("old receipt replay altered a live session")
	}
}
