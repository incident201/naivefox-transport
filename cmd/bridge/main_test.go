package main

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"naivefox.local/transport/internal/cell"
	"naivefox.local/transport/internal/mux"
)

func TestBridgeDefaultsMatchContinuousPipeline(t *testing.T) {
	cfg, err := decodeConfig(strings.NewReader(`{}`))
	if err != nil || !cfg.Continuous || cfg.ReceiveWindow != 524288 || cfg.Append || cfg.FillerOnly {
		t.Fatal("bridge default mismatch")
	}
	cfg, err = decodeConfig(strings.NewReader(`{"continuous":false,"receive_window":0}`))
	if err != nil || cfg.Continuous || cfg.ReceiveWindow != 0 {
		t.Fatal("explicit legacy settings lost")
	}
	if _, err = decodeConfig(strings.NewReader(`{"unknown":true}`)); err == nil {
		t.Fatal("unknown bridge configuration accepted")
	}
}

func TestBothFrontendsByteExactThroughCells(t *testing.T) {
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	go func() {
		for {
			conn, err := target.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				body, err := io.ReadAll(io.LimitReader(conn, 400000))
				if err == nil {
					conn.Write(body)
				}
			}()
		}
	}()
	server := mux.New(func(ctx context.Context, authority string) (net.Conn, error) {
		if authority != "example.test:443" {
			t.Error("authority not retained")
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", target.Addr().String())
	})
	client := mux.New(nil)
	defer client.Close()
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errors := make(chan error, 1)
	go func() {
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		var sequence uint32
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			for _, pair := range [][2]*mux.Peer{{client, server}, {server, client}} {
				body, err := cell.Encode(sequence, 4096, pair[0].Take(4080))
				if err != nil {
					errors <- err
					return
				}
				_, frames, _, err := cell.Decode(body)
				if err == nil {
					err = pair[1].Receive(frames)
				}
				if err != nil {
					errors <- err
					return
				}
			}
			sequence++
		}
	}()
	var wg sync.WaitGroup
	for index := 0; index < 4; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Error(err)
				return
			}
			defer listener.Close()
			app, err := net.Dial("tcp", listener.Addr().String())
			if err != nil {
				t.Error(err)
				return
			}
			defer app.Close()
			app.SetDeadline(time.Now().Add(8 * time.Second))
			bridge, err := listener.Accept()
			if err != nil {
				t.Error(err)
				return
			}
			go handle(bridge, index%2 == 0, client)
			reader := bufio.NewReader(app)
			if index%2 == 0 {
				app.Write([]byte{5, 1, 0})
				reply := make([]byte, 2)
				if _, err := io.ReadFull(reader, reply); err != nil || !bytes.Equal(reply, []byte{5, 0}) {
					t.Error("socks method")
					return
				}
				request := append([]byte{5, 1, 0, 3, 12}, []byte("example.test")...)
				request = append(request, 1, 187)
				app.Write(request)
				reply = make([]byte, 10)
				if _, err := io.ReadFull(reader, reply); err != nil || reply[1] != 0 {
					t.Error("socks connect")
					return
				}
			} else {
				io.WriteString(app, "CONNECT example.test:443 HTTP/1.1\r\nHost: example.test:443\r\n\r\n")
				line, err := reader.ReadString('\n')
				if err != nil || line != "HTTP/1.1 200 Connection Established\r\n" {
					t.Error("http connect")
					return
				}
				reader.ReadString('\n')
			}
			body := bytes.Repeat([]byte{byte(index + 10)}, 300000)
			if _, err := app.Write(body); err != nil {
				t.Error(err)
				return
			}
			app.(*net.TCPConn).CloseWrite()
			got, err := io.ReadAll(reader)
			if err != nil || !bytes.Equal(got, body) {
				t.Errorf("stream %d bytes=%d err=%v", index, len(got), err)
			}
		}(index)
	}
	wg.Wait()
	select {
	case err := <-errors:
		t.Fatal(err)
	default:
	}
}
