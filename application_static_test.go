package transport

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

func provisionApplicationSite(t *testing.T, root string) *Transport {
	t.Helper()
	module := &Transport{ApplicationRoot: root, Access: testAccess()}
	if err := module.Provision(testCaddyContext(t)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := module.Cleanup(); err != nil {
			t.Error(err)
		}
	})
	return module
}

func requestApplicationSite(t *testing.T, module *Transport, method, target string, headers http.Header) *httptest.ResponseRecorder {
	t.Helper()
	request := testRequest(method, "https://localhost"+target, nil)
	if headers != nil {
		request.Header = headers.Clone()
	}
	response := httptest.NewRecorder()
	next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) error {
		w.WriteHeader(http.StatusNotFound)
		return nil
	})
	if err := module.ServeHTTP(response, request, next); err != nil {
		handlerErr, ok := err.(caddyhttp.HandlerError)
		if !ok {
			t.Fatal(err)
		}
		response.WriteHeader(handlerErr.StatusCode)
	}
	return response
}

func TestApplicationStaticFiles(t *testing.T) {
	root := copyApplicationTemplate(t)
	// Navigation links leave additional files on the ordinary live static path.
	index := append(mustReadFile(t, filepath.Join(root, "index.html")), []byte("<a href=/extra/module.js>Download</a>")...)
	writeApplicationFile(t, root, "index.html", index)
	module := provisionApplicationSite(t, root)
	cases := []struct {
		name, mime string
		body       []byte
	}{
		{"extra/module.js", "text/javascript", []byte("export const title = 'Site';")},
		{"extra/site.css", "text/css", []byte("body { color: navy; }")},
		{"extra/page.html", "text/html", []byte("<h1>Another page</h1>")},
		{"extra/icon.png", "image/png", []byte{137, 80, 78, 71, 13, 10, 26, 10, 0, 255}},
		{"extra/empty.txt", "text/plain", nil},
		{"extra/large.bin", "application/octet-stream", bytes.Repeat([]byte{0, 255, 1, 2}, 256*1024)},
		{"extra/space name.txt", "text/plain", []byte("space")},
		{"extra/caf\u00e9.txt", "text/plain", []byte("unicode")},
	}
	for _, tc := range cases {
		// Added after Provision: extras are neither eagerly loaded nor validated.
		writeApplicationFile(t, root, tc.name, tc.body)
		target := (&url.URL{Path: "/" + tc.name, RawQuery: "v=2"}).String()
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			response := requestApplicationSite(t, module, method, target, nil)
			expected := tc.body
			if method == http.MethodHead {
				expected = nil
			}
			if response.Code != 200 || !bytes.Equal(response.Body.Bytes(), expected) {
				t.Fatalf("%s %s: status=%d length=%d", method, target, response.Code, response.Body.Len())
			}
			if response.Header().Get("Content-Length") != strconv.Itoa(len(tc.body)) ||
				!strings.HasPrefix(response.Header().Get("Content-Type"), tc.mime) ||
				response.Header().Get("Last-Modified") == "" || response.Header().Get("Cache-Control") != "no-cache" {
				t.Fatalf("%s %s: headers=%v", method, target, response.Header())
			}
			for _, header := range []string{"Set-Cookie", "X-App-Profile", "X-App-Auth", "X-App-Realtime", "X-App-Capacity"} {
				if response.Header().Get(header) != "" {
					t.Fatalf("static file acquired transport header %s", header)
				}
			}
		}
	}
	if len(module.sessions) != 0 || len(module.stats.Requests) != 0 {
		t.Fatal("ordinary resources created transport state")
	}
	var concurrent sync.WaitGroup
	for range 32 {
		concurrent.Add(1)
		go func() {
			defer concurrent.Done()
			response := requestApplicationSite(t, module, "GET", "/extra/module.js", nil)
			if response.Code != 200 || !bytes.Equal(response.Body.Bytes(), cases[0].body) {
				t.Error("concurrent static response")
			}
		}()
	}
	concurrent.Wait()
}

func TestApplicationStaticUpdatesAndTransportSnapshot(t *testing.T) {
	root := copyApplicationTemplate(t)
	module := provisionApplicationSite(t, root)
	for _, body := range []string{"first", "replacement"} {
		writeApplicationFile(t, root, "extra.txt", []byte(body))
		response := requestApplicationSite(t, module, "GET", "/extra.txt", nil)
		if response.Code != 200 || response.Body.String() != body {
			t.Fatal("static file was not read live")
		}
	}
	if err := os.Remove(filepath.Join(root, "extra.txt")); err != nil {
		t.Fatal(err)
	}
	if response := requestApplicationSite(t, module, "GET", "/extra.txt", nil); response.Code != 404 {
		t.Fatal("deleted static file remains available")
	}
	for route, snapshot := range module.application.assets {
		source := strings.TrimPrefix(route, "/")
		if route == "/" {
			source = "index.html"
		}
		writeApplicationFile(t, root, source, []byte("invalid replacement"))
		for _, remove := range []bool{false, true} {
			if remove {
				if err := os.Remove(filepath.Join(root, filepath.FromSlash(source))); err != nil {
					t.Fatal(err)
				}
			}
			response := requestApplicationSite(t, module, "GET", route+"?v=changed", nil)
			if response.Code != 200 || !bytes.Equal(response.Body.Bytes(), snapshot.body) ||
				response.Header().Get("Content-Length") != strconv.Itoa(len(snapshot.body)) ||
				response.Header().Get("Content-Type") != snapshot.mime {
				t.Fatalf("transport snapshot changed for %s", route)
			}
		}
	}
}

func TestApplicationStaticRangeAndConditionalRequests(t *testing.T) {
	root := copyApplicationTemplate(t)
	writeApplicationFile(t, root, "sample.txt", []byte("0123456789"))
	modified := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(root, "sample.txt"), modified, modified); err != nil {
		t.Fatal(err)
	}
	module := provisionApplicationSite(t, root)
	cases := []struct {
		method             string
		headers            http.Header
		status             int
		body, contentRange string
	}{
		{"GET", http.Header{"Range": {"bytes=2-5"}}, 206, "2345", "bytes 2-5/10"},
		{"HEAD", http.Header{"Range": {"bytes=2-5"}}, 206, "", "bytes 2-5/10"},
		{"GET", http.Header{"Range": {"bytes=20-30"}}, 416, "invalid range: failed to overlap\n", "bytes */10"},
		{"GET", http.Header{"If-Modified-Since": {modified.Format(http.TimeFormat)}}, 304, "", ""},
		{"GET", http.Header{"If-Modified-Since": {modified.Add(-time.Second).Format(http.TimeFormat)}}, 200, "0123456789", ""},
		{"GET", http.Header{"Range": {"bytes=2-5"}, "If-Range": {modified.Add(-time.Second).Format(http.TimeFormat)}}, 200, "0123456789", ""},
	}
	for _, tc := range cases {
		response := requestApplicationSite(t, module, tc.method, "/sample.txt?cache=1", tc.headers)
		if response.Code != tc.status || response.Body.String() != tc.body || response.Header().Get("Content-Range") != tc.contentRange {
			t.Fatalf("%s %v: %d %q %v", tc.method, tc.headers, response.Code, response.Body.String(), response.Header())
		}
	}
}

func TestApplicationStaticDirectoriesAndAliases(t *testing.T) {
	root := copyApplicationTemplate(t)
	writeApplicationFile(t, root, "pages/nested/index.html", []byte("<h1>Nested</h1>"))
	writeApplicationFile(t, root, "no-index/hidden.txt", []byte("not a directory listing"))
	module := provisionApplicationSite(t, root)
	redirects := map[string]string{
		"/index.html?x=1%2F2":    "/?x=1%2F2",
		"/%69ndex.html":          "/",
		"/index.html/":           "/",
		"/./":                    "/",
		"/pages/nested?x=1":      "/pages/nested/?x=1",
		"/pages//nested/":        "/pages/nested/",
		"/assets/./app.js?v=1":   "/assets/app.js?v=1",
		"/assets/%2e/site.css":   "/assets/site.css",
		"/assets%2Fimage-1.svg/": "/assets/image-1.svg",
		"/api//sync":             "/api/sync",
		"/api/sync/":             "/api/sync",
		"/api/./realtime":        "/api/realtime",
		"/__lab//stats":          "", // Already reserved by the original diagnostics router.
		"//other.example/test":   "/other.example/test",
	}
	for target, location := range redirects {
		response := requestApplicationSite(t, module, "GET", target, nil)
		if location == "" {
			if response.Code != 404 {
				t.Fatal("diagnostics prefix lost priority")
			}
			continue
		}
		if response.Code != 308 || response.Header().Get("Location") != location {
			t.Fatalf("%s: status=%d location=%q", target, response.Code, response.Header().Get("Location"))
		}
	}
	for _, target := range []string{"/pages/nested/", "/pages/nested/index.html"} {
		response := requestApplicationSite(t, module, "GET", target, nil)
		if response.Code != 200 || response.Body.String() != "<h1>Nested</h1>" {
			t.Fatalf("nested index %s: %d", target, response.Code)
		}
	}
	for _, target := range []string{"/no-index", "/no-index/", "/does-not-exist"} {
		if response := requestApplicationSite(t, module, "GET", target, nil); response.Code != 404 {
			t.Fatalf("directory listing or SPA fallback on %s", target)
		}
	}
	// Follow an asset alias after replacing its source; it must reach memory.
	expected, _ := module.application.asset("/assets/app.js")
	writeApplicationFile(t, root, "assets/app.js", []byte("live replacement"))
	redirect := requestApplicationSite(t, module, "GET", "/assets/./app.js", nil)
	response := requestApplicationSite(t, module, "GET", redirect.Header().Get("Location"), nil)
	if !bytes.Equal(response.Body.Bytes(), expected.body) {
		t.Fatal("normalized asset URL bypassed the memory snapshot")
	}
}

func TestApplicationStaticReservedRoutesAndFallback(t *testing.T) {
	root := copyApplicationTemplate(t)
	const marker = "must not be served as static content"
	for _, name := range []string{"api/sync", "api/realtime", "api/events/brief", "api/events/state", "media/chunk/0", "__lab/stats"} {
		writeApplicationFile(t, root, name, []byte(marker))
	}
	writeApplicationFile(t, root, "ordinary.txt", []byte("public"))
	module := provisionApplicationSite(t, root)
	for _, target := range []string{"/api/sync", "/api/realtime", "/api/events/brief", "/api/events/state", "/media/chunk/0", "/__lab/stats", "/%61pi/sync"} {
		response := requestApplicationSite(t, module, "GET", target, nil)
		if response.Code == 200 || strings.Contains(response.Body.String(), marker) {
			t.Fatalf("static content shadowed %s", target)
		}
	}
	fallbacks := 0
	for _, tc := range []struct{ method, target string }{
		{"GET", "https://example.com/missing?original=yes"},
		{"HEAD", "https://example.com/missing"},
		{"POST", "https://example.com/ordinary.txt"},
		{"CONNECT", "example.com:443"},
	} {
		request := testRequest(tc.method, tc.target, strings.NewReader("original body"))
		request.Header.Set("Proxy-Authorization", "Basic fixture")
		response := httptest.NewRecorder()
		next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
			fallbacks++
			if r != request || r.Header.Get("Proxy-Authorization") != "Basic fixture" {
				t.Error("fallback request was mutated")
			}
			w.WriteHeader(http.StatusTeapot)
			return nil
		})
		if err := module.ServeHTTP(response, request, next); err != nil || response.Code != http.StatusTeapot {
			t.Fatalf("fallback %s: %d %v", tc.method, response.Code, err)
		}
	}
	if fallbacks != 4 {
		t.Fatal("fallback chain or CONNECT handling changed")
	}
}

func TestApplicationStaticConfinedRoot(t *testing.T) {
	root := copyApplicationTemplate(t)
	outside := t.TempDir()
	writeApplicationFile(t, outside, "secret.txt", []byte("outside secret"))
	writeApplicationFile(t, root, "public.txt", []byte("public link target"))
	for _, link := range []struct{ target, name string }{
		{filepath.Join(outside, "secret.txt"), "escape.txt"},
		{outside, "escape-dir"},
		{"public.txt", "internal.txt"},
	} {
		if err := os.Symlink(link.target, filepath.Join(root, link.name)); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
	}
	module := provisionApplicationSite(t, root)
	for _, target := range []string{
		"/escape.txt", "/escape-dir/secret.txt", "/../secret.txt",
		"/%2e%2e/secret.txt", "/safe/%2e%2e/public.txt", "/%5csecret.txt", "/%00secret.txt",
	} {
		response := requestApplicationSite(t, module, "GET", target, nil)
		if (response.Code != 400 && response.Code != 403) || strings.Contains(response.Body.String(), "outside secret") {
			t.Fatalf("unsafe path %s: %d", target, response.Code)
		}
	}
	response := requestApplicationSite(t, module, "GET", "/internal.txt", nil)
	if response.Code != 200 || response.Body.String() != "public link target" {
		t.Fatal("confined relative symlink should work")
	}
}
