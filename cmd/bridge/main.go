package main

import (
	"bufio"
	"crypto/subtle"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"naivefox.local/transport/internal/cell"
	"naivefox.local/transport/internal/mux"
)

type config struct {
	Key         string `json:"key"`
	Token       string `json:"token"`
	Origin      string `json:"origin"`
	Certificate string `json:"certificate"`
	PrivateKey  string `json:"private_key"`
	Ready       string `json:"ready"`
	Stats       string `json:"stats"`
	Append      bool   `json:"append"`
}

func main() {
	path := flag.String("config", "", "private config")
	flag.Parse()
	if err := run(*path); err != nil {
		os.Stderr.WriteString("bridge failed\n")
		os.Exit(1)
	}
}

func run(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	var cfg config
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	err = decoder.Decode(&cfg)
	file.Close()
	if err != nil {
		return err
	}
	if len(cfg.Key) < 32 || len(cfg.Token) < 32 || cfg.Origin == "" {
		return errors.New("configuration")
	}
	peer := mux.New(nil)
	defer peer.Close()
	httpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer httpListener.Close()
	socksListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer socksListener.Close()
	wsListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer wsListener.Close()
	var mu sync.Mutex
	attached := false
	muxHTTP := http.NewServeMux()
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == cfg.Origin }, EnableCompression: false}
	muxHTTP.HandleFunc("/bridge", func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("token")), []byte(cfg.Token)) != 1 {
			w.WriteHeader(404)
			return
		}
		mu.Lock()
		if attached {
			mu.Unlock()
			w.WriteHeader(409)
			return
		}
		attached = true
		mu.Unlock()
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		defer peer.Close()
		conn.SetReadLimit(cell.MaxCell + 5)
		var uploadSequence, downloadSequence uint32
		authSent := false
		for {
			kind, body, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if kind != websocket.BinaryMessage || len(body) < 1 {
				return
			}
			var reply []byte
			switch body[0] {
			case 1:
				if len(body) != 5 {
					return
				}
				capacity := int(binary.BigEndian.Uint32(body[1:]))
				if capacity != 4096 && capacity != 131072 {
					return
				}
				frames := []cell.Frame{}
				budget := capacity - cell.Header
				if !authSent {
					f := cell.Frame{Kind: cell.Auth, Body: []byte(cfg.Key)}
					frames = append(frames, f)
					budget -= f.Size()
					authSent = true
				}
				frames = append(frames, peer.Take(budget)...)
				if cfg.Append {
					for _, f := range frames {
						capacity += f.Size()
					}
				}
				reply, err = cell.Encode(uploadSequence, capacity, frames)
				if err != nil {
					return
				}
				uploadSequence++
			case 2:
				seq, frames, _, err := cell.Decode(body[1:])
				if err != nil || seq != downloadSequence {
					return
				}
				downloadSequence++
				if err := peer.Receive(frames); err != nil {
					return
				}
				reply = []byte{3}
			default:
				return
			}
			if err := conn.WriteMessage(websocket.BinaryMessage, reply); err != nil {
				return
			}
		}
	})
	server := &http.Server{Handler: muxHTTP, ReadHeaderTimeout: 5 * time.Second}
	defer server.Close()
	go server.ServeTLS(wsListener, cfg.Certificate, cfg.PrivateKey)
	admission := make(chan struct{}, 64)
	accept := func(listener net.Listener, socks bool) {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			select {
			case admission <- struct{}{}:
				go func() { defer func() { <-admission }(); handle(conn, socks, peer) }()
			default:
				conn.Close()
			}
		}
	}
	go accept(httpListener, false)
	go accept(socksListener, true)
	ready := map[string]string{"http": httpListener.Addr().String(), "socks": socksListener.Addr().String(), "websocket": wsListener.Addr().String()}
	output, err := json.Marshal(ready)
	if err != nil {
		return err
	}
	if err := os.WriteFile(cfg.Ready, output, 0600); err != nil {
		return err
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(stop)
	<-stop
	peer.Close()
	if cfg.Stats != "" {
		body, _ := json.Marshal(peer.Snapshot())
		return os.WriteFile(cfg.Stats, body, 0600)
	}
	return nil
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }
func (c *bufferedConn) CloseWrite() error {
	if half, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return half.CloseWrite()
	}
	return nil
}

func handle(conn net.Conn, socks bool, peer *mux.Peer) {
	owned := true
	defer func() {
		if owned {
			conn.Close()
		}
	}()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	var authority string
	var wrapped net.Conn = conn
	if socks {
		header := make([]byte, 2)
		if _, err := io.ReadFull(conn, header); err != nil || header[0] != 5 || header[1] == 0 {
			return
		}
		methods := make([]byte, int(header[1]))
		if _, err := io.ReadFull(conn, methods); err != nil {
			return
		}
		found := false
		for _, m := range methods {
			if m == 0 {
				found = true
			}
		}
		if !found {
			conn.Write([]byte{5, 255})
			return
		}
		if _, err := conn.Write([]byte{5, 0}); err != nil {
			return
		}
		head := make([]byte, 4)
		if _, err := io.ReadFull(conn, head); err != nil || head[0] != 5 || head[1] != 1 || head[2] != 0 {
			return
		}
		length := 0
		switch head[3] {
		case 1:
			length = 4
		case 4:
			length = 16
		case 3:
			one := []byte{0}
			if _, err := io.ReadFull(conn, one); err != nil || one[0] == 0 {
				return
			}
			length = int(one[0])
		default:
			return
		}
		address := make([]byte, length+2)
		if _, err := io.ReadFull(conn, address); err != nil {
			return
		}
		host := string(address[:length])
		if head[3] != 3 {
			host = net.IP(address[:length]).String()
		}
		port := int(binary.BigEndian.Uint16(address[length:]))
		if port == 0 {
			return
		}
		authority = net.JoinHostPort(host, strconv.Itoa(port))
		if _, err := conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
			return
		}
	} else {
		reader := bufio.NewReaderSize(conn, 4096)
		line, err := reader.ReadSlice('\n')
		if err != nil || len(line) > 2048 {
			return
		}
		fields := strings.Fields(string(line))
		if len(fields) != 3 || fields[0] != "CONNECT" || fields[2] != "HTTP/1.1" {
			return
		}
		authority = fields[1]
		if _, _, err := net.SplitHostPort(authority); err != nil {
			return
		}
		total := len(line)
		for {
			line, err = reader.ReadSlice('\n')
			total += len(line)
			if err != nil || total > 8192 {
				return
			}
			if string(line) == "\r\n" {
				break
			}
		}
		if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			return
		}
		wrapped = &bufferedConn{Conn: conn, reader: reader}
	}
	conn.SetDeadline(time.Time{})
	if _, err := peer.Open(wrapped, authority); err != nil {
		return
	}
	owned = false
}
