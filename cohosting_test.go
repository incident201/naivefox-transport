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
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caddyserver/forwardproxy/httpclient"
	"github.com/gorilla/websocket"
	"github.com/incident201/naivefox-transport/internal/cell"
	"golang.org/x/net/http2"
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
	templateRoot := copyApplicationTemplate(t)
	customRoot := append(mustReadFile(t, filepath.Join(templateRoot, "index.html")), []byte("<!-- actual external application -->")...)
	writeApplicationFile(t, templateRoot, "index.html", customRoot)
	// Use a different loopback host from the proxy so a hostname-only Caddy
	// route cannot accidentally admit CONNECT because both authorities happen
	// to contain 127.0.0.1.
	target, err := net.Listen("tcp", "127.0.0.2:0")
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
	_, listenPort, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	const siteMarker = ":443, {$NAIVEFOX_SERVER} {"
	if !strings.Contains(config, siteMarker) {
		t.Fatal("example must retain the hostless :443 CONNECT route")
	}
	config = strings.Replace(config, siteMarker, fmt.Sprintf("https://:%s, {$NAIVEFOX_SERVER} {", listenPort), 1)
	config = strings.Replace(config, "\troute {", fmt.Sprintf("\ttls %s %s\n\troute {", certFile, keyFile), 1)
	config = strings.Replace(config, "\t\t\tforward_proxy {", "\t\t\tforward_proxy {\n\t\t\t\tacl {\n\t\t\t\t\tallow 127.0.0.0/8\n\t\t\t\t}", 1)
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
		"NAIVEFOX_APP_ROOT="+templateRoot,
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
			if readErr != nil || response.StatusCode != 200 || response.ProtoMajor != 2 || len(body) != 4096 || !bytes.Contains(body, []byte("actual external application")) || response.Header.Get("X-App-Profile") != defaultProfile || response.Header.Get("X-App-Auth") != "basic" {
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
	if err != nil || connected.StatusCode != 200 || connected.Header.Get("Padding") == "" {
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

	classicH2TLS, err := tls.Dial("tcp", address, &tls.Config{RootCAs: roots, NextProtos: []string{"h2"}})
	if err != nil {
		t.Fatal(err)
	}
	if classicH2TLS.ConnectionState().NegotiatedProtocol != "h2" {
		t.Fatalf("classic CONNECT ALPN: %q", classicH2TLS.ConnectionState().NegotiatedProtocol)
	}
	classicH2TLS.SetDeadline(time.Now().Add(10 * time.Second))
	requestBody, requestWriter := io.Pipe()
	connectURL, err := url.Parse("https://" + target.Addr().String() + "/")
	if err != nil {
		t.Fatal(err)
	}
	connectRequest := &http.Request{
		Method: http.MethodConnect,
		URL:    connectURL,
		Host:   target.Addr().String(),
		Header: http.Header{
			"Padding":             []string{"!#$()+<>?@[]^`{}~~~~~~~~~~~~~~~~"},
			"Proxy-Authorization": []string{"Basic " + base64.StdEncoding.EncodeToString([]byte("fixture:fixture"))},
		},
		Body:  requestBody,
		Proto: "HTTP/2.0", ProtoMajor: 2,
	}
	h2Transport := http2.Transport{}
	h2Client, err := h2Transport.NewClientConn(classicH2TLS)
	if err != nil {
		t.Fatal(err)
	}
	connectResponse, err := h2Client.RoundTrip(connectRequest)
	if err != nil {
		t.Fatal(err)
	}
	if connectResponse.StatusCode != http.StatusOK || connectResponse.Header.Get("Padding") == "" {
		t.Fatalf("classic H2 CONNECT: status=%s padding=%q", connectResponse.Status, connectResponse.Header.Get("Padding"))
	}
	classicH2 := httpclient.NewHttp2Conn(classicH2TLS, requestWriter, connectResponse.Body)
	defer classicH2.Close()
	checkClassicH2 := func(message string) {
		t.Helper()
		payload := []byte(message)
		upload := append([]byte{byte(len(payload) >> 8), byte(len(payload)), 0}, payload...)
		if _, err := classicH2.Write(upload); err != nil {
			t.Fatal(err)
		}
		header := make([]byte, 3)
		if _, err := io.ReadFull(classicH2, header); err != nil {
			t.Fatal(err)
		}
		payloadLength := int(header[0])*256 + int(header[1])
		echo := make([]byte, payloadLength+int(header[2]))
		if _, err := io.ReadFull(classicH2, echo); err != nil || !bytes.Equal(echo[:payloadLength], payload) {
			t.Fatalf("classic H2 padded echo: header=%v body=%q error=%v", header, echo, err)
		}
	}
	checkClassicH2("classic h2 before no-connect")

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
	checkClassicH2("classic h2 remains connected")

	client.Jar, _ = cookiejar.New(nil)
	response, err := client.Get(origin + "/")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.Header.Get("X-App-Realtime") != "websocket-v1" {
		t.Fatal("missing realtime advertisement")
	}
	for round := 0; round < 20; round++ {
		var frames []cell.Frame
		if round == 0 {
			frames = []cell.Frame{{Kind: cell.Auth, Body: []byte(testAuthorization)}}
		}
		upload(uint32(round), frames)
		response, err := client.Get(origin + startupPath(round))
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		sequence, _, _, decodeErr := cell.Decode(body)
		if response.ProtoMajor != 2 || response.StatusCode != 200 || readErr != nil || decodeErr != nil || sequence != uint32(round) {
			t.Fatal("hybrid H2 bootstrap")
		}
	}
	dialer := websocket.Dialer{TLSClientConfig: &tls.Config{RootCAs: roots}, Jar: client.Jar, Subprotocols: []string{realtimeProtocol}}
	ws, response, err := dialer.Dial("wss://"+address+"/api/realtime", http.Header{"Origin": []string{origin}})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	if response.StatusCode != 101 || ws.Subprotocol() != realtimeProtocol {
		t.Fatal("real WebSocket handshake")
	}
	ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	body, err := cell.Encode(20, 512, []cell.Frame{{Kind: cell.Open, Stream: 1, Body: []byte(target.Addr().String())}})
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.WriteMessage(websocket.BinaryMessage, body); err != nil {
		t.Fatal(err)
	}
	_, body, err = ws.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	sequence, frames, _, err := cell.Decode(body)
	if err != nil || sequence != 20 || len(frames) == 0 || frames[0].Kind != cell.Ack || frames[0].Sequence != 20 {
		t.Fatal("WebSocket NFC1 acknowledgement")
	}
	checkClassic("classic remains connected through hybrid")
	checkClassicH2("classic h2 remains connected through hybrid")
}

func TestCombinedCaddyRejectsInvalidExternalApplication(t *testing.T) {
	binary := os.Getenv("NAIVEFOX_CADDY_BIN")
	if binary == "" {
		t.Skip("set NAIVEFOX_CADDY_BIN to the combined Caddy executable")
	}
	dir := t.TempDir()
	certFile, keyFile, _ := testCertificate(t, dir)
	templateRoot := copyApplicationTemplate(t)
	stylePath := filepath.Join(templateRoot, "assets", "site.css")
	style := []byte{0xff, 0xfe}
	if err := os.WriteFile(stylePath, style, 0644); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	example, err := os.ReadFile("examples/Caddyfile")
	if err != nil {
		t.Fatal(err)
	}
	config := "{\n admin off\n auto_https off\n}\n" + string(example)
	config = strings.Replace(config, ":443, {$NAIVEFOX_SERVER} {", "https://:"+port+", {$NAIVEFOX_SERVER} {", 1)
	config = strings.Replace(config, "\troute {", fmt.Sprintf("\ttls %s %s\n\troute {", certFile, keyFile), 1)
	configFile := filepath.Join(dir, "Caddyfile")
	if err := os.WriteFile(configFile, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "validate", "--adapter", "caddyfile", "--config", configFile)
	command.Env = append(os.Environ(),
		"XDG_CONFIG_HOME="+dir, "XDG_DATA_HOME="+dir,
		"NAIVEFOX_SERVER=https://"+address,
		"NAIVEFOX_APP_ROOT="+templateRoot,
		"NAIVEFOX_USER=fixture", "NAIVEFOX_PASSWORD=fixture")
	output, err := command.CombinedOutput()
	if err == nil || !bytes.Contains(output, []byte("NUL-free UTF-8")) {
		t.Fatalf("invalid external application validation: %v\n%s", err, output)
	}
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
