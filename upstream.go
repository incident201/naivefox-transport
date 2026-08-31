package transport

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/proxy"
)

const maxUpstreamResponseHeaders = 64 << 10

// dialUpstream opens one dedicated upstream connection per target. It does not
// change forwardproxy, register global dialers, resolve the target locally, or
// share an HTTP/2 connection with another target. The timeout bounds setup only.
func dialUpstream(ctx context.Context, target string, upstream *url.URL, timeout time.Duration) (net.Conn, error) {
	return dialUpstreamTLS(ctx, target, upstream, timeout, nil)
}

// A non-nil TLS config permits tests to install their CA without weakening the
// production trust policy. Hostname verification and ALPN still use the URL.
func dialUpstreamTLS(ctx context.Context, target string, upstream *url.URL, timeout time.Duration, tlsConfig *tls.Config) (result net.Conn, resultErr error) {
	host, port, err := net.SplitHostPort(target)
	number, portErr := strconv.ParseUint(port, 10, 16)
	if err != nil || host == "" || portErr != nil || number == 0 || strings.ContainsAny(target, "\r\n\t ") {
		return nil, errors.New("invalid upstream target authority")
	}
	if upstream == nil || upstream.Hostname() == "" {
		return nil, errors.New("invalid upstream URL")
	}
	scheme := strings.ToLower(upstream.Scheme)
	defaultPort := ""
	switch scheme {
	case "http":
		defaultPort = "80"
	case "https":
		defaultPort = "443"
	case "socks5", "socks5h":
		defaultPort = "1080"
	default:
		return nil, errors.New("unsupported upstream scheme")
	}
	upstreamPort := upstream.Port()
	if upstreamPort == "" {
		upstreamPort = defaultPort
	}
	address := net.JoinHostPort(upstream.Hostname(), upstreamPort)
	setupCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		setupCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	raw, err := (&net.Dialer{}).DialContext(setupCtx, "tcp", address)
	if err != nil {
		return nil, upstreamSetupError(setupCtx, "dial", err)
	}
	// Closing on cancellation also interrupts TLS, request writes, response
	// headers and HTTP/2 flow control, including contexts without a deadline.
	stopped := make(chan struct{})
	stop := context.AfterFunc(setupCtx, func() { raw.Close(); close(stopped) })
	defer func() {
		if !stop() {
			<-stopped
			if resultErr == nil {
				resultErr = setupCtx.Err()
			}
		}
		if resultErr == nil && setupCtx.Err() != nil {
			resultErr = setupCtx.Err()
		}
		if resultErr != nil {
			if result != nil {
				result.Close()
				result = nil
			}
			raw.Close()
		}
	}()
	if deadline, ok := setupCtx.Deadline(); ok {
		if err := raw.SetDeadline(deadline); err != nil {
			return nil, err
		}
	}
	conn := raw
	if scheme == "socks5" || scheme == "socks5h" {
		var auth *proxy.Auth
		if upstream.User != nil {
			password, _ := upstream.User.Password()
			auth = &proxy.Auth{User: upstream.User.Username(), Password: password}
		}
		dialer, err := proxy.SOCKS5("tcp", address, auth, upstreamSocket{raw})
		if err != nil {
			return nil, upstreamSetupError(setupCtx, "SOCKS5", err)
		}
		_, err = dialer.(proxy.ContextDialer).DialContext(setupCtx, "tcp", target)
		if err != nil {
			return nil, upstreamSetupError(setupCtx, "SOCKS5", err)
		}
		// x/net/proxy reads handshake fields with io.ReadFull and does not
		// prefetch data. Return the underlying TCPConn to retain CloseWrite.
		if err := raw.SetDeadline(time.Time{}); err != nil {
			return nil, err
		}
		return raw, nil
	}
	if scheme == "https" {
		config := &tls.Config{}
		if tlsConfig != nil {
			config = tlsConfig.Clone()
		}
		config.ServerName = upstream.Hostname()
		config.InsecureSkipVerify = false
		config.NextProtos = []string{"h2", "http/1.1"}
		secure := tls.Client(raw, config)
		if err := secure.HandshakeContext(setupCtx); err != nil {
			return nil, upstreamSetupError(setupCtx, "TLS", err)
		}
		conn = secure
		if secure.ConnectionState().NegotiatedProtocol == "h2" {
			result, err = dialUpstreamH2(ctx, conn, target, upstream)
			if err != nil {
				return nil, upstreamSetupError(setupCtx, "HTTP/2 CONNECT", err)
			}
			if err := raw.SetDeadline(time.Time{}); err != nil {
				return result, err
			}
			return result, nil
		}
	}
	request := upstreamRequest(target, upstream)
	if err := request.Write(conn); err != nil {
		return nil, upstreamSetupError(setupCtx, "CONNECT write", err)
	}
	limited := &upstreamHeaderReader{reader: conn, remaining: maxUpstreamResponseHeaders}
	reader := bufio.NewReader(limited)
	response, err := http.ReadResponse(reader, request)
	if err != nil {
		return nil, upstreamSetupError(setupCtx, "CONNECT response", err)
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream CONNECT rejected with status %d", response.StatusCode)
	}
	// ReadResponse may prefetch target data. Keep its reader, remove only the
	// header limit, and never interpret target bytes as an HTTP response body.
	limited.remaining = -1
	if err := raw.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	return &upstreamBufferedConn{Conn: conn, reader: reader}, nil
}

func upstreamSetupError(ctx context.Context, phase string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// A socket deadline may fire just before the context timer's callback.
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	// Do not echo upstream URL credentials, response bodies or target names.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("upstream %s failed", phase)
}

func upstreamRequest(target string, upstream *url.URL) *http.Request {
	request := &http.Request{Method: http.MethodConnect, URL: &url.URL{Host: target}, Host: target, Header: make(http.Header)}
	if upstream.User != nil {
		password, _ := upstream.User.Password()
		encoded := base64.StdEncoding.EncodeToString([]byte(upstream.User.Username() + ":" + password))
		request.Header.Set("Proxy-Authorization", "Basic "+encoded)
	}
	return request
}

type upstreamSocket struct{ net.Conn }

func (s upstreamSocket) Dial(string, string) (net.Conn, error) { return s.Conn, nil }
func (s upstreamSocket) DialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.Conn, nil
}

type upstreamHeaderReader struct {
	reader    io.Reader
	remaining int
}

func (r *upstreamHeaderReader) Read(buffer []byte) (int, error) {
	if r.remaining == 0 {
		return 0, errors.New("upstream response headers too large")
	}
	if r.remaining > 0 && len(buffer) > r.remaining {
		buffer = buffer[:r.remaining]
	}
	n, err := r.reader.Read(buffer)
	if r.remaining > 0 {
		r.remaining -= n
	}
	return n, err
}

type upstreamBufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *upstreamBufferedConn) Read(buffer []byte) (int, error) { return c.reader.Read(buffer) }
func (c *upstreamBufferedConn) CloseWrite() error {
	if half, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return half.CloseWrite()
	}
	return errors.New("upstream connection does not support half-close")
}

func dialUpstreamH2(ctx context.Context, conn net.Conn, target string, upstream *url.URL) (net.Conn, error) {
	transport := &http2.Transport{DisableCompression: true, MaxHeaderListSize: maxUpstreamResponseHeaders}
	client, err := transport.NewClientConn(conn)
	if err != nil {
		return nil, err
	}
	input, output := io.Pipe()
	// Setup cancellation closes conn above. The request lifetime belongs to the
	// returned tunnel, not to the setup timeout or a caller's deferred cancel.
	live, cancel := context.WithCancel(context.WithoutCancel(ctx))
	request := upstreamRequest(target, upstream).WithContext(live)
	request.Body, request.ContentLength = input, -1
	request.URL.Scheme = "https"
	response, err := client.RoundTrip(request)
	if err != nil {
		cancel()
		output.CloseWithError(net.ErrClosed)
		input.Close()
		client.Close()
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		cancel()
		output.CloseWithError(net.ErrClosed)
		response.Body.Close()
		client.Close()
		return nil, fmt.Errorf("upstream CONNECT rejected with status %d", response.StatusCode)
	}
	return &upstreamH2Conn{Conn: conn, client: client, body: response.Body, output: output, cancel: cancel}, nil
}

type upstreamH2Conn struct {
	net.Conn
	client                          *http2.ClientConn
	body                            io.ReadCloser
	output                          *io.PipeWriter
	cancel                          context.CancelFunc
	once                            sync.Once
	mu                              sync.Mutex
	closed                          bool
	readTimer, writeTimer           *time.Timer
	readExpired, writeExpired       bool
	readGeneration, writeGeneration uint64
}

func (c *upstreamH2Conn) Read(buffer []byte) (int, error) {
	n, err := c.body.Read(buffer)
	c.mu.Lock()
	expired := c.readExpired
	c.mu.Unlock()
	if err != nil && expired {
		err = os.ErrDeadlineExceeded
	}
	return n, err
}
func (c *upstreamH2Conn) Write(buffer []byte) (int, error) {
	n, err := c.output.Write(buffer)
	c.mu.Lock()
	expired := c.writeExpired
	c.mu.Unlock()
	if err != nil && expired {
		err = os.ErrDeadlineExceeded
	}
	return n, err
}
func (c *upstreamH2Conn) CloseWrite() error { return c.output.Close() }
func (c *upstreamH2Conn) Close() error {
	c.once.Do(func() {
		c.mu.Lock()
		c.closed = true
		if c.readTimer != nil {
			c.readTimer.Stop()
		}
		if c.writeTimer != nil {
			c.writeTimer.Stop()
		}
		c.mu.Unlock()
		c.cancel()
		c.output.CloseWithError(net.ErrClosed)
		c.body.Close()
		c.client.Close()
		c.Conn.Close()
	})
	return nil
}

// A deadline closes this dedicated HTTP/2 connection so that a writer blocked
// on stream flow control is interrupted as well as socket I/O. No other target
// shares that connection. Clear or move deadlines before they expire.
func (c *upstreamH2Conn) setDeadline(deadline time.Time, read bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return net.ErrClosed
	}
	timer := &c.writeTimer
	generation := &c.writeGeneration
	if read {
		timer = &c.readTimer
		generation = &c.readGeneration
	}
	*generation++
	current := *generation
	if *timer != nil {
		(*timer).Stop()
		*timer = nil
	}
	if !deadline.IsZero() {
		*timer = time.AfterFunc(time.Until(deadline), func() {
			c.mu.Lock()
			if c.closed || *generation != current {
				c.mu.Unlock()
				return
			}
			if read {
				c.readExpired = true
			} else {
				c.writeExpired = true
			}
			c.mu.Unlock()
			c.Close()
		})
	}
	return nil
}
func (c *upstreamH2Conn) SetReadDeadline(t time.Time) error  { return c.setDeadline(t, true) }
func (c *upstreamH2Conn) SetWriteDeadline(t time.Time) error { return c.setDeadline(t, false) }
func (c *upstreamH2Conn) SetDeadline(t time.Time) error {
	if err := c.SetReadDeadline(t); err != nil {
		return err
	}
	return c.SetWriteDeadline(t)
}
