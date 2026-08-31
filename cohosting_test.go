package transport

import (
	"bufio"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/incident201/naivefox-transport/internal/cell"
)

// TestCombinedCaddyTLS exercises the actual combined binary and checked-in
// route configuration, not just a stand-in next handler. It is opt-in locally
// and required by CI after tools/build.sh.
func TestCombinedCaddyTLS(t *testing.T) {
	binary := os.Getenv("NAIVEFOX_CADDY_BIN")
	if binary == "" {
		t.Skip("set NAIVEFOX_CADDY_BIN to the combined Caddy executable")
	}
	dir := t.TempDir()
	certFile, keyFile, roots := testCertificate(t, dir)
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
			go func() { defer conn.Close(); io.Copy(conn, conn) }()
		}
	}()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()

	// Start from the user-facing example. A test-only certificate and loopback
	// forwardproxy ACL make the fixture independent of ACME and public targets.
	example, err := os.ReadFile("examples/Caddyfile")
	if err != nil {
		t.Fatal(err)
	}
	config := "{\n admin off\n auto_https off\n}\n" + string(example)
	config = strings.Replace(config, "\troute {", fmt.Sprintf("\ttls %s %s\n\troute {", certFile, keyFile), 1)
	config = strings.Replace(config, "\t\t\tforward_proxy {", "\t\t\tforward_proxy {\n\t\t\t\tacl {\n\t\t\t\t\tallow 127.0.0.1\n\t\t\t\t}", 1)
	configFile := filepath.Join(dir, "Caddyfile")
	if err := os.WriteFile(configFile, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	log, err := os.Create(filepath.Join(dir, "caddy.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	cmd := exec.Command(binary, "run", "--adapter", "caddyfile", "--config", configFile)
	cmd.Env = append(os.Environ(),
		"XDG_CONFIG_HOME="+dir, "XDG_DATA_HOME="+dir,
		"NAIVEFOX_SERVER=https://"+address,
		"NAIVEFOX_USER=fixture", "NAIVEFOX_PASSWORD=fixture")
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	jar, _ := cookiejar.New(nil)
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}, ForceAttemptHTTP2: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Jar: jar, Timeout: 2 * time.Second}
	origin := "https://" + address
	deadline := time.Now().Add(10 * time.Second)
	for {
		response, err := client.Get(origin + "/")
		if err == nil {
			body, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			if readErr != nil || response.StatusCode != 200 || response.ProtoMajor != 2 || len(body) != 4096 || response.Header.Get("X-App-Profile") != defaultProfile || response.Header.Get("X-App-Auth") != "basic" {
				t.Fatalf("origin handshake: status=%d protocol=%s length=%d read=%v", response.StatusCode, response.Proto, len(body), readErr)
			}
			break
		}
		if time.Now().After(deadline) {
			output, _ := os.ReadFile(filepath.Join(dir, "caddy.log"))
			t.Fatalf("Caddy did not start: %v\n%s", err, output)
		}
		time.Sleep(10 * time.Millisecond)
	}

	for _, diagnostic := range []struct{ method, path string }{{http.MethodGet, "/__lab/stats"}, {http.MethodDelete, "/__lab/sessions"}} {
		request, err := http.NewRequest(diagnostic.method, origin+diagnostic.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", testAuthorization)
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("release exposes %s %s: %d", diagnostic.method, diagnostic.path, response.StatusCode)
		}
	}
	classic, err := tls.Dial("tcp", address, &tls.Config{RootCAs: roots, NextProtos: []string{"http/1.1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer classic.Close()
	classic.SetDeadline(time.Now().Add(10 * time.Second))
	fmt.Fprintf(classic, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Basic %s\r\n\r\n", target.Addr(), target.Addr(), base64.StdEncoding.EncodeToString([]byte("fixture:fixture")))
	reader := bufio.NewReader(classic)
	connected, err := http.ReadResponse(reader, &http.Request{Method: "CONNECT"})
	if err != nil || connected.StatusCode != 200 {
		t.Fatalf("classic CONNECT: response=%v error=%v", connected, err)
	}
	checkClassic := func(message string) {
		t.Helper()
		if _, err := io.WriteString(classic, message); err != nil {
			t.Fatal(err)
		}
		body := make([]byte, len(message))
		if _, err := io.ReadFull(reader, body); err != nil || string(body) != message {
			t.Fatalf("classic echo: %q, %v", body, err)
		}
	}
	checkClassic("classic before no-connect")

	upload := func(sequence uint32, frames []cell.Frame) {
		t.Helper()
		body, err := cell.Encode(sequence, 4096, frames)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Post(origin+"/api/sync", "application/octet-stream", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != 204 {
			t.Fatalf("no-connect upload status: %d", response.StatusCode)
		}
	}
	upload(0, []cell.Frame{{Kind: cell.Auth, Body: []byte(testAuthorization)}, {Kind: cell.Open, Stream: 1, Body: []byte(target.Addr().String())}})
	var sequence uint32
	downloadUntil := func(done func([]cell.Frame) bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			response, err := client.Get(origin + "/api/data/interactive")
			if err != nil {
				t.Fatal(err)
			}
			body, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			seq, frames, _, decodeErr := cell.Decode(body)
			if readErr != nil || decodeErr != nil || response.StatusCode != 200 || len(body) != 8192 || response.Header.Get("X-App-Capacity") != "8192" || seq != sequence {
				t.Fatalf("no-connect response: status=%d seq=%d expected=%d read=%v decode=%v", response.StatusCode, seq, sequence, readErr, decodeErr)
			}
			sequence++
			if done(frames) {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("no-connect target did not reply")
	}
	downloadUntil(func(frames []cell.Frame) bool {
		for _, frame := range frames {
			if frame.Kind == cell.Opened && frame.Stream == 1 {
				return true
			}
		}
		return false
	})
	message := []byte("native no-connect through the same Caddy")
	upload(1, []cell.Frame{{Kind: cell.Data, Stream: 1, Body: message}})
	var echoed []byte
	downloadUntil(func(frames []cell.Frame) bool {
		for _, frame := range frames {
			if frame.Kind == cell.Data && frame.Stream == 1 {
				if frame.Sequence != uint32(len(echoed)) {
					t.Fatal("echo byte sequence")
				}
				echoed = append(echoed, frame.Body...)
			}
		}
		return len(echoed) >= len(message)
	})
	if !bytes.Equal(echoed, message) {
		t.Fatal("no-connect echo mismatch")
	}
	checkClassic("classic remains connected")
}

func testCertificate(t *testing.T, dir string) (string, string, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	certificate := &x509.Certificate{
		SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	certFile, keyFile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}), 0600); err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certPEM) {
		t.Fatal("fixture certificate")
	}
	return certFile, keyFile, roots
}
