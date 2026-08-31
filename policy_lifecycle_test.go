package transport

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/caddyserver/forwardproxy"
)

func TestDirectPolicyKeepsConnectionAfterDialContextCancellation(t *testing.T) {
	for _, targetClosesFirst := range []bool{false, true} {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		payload := bytes.Repeat([]byte("ordered bytes across FIN\n"), 8192)
		done := make(chan error, 1)
		go func() {
			conn, err := listener.Accept()
			if err != nil {
				done <- err
				return
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(3 * time.Second))
			if targetClosesFirst {
				if _, err := io.WriteString(conn, "greeting before FIN"); err != nil {
					done <- err
					return
				}
				if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
					done <- err
					return
				}
			}
			body, err := io.ReadAll(conn)
			if err == nil && !bytes.Equal(body, payload) {
				err = io.ErrUnexpectedEOF
			}
			if err == nil && !targetClosesFirst {
				_, err = conn.Write(body)
			}
			done <- err
		}()
		policy, err := newTCPPolicy(&forwardproxy.Handler{ACL: []forwardproxy.ACLRule{{Subjects: []string{"127.0.0.1"}, Allow: true}}})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		conn, err := policy.DialContext(ctx, listener.Addr().String())
		cancel() // Like net.Dialer, cancellation after success does not own the socket.
		if err != nil {
			listener.Close()
			t.Fatal(err)
		}
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		if targetClosesFirst {
			body, err := io.ReadAll(conn)
			if err != nil || string(body) != "greeting before FIN" {
				t.Fatalf("target FIN: %q %v", body, err)
			}
		}
		if _, err := conn.Write(payload); err != nil {
			t.Fatal(err)
		}
		if err := conn.(interface{ CloseWrite() error }).CloseWrite(); err != nil {
			t.Fatal(err)
		}
		if !targetClosesFirst {
			body, err := io.ReadAll(conn)
			if err != nil || !bytes.Equal(body, payload) {
				t.Fatalf("reply after upload FIN: bytes=%d error=%v", len(body), err)
			}
		}
		conn.Close()
		listener.Close()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}
