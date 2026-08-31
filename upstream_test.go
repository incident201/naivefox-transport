package transport

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func upstreamURL(t *testing.T, value string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func rawUpstream(t *testing.T, handler func(net.Conn) error) (string, <-chan error) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	t.Cleanup(func() { listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		t.Cleanup(func() { conn.Close() })
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		done <- handler(conn)
	}()
	return listener.Addr().String(), done
}

func upstreamDone(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("upstream did not finish")
	}
}

func upstreamHalfClose(t *testing.T, conn net.Conn) {
	t.Helper()
	half, ok := conn.(interface{ CloseWrite() error })
	if !ok {
		t.Fatal("upstream lost CloseWrite")
	}
	if err := half.CloseWrite(); err != nil {
		t.Fatal(err)
	}
}

func TestUpstreamHTTPPrefetchedTailAndHalfClose(t *testing.T) {
	expected := "Basic " + base64.StdEncoding.EncodeToString([]byte("user@name:pass:word"))
	address, done := rawUpstream(t, func(conn net.Conn) error {
		req, err := http.ReadRequest(bufio.NewReader(conn))
		if err != nil {
			return err
		}
		if req.Method != "CONNECT" || req.Host != "remote.invalid:443" || req.Header.Get("Proxy-Authorization") != expected {
			return errors.New("incorrect CONNECT authority or authorization")
		}
		if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\nprefetched"); err != nil {
			return err
		}
		data, err := io.ReadAll(conn)
		if err != nil || string(data) != "upload" {
			return errors.New("upload or half-close failed")
		}
		_, err = io.WriteString(conn, "after-fin")
		return err
	})
	u := upstreamURL(t, "http://"+address)
	u.User = url.UserPassword("user@name", "pass:word")
	conn, err := dialUpstream(context.Background(), "remote.invalid:443", u, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	prefix := make([]byte, len("prefetched"))
	if _, err := io.ReadFull(conn, prefix); err != nil || string(prefix) != "prefetched" {
		t.Fatal("prefetched target bytes lost")
	}
	if _, err := io.WriteString(conn, "upload"); err != nil {
		t.Fatal(err)
	}
	upstreamHalfClose(t, conn)
	tail, err := io.ReadAll(conn)
	if err != nil || string(tail) != "after-fin" {
		t.Fatal("half-close lost response")
	}
	upstreamDone(t, done)
}

func TestUpstreamHTTPRejectsInvalidResponses(t *testing.T) {
	for name, response := range map[string]string{
		"authentication": "HTTP/1.1 407 Proxy Authentication Required\r\nContent-Length: 0\r\n\r\n",
		"non200":         "HTTP/1.1 201 Created\r\nContent-Length: 0\r\n\r\n",
		"malformed":      "not http\r\n\r\n",
		"oversized":      "HTTP/1.1 200 OK\r\nLarge: " + strings.Repeat("x", maxUpstreamResponseHeaders) + "\r\n\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			address, _ := rawUpstream(t, func(c net.Conn) error {
				_, err := http.ReadRequest(bufio.NewReader(c))
				if err != nil {
					return err
				}
				_, err = io.WriteString(c, response)
				return err
			})
			conn, err := dialUpstream(context.Background(), "remote.invalid:443", upstreamURL(t, "http://private-user:private-password@"+address), time.Second)
			if conn != nil {
				conn.Close()
				t.Fatal("accepted rejected CONNECT")
			}
			if err == nil {
				t.Fatal("accepted invalid response")
			}
			if strings.Contains(err.Error(), "private-") {
				t.Fatal("error leaked upstream credentials")
			}
		})
	}
}

func TestUpstreamSetupCancellation(t *testing.T) {
	for _, scheme := range []string{"http", "https", "socks5", "socks5h"} {
		for _, deadline := range []bool{false, true} {
			name := fmt.Sprintf("%s/deadline=%t", scheme, deadline)
			t.Run(name, func(t *testing.T) {
				entered := make(chan struct{})
				address, done := rawUpstream(t, func(c net.Conn) error {
					close(entered)
					_, err := io.Copy(io.Discard, c)
					return err
				})
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if !deadline {
					go func() { <-entered; time.Sleep(25 * time.Millisecond); cancel() }()
				}
				timeout := time.Duration(0)
				expected := context.Canceled
				if deadline {
					timeout = 75 * time.Millisecond
					expected = context.DeadlineExceeded
				}
				started := time.Now()
				conn, err := dialUpstream(ctx, "remote.invalid:443", upstreamURL(t, scheme+"://"+address), timeout)
				if conn != nil {
					conn.Close()
					t.Fatal("silent upstream connected")
				}
				if !errors.Is(err, expected) {
					t.Fatalf("cancellation error = %v, want %v", err, expected)
				}
				if time.Since(started) > time.Second {
					t.Fatal("cancellation was not bounded")
				}
				upstreamDone(t, done)
			})
		}
	}
}

func TestUpstreamSOCKS5RemoteDNSAuthAndHalfClose(t *testing.T) {
	for _, scheme := range []string{"socks5", "socks5h"} {
		t.Run(scheme, func(t *testing.T) {
			address, done := rawUpstream(t, func(c net.Conn) error {
				head := make([]byte, 2)
				if _, err := io.ReadFull(c, head); err != nil {
					return err
				}
				methods := make([]byte, int(head[1]))
				if _, err := io.ReadFull(c, methods); err != nil {
					return err
				}
				if head[0] != 5 || !bytes.Contains(methods, []byte{2}) {
					return errors.New("missing SOCKS5 auth offer")
				}
				if _, err := c.Write([]byte{5, 2}); err != nil {
					return err
				}
				if _, err := io.ReadFull(c, head); err != nil {
					return err
				}
				user := make([]byte, int(head[1]))
				if _, err := io.ReadFull(c, user); err != nil {
					return err
				}
				length := make([]byte, 1)
				if _, err := io.ReadFull(c, length); err != nil {
					return err
				}
				password := make([]byte, int(length[0]))
				if _, err := io.ReadFull(c, password); err != nil {
					return err
				}
				if head[0] != 1 || string(user) != "user@name" || string(password) != "pass:word" {
					return errors.New("incorrect SOCKS5 credentials")
				}
				if _, err := c.Write([]byte{1, 0}); err != nil {
					return err
				}
				request := make([]byte, 5)
				if _, err := io.ReadFull(c, request); err != nil {
					return err
				}
				if !bytes.Equal(request[:4], []byte{5, 1, 0, 3}) {
					return errors.New("SOCKS5 did not preserve remote DNS name")
				}
				target := make([]byte, int(request[4])+2)
				if _, err := io.ReadFull(c, target); err != nil {
					return err
				}
				if string(target[:len(target)-2]) != "remote.invalid" || binary.BigEndian.Uint16(target[len(target)-2:]) != 443 {
					return errors.New("SOCKS5 target changed")
				}
				if _, err := c.Write(append([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}, []byte("prefetched")...)); err != nil {
					return err
				}
				data, err := io.ReadAll(c)
				if err != nil || string(data) != "upload" {
					return errors.New("SOCKS5 half-close failed")
				}
				_, err = io.WriteString(c, "after-fin")
				return err
			})
			u := upstreamURL(t, scheme+"://"+address)
			u.User = url.UserPassword("user@name", "pass:word")
			conn, err := dialUpstream(context.Background(), "remote.invalid:443", u, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			prefix := make([]byte, len("prefetched"))
			if _, err := io.ReadFull(conn, prefix); err != nil || string(prefix) != "prefetched" {
				t.Fatal("SOCKS5 lost target tail")
			}
			io.WriteString(conn, "upload")
			upstreamHalfClose(t, conn)
			tail, err := io.ReadAll(conn)
			if err != nil || string(tail) != "after-fin" {
				t.Fatal("SOCKS5 lost half-close response")
			}
			upstreamDone(t, done)
		})
	}
}

func tlsUpstream(t *testing.T, h2 bool, handler http.HandlerFunc) (*httptest.Server, *tls.Config) {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.EnableHTTP2 = h2
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	t.Cleanup(server.Close)
	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())
	return server, &tls.Config{RootCAs: pool}
}

func TestUpstreamHTTPSStrictTrustAndHalfClose(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		t.Run(fmt.Sprintf("http2=%t", h2), func(t *testing.T) {
			done := make(chan error, 1)
			server, roots := tlsUpstream(t, h2, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "CONNECT" || r.Host != "remote.invalid:443" || (r.ProtoMajor == 2) != h2 {
					done <- errors.New("TLS CONNECT protocol or authority mismatch")
					w.WriteHeader(400)
					return
				}
				if !h2 {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						done <- err
						return
					}
					defer conn.Close()
					io.WriteString(conn, "HTTP/1.1 200 OK\r\n\r\nprefetched")
					body, err := io.ReadAll(conn)
					if err == nil && string(body) != "upload" {
						err = errors.New("TLS half-close upload mismatch")
					}
					if err == nil {
						_, err = io.WriteString(conn, "after-fin")
					}
					done <- err
					return
				}
				w.WriteHeader(200)
				io.WriteString(w, "prefetched")
				w.(http.Flusher).Flush()
				body, err := io.ReadAll(r.Body)
				if err == nil && string(body) != "upload" {
					err = errors.New("H2 half-close upload mismatch")
				}
				if err == nil {
					_, err = io.WriteString(w, "after-fin")
				}
				done <- err
			})
			u := upstreamURL(t, server.URL)
			rejected, err := dialUpstream(context.Background(), "remote.invalid:443", u, time.Second)
			if rejected != nil {
				rejected.Close()
				t.Fatal("untrusted TLS accepted")
			}
			if err == nil {
				t.Fatal("untrusted TLS accepted")
			}
			ctx, cancel := context.WithCancel(context.Background())
			conn, err := dialUpstreamTLS(ctx, "remote.invalid:443", u, 250*time.Millisecond, roots)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			cancel() // DialContext cancellation must not destroy an established tunnel.
			prefix := make([]byte, len("prefetched"))
			if _, err := io.ReadFull(conn, prefix); err != nil || string(prefix) != "prefetched" {
				t.Fatal("TLS lost prefetched data")
			}
			time.Sleep(300 * time.Millisecond) // setup deadline must also be cleared
			if _, err := io.WriteString(conn, "upload"); err != nil {
				t.Fatal(err)
			}
			upstreamHalfClose(t, conn)
			tail, err := io.ReadAll(conn)
			if err != nil || string(tail) != "after-fin" {
				t.Fatalf("TLS half-close response failed: %v", err)
			}
			upstreamDone(t, done)
		})
	}
}

func TestUpstreamHTTP2HeaderCancellation(t *testing.T) {
	entered := make(chan struct{})
	server, roots := tlsUpstream(t, true, func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { <-entered; cancel() }()
	conn, err := dialUpstreamTLS(ctx, "remote.invalid:443", upstreamURL(t, server.URL), time.Second, roots)
	if conn != nil {
		conn.Close()
		t.Fatal("silent H2 connected")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("H2 header cancellation failed: %v", err)
	}
}

func TestUpstreamHTTP2DedicatedConnectionsAndBlockedClose(t *testing.T) {
	var accepted atomic.Int32
	server, roots := tlsUpstream(t, true, func(w http.ResponseWriter, r *http.Request) {
		accepted.Add(1)
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		if r.Host == "blocked.invalid:443" {
			<-r.Context().Done()
			return
		}
		body, err := io.ReadAll(r.Body)
		if err == nil {
			w.Write(body)
		}
	})
	u := upstreamURL(t, server.URL)
	first, err := dialUpstreamTLS(context.Background(), "blocked.invalid:443", u, time.Second, roots)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := dialUpstreamTLS(context.Background(), "working.invalid:443", u, time.Second, roots)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if first.LocalAddr().String() == second.LocalAddr().String() {
		t.Fatal("targets share an upstream connection")
	}
	writeDone := make(chan error, 1)
	go func() { _, err := first.Write(make([]byte, 4<<20)); writeDone <- err }()
	first.SetWriteDeadline(time.Now().Add(75 * time.Millisecond))
	select {
	case err := <-writeDone:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("blocked H2 write did not time out: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("H2 flow-control wait ignored deadline")
	}
	io.WriteString(second, "independent")
	upstreamHalfClose(t, second)
	body, err := io.ReadAll(second)
	if err != nil || string(body) != "independent" || accepted.Load() != 2 {
		t.Fatal("closing one H2 target affected another")
	}
}

func TestUpstreamHTTP2RejectsNon200(t *testing.T) {
	server, roots := tlsUpstream(t, true, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusProxyAuthRequired)
	})
	conn, err := dialUpstreamTLS(context.Background(), "remote.invalid:443", upstreamURL(t, server.URL), time.Second, roots)
	if conn != nil {
		conn.Close()
		t.Fatal("accepted rejected H2 CONNECT")
	}
	if err == nil {
		t.Fatal("accepted rejected H2 CONNECT")
	}
}
