package transport

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"
)

func releaseBinary(t *testing.T) string {
	t.Helper()
	binary := os.Getenv("NAIVEFOX_CADDY_BIN")
	if binary == "" {
		t.Skip("set NAIVEFOX_CADDY_BIN to test the built release")
	}
	absolute, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	return absolute
}

func TestReleaseBinaryTLS(t *testing.T) {
	binary := releaseBinary(t)
	directory := t.TempDir()
	certificate, key, roots := testCertificate(t, directory)
	application := testApplicationRoot(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	config := fmt.Sprintf("{\n admin off\n auto_https off\n}\nhttps://%s {\n bind 127.0.0.1\n tls %q %q\n route {\n naivefox_transport {\n  application_root %q\n  basic_auth fixture fixture\n  allow 127.0.0.1/32\n  deny all\n }\n }\n}\n", address, certificate, key, application)
	configPath := filepath.Join(directory, "Caddyfile")
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	log, err := os.Create(filepath.Join(directory, "caddy.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	process := exec.Command(binary, "run", "--config", configPath, "--adapter", "caddyfile")
	process.Env = append(os.Environ(), "XDG_DATA_HOME="+filepath.Join(directory, "data"), "XDG_CONFIG_HOME="+filepath.Join(directory, "config"))
	process.Stdout, process.Stderr = log, log
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- process.Wait() }()
	defer func() {
		_ = process.Process.Signal(os.Interrupt)
		select {
		case err := <-exited:
			if err != nil {
				t.Errorf("release process shutdown: %v", err)
			}
		case <-time.After(10 * time.Second):
			_ = process.Process.Kill()
			<-exited
			t.Error("release process required forced shutdown")
		}
	}()
	tcp := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}, ForceAttemptHTTP2: true}
	defer tcp.CloseIdleConnections()
	client := &http.Client{Transport: tcp, Timeout: time.Second}
	url := "https://" + address + "/"
	deadline := time.Now().Add(10 * time.Second)
	for {
		response, err := client.Get(url)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("release server did not become ready: %v; log: %s", err, mustReadFile(t, filepath.Join(directory, "caddy.log")))
		}
		time.Sleep(20 * time.Millisecond)
	}
	quic := &http3.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}}
	defer quic.Close()
	for _, test := range []struct {
		name      string
		version   int
		transport http.RoundTripper
	}{
		{"h2", 2, tcp}, {"h3", 3, quic},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := test.transport.RoundTrip(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil || response.StatusCode != http.StatusOK || response.ProtoMajor != test.version || !bytes.Equal(body, mustReadFile(t, filepath.Join(application, "index.html"))) {
				t.Fatalf("release %s response status=%d protocol=%s error=%v", test.name, response.StatusCode, response.Proto, err)
			}
			for name := range response.Header {
				if strings.HasPrefix(strings.ToLower(name), "x-app-") {
					t.Fatal("public metadata header", name)
				}
			}
		})
	}
}

func TestReleaseBinaryRejectsInvalidApplication(t *testing.T) {
	binary := releaseBinary(t)
	directory := t.TempDir()
	path := filepath.Join(directory, "Caddyfile")
	body := fmt.Sprintf("http://127.0.0.1:8080 {\n route {\n naivefox_transport {\n application_root %q\n }\n }\n}\n", directory)
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	process := exec.Command(binary, "validate", "--config", path, "--adapter", "caddyfile")
	process.Env = append(os.Environ(), "XDG_DATA_HOME="+filepath.Join(directory, "data"), "XDG_CONFIG_HOME="+filepath.Join(directory, "config"))
	output, err := process.CombinedOutput()
	if err == nil {
		t.Fatal("release accepted an invalid application")
	}
	if !strings.Contains(string(output), "application") && !strings.Contains(string(output), "index.html") {
		t.Fatalf("release validation failed for an unrelated reason: %s", output)
	}
}
