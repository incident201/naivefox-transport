package transport

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/incident201/naivefox-transport/internal/packet"
)

type failedPacketBody struct {
	data []byte
	err  error
}

func (b *failedPacketBody) Read(out []byte) (int, error) {
	if len(b.data) == 0 {
		return 0, b.err
	}
	n := copy(out, b.data)
	b.data = b.data[n:]
	return n, nil
}
func (*failedPacketBody) Close() error { return nil }

func TestPacketBodyReadFailureIsRetryable(t *testing.T) {
	for _, failure := range []error{os.ErrDeadlineExceeded, io.ErrUnexpectedEOF, context.Canceled} {
		for _, stage := range []string{"begin", "auth", "upload"} {
			t.Run(stage+"/"+failure.Error(), func(t *testing.T) {
				request := httptest.NewRequest(http.MethodPost, "https://localhost/api/packet", bytes.NewReader(make([]byte, 202)))
				request.Header.Set("Content-Type", "application/octet-stream")
				request.Body = &failedPacketBody{data: []byte{1, 2}, err: failure}
				response := httptest.NewRecorder()
				module := &Transport{}
				var err error
				switch stage {
				case "begin":
					err = module.beginPacket(response, request)
				case "auth":
					err = module.authenticatePacket(response, request, nil)
				case "upload":
					err = module.uploadPacket(response, request, nil, nil, 0)
				}
				if err != nil || response.Code != http.StatusServiceUnavailable {
					t.Fatalf("read failure returned %d, %v", response.Code, err)
				}
			})
		}
	}
}

type packetShortReadDeadline struct{ http.ResponseWriter }

func (w packetShortReadDeadline) SetReadDeadline(time.Time) error {
	return http.NewResponseController(w.ResponseWriter).SetReadDeadline(time.Now().Add(25 * time.Millisecond))
}
func (w packetShortReadDeadline) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func TestPacketH2IncompleteBodyCanRetryWithoutClosingSession(t *testing.T) {
	secret := bytes.Repeat([]byte{7}, 32)
	body := bytes.Repeat([]byte{9}, 202)
	state := &packetSession{id: hex.EncodeToString(bytes.Repeat([]byte{4}, 32)), raw: packet.New()}
	defer state.raw.Close()
	module := &Transport{}
	var requests atomic.Uint32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			t.Error("HTTP/2 was not used")
		}
		if requests.Add(1) == 1 {
			w = packetShortReadDeadline{w}
		}
		if err := module.uploadPacket(w, r, state, secret, 0); err != nil {
			t.Error(err)
		}
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	request := func(reader io.Reader, tag []byte) (int, []byte) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/api/packet/"+state.id+"/upload/0", reader)
		if err != nil {
			t.Fatal(err)
		}
		req.ContentLength = int64(len(body))
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("NaiveFox-Cursor", "0")
		req.Header.Set("NaiveFox-MAC", hex.EncodeToString(tag))
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		reply, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, reply
	}
	tag := packet.MAC(secret, "upload", state.id, 0, 0, body)
	missing, writer := io.Pipe()
	defer missing.Close()
	defer writer.Close()
	status, _ := request(missing, tag)
	if status != http.StatusServiceUnavailable || state.raw.Uploaded() != 0 || state.closed {
		t.Fatalf("incomplete body: status=%d next=%d closed=%v", status, state.raw.Uploaded(), state.closed)
	}
	for i := 0; i < 2; i++ {
		status, reply := request(bytes.NewReader(body), tag)
		if status != http.StatusOK || len(reply) != 48 || binary.BigEndian.Uint64(reply[:8]) != 1 {
			t.Fatalf("retry %d: status=%d reply length=%d", i, status, len(reply))
		}
	}
	received := make([]byte, len(body))
	if _, err := io.ReadFull(state.raw, received); err != nil || !bytes.Equal(received, body) {
		t.Fatalf("retry data: %v", err)
	}
	state.raw.SetReadDeadline(time.Now().Add(10 * time.Millisecond))
	if n, err := state.raw.Read(make([]byte, 1)); n != 0 || err == nil {
		t.Fatalf("duplicate delivery: %d %v", n, err)
	}
	badTag := append([]byte(nil), tag...)
	badTag[0] ^= 1
	if status, _ := request(bytes.NewReader(body), badTag); status != http.StatusForbidden {
		t.Fatalf("bad MAC accepted: %d", status)
	}
}
