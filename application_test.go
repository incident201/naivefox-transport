package transport

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

var applicationTemplateFiles = []string{
	"index.html",
	"assets/site.css",
	"assets/app.js",
	"assets/image-1.svg",
	"assets/image-2.svg",
	"assets/image-3.svg",
	"assets/image-4.svg",
}

func copyApplicationTemplate(t *testing.T) string {
	t.Helper()
	source := testApplicationRoot(t)
	target := t.TempDir()
	for _, relative := range applicationTemplateFiles {
		body, err := os.ReadFile(filepath.Join(source, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(target, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0644); err != nil {
			t.Fatal(err)
		}
	}
	return target
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func writeApplicationFile(t *testing.T, root, relative string, body []byte) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestExternalApplicationSnapshotAndWireCapacities(t *testing.T) {
	root := copyApplicationTemplate(t)
	customRoot := []byte("<!doctype html><title>Custom</title><link rel=stylesheet href=/assets/site.css><script src=/assets/app.js></script><img src=/assets/image-1.svg><img src=/assets/image-2.svg><img src=/assets/image-3.svg><img src=/assets/image-4.svg>")
	customScript := []byte(`"use strict";document.title="Operator application";`)
	writeApplicationFile(t, root, "index.html", customRoot)
	writeApplicationFile(t, root, "assets/app.js", customScript)

	module := &Transport{ApplicationRoot: root, ForwardProxy: testForwardProxy()}
	if err := module.Provision(testCaddyContext(t)); err != nil {
		t.Fatal(err)
	}
	defer module.Cleanup()
	for _, spec := range applicationAssetSpecs {
		asset, ok := module.application.asset(spec.path)
		if !ok || len(asset.body) != spec.size || asset.mime != spec.mime {
			t.Fatalf("asset contract changed for %s", spec.path)
		}
	}
	script, _ := module.application.asset("/assets/app.js")
	if !bytes.HasPrefix(script.body, customScript) {
		t.Fatal("custom script was not served verbatim")
	}
	rootAsset, _ := module.application.asset("/")
	if !bytes.HasPrefix(rootAsset.body, customRoot) {
		t.Fatal("custom root was not loaded")
	}

	next := caddyhttp.HandlerFunc(func(http.ResponseWriter, *http.Request) error {
		t.Fatal("unexpected fallback")
		return nil
	})
	response := httptest.NewRecorder()
	if err := module.ServeHTTP(response, testRequest(http.MethodGet, "https://localhost/", nil), next); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || response.Body.Len() != rootCapacity || !bytes.HasPrefix(response.Body.Bytes(), customRoot) {
		t.Fatal("custom root response contract")
	}
	writeApplicationFile(t, root, "index.html", []byte("changed after provision"))
	unchanged := httptest.NewRecorder()
	if err := module.ServeHTTP(unchanged, testRequest(http.MethodGet, "https://localhost/", nil), next); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(response.Body.Bytes(), unchanged.Body.Bytes()) || !bytes.Equal(rootAsset.body, unchanged.Body.Bytes()) {
		t.Fatal("application snapshot changed after provision")
	}

	var concurrent sync.WaitGroup
	for range 32 {
		concurrent.Add(1)
		go func() {
			defer concurrent.Done()
			current := httptest.NewRecorder()
			if err := module.ServeHTTP(current, testRequest(http.MethodGet, "https://localhost/", nil), next); err != nil || !bytes.Equal(response.Body.Bytes(), current.Body.Bytes()) {
				t.Errorf("concurrent application snapshot: %v", err)
			}
		}()
	}
	concurrent.Wait()
}

func TestProductionTemplateContainsNoTransportRuntime(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(testApplicationRoot(t), "assets", "app.js"))
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{
		"__NFC", "NFC1", "/api/sync", "/api/realtime",
		"__NFC_READER__", "__NFC_LIFECYCLE__", "__NFC_PROFILE__",
	} {
		if bytes.Contains(body, []byte(token)) {
			t.Fatalf("production script contains transport token %q", token)
		}
	}
}

func TestProvisionRequiresExternalApplicationRoot(t *testing.T) {
	module := &Transport{ForwardProxy: testForwardProxy()}
	err := module.Provision(testCaddyContext(t))
	if err == nil || !strings.Contains(err.Error(), "application_root is required") {
		t.Fatalf("missing application_root provision result: %v", err)
	}
}

func TestExternalApplicationRequiresCompleteValidatedBundle(t *testing.T) {
	if _, err := loadApplication(""); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatal("missing application_root accepted")
	}
	if _, err := loadApplication("relative/template"); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatal("relative application_root accepted")
	}
	notDirectory := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notDirectory, []byte("file"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadApplication(notDirectory); err == nil {
		t.Fatal("non-directory application_root accepted")
	}
	for _, relative := range applicationTemplateFiles {
		t.Run("missing-"+strings.ReplaceAll(relative, "/", "-"), func(t *testing.T) {
			root := copyApplicationTemplate(t)
			if err := os.Remove(filepath.Join(root, filepath.FromSlash(relative))); err != nil {
				t.Fatal(err)
			}
			if _, err := loadApplication(root); err == nil || !strings.Contains(err.Error(), relative) {
				t.Fatalf("missing file accepted: %v", err)
			}
		})
	}
}

func TestExternalApplicationRejectsUnsafeOrIncompatibleContent(t *testing.T) {
	cases := []struct {
		name      string
		relative  string
		body      func([]byte) []byte
		errorPart string
	}{
		{
			name: "oversized-root", relative: "index.html",
			body:      func([]byte) []byte { return bytes.Repeat([]byte("x"), rootCapacity+1) },
			errorPart: "exceeds",
		},
		{
			name: "invalid-utf8", relative: "assets/site.css",
			body:      func([]byte) []byte { return []byte{0xff, 0xfe} },
			errorPart: "UTF-8",
		},
		{
			name: "nul", relative: "assets/site.css",
			body:      func([]byte) []byte { return []byte{'a', 0, 'b'} },
			errorPart: "NUL-free",
		},
		{
			name: "invalid-svg", relative: "assets/image-3.svg",
			body:      func([]byte) []byte { return []byte("<html></html>") },
			errorPart: "SVG",
		},
		{
			name: "missing-resource-reference", relative: "index.html",
			body: func(body []byte) []byte {
				return bytes.Replace(body, []byte("/assets/image-4.svg"), []byte("/missing.svg"), 1)
			},
			errorPart: "/assets/image-4.svg",
		},
		{
			name: "duplicate-resource-reference", relative: "index.html",
			body: func(body []byte) []byte {
				return append(body, []byte("<img src=/assets/image-4.svg>")...)
			},
			errorPart: "/assets/image-4.svg",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			root := copyApplicationTemplate(t)
			path := filepath.Join(root, filepath.FromSlash(test.relative))
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			writeApplicationFile(t, root, test.relative, test.body(original))
			if _, err := loadApplication(root); err == nil || !strings.Contains(err.Error(), test.errorPart) {
				t.Fatalf("invalid application accepted: %v", err)
			}
		})
	}
	t.Run("symlink-escape", func(t *testing.T) {
		root := copyApplicationTemplate(t)
		outside := filepath.Join(t.TempDir(), "outside.css")
		if err := os.WriteFile(outside, []byte("secret"), 0644); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "assets", "site.css")
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, path); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if _, err := loadApplication(root); err == nil {
			t.Fatal("application symlink escaped its root")
		}
	})
	t.Run("non-regular", func(t *testing.T) {
		root := copyApplicationTemplate(t)
		path := filepath.Join(root, "assets", "site.css")
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0755); err != nil {
			t.Fatal(err)
		}
		if _, err := loadApplication(root); err == nil || !strings.Contains(err.Error(), "regular") {
			t.Fatalf("directory asset accepted: %v", err)
		}
	})
}

func TestApplicationRootJSONContract(t *testing.T) {
	var module Transport
	if err := json.Unmarshal([]byte(`{"application_root":"/srv/naivefox/app"}`), &module); err != nil {
		t.Fatal(err)
	}
	if module.ApplicationRoot != "/srv/naivefox/app" {
		t.Fatal("application_root JSON field changed")
	}
	body, err := json.Marshal(module)
	if err != nil || !bytes.Contains(body, []byte(`"application_root":"/srv/naivefox/app"`)) {
		t.Fatalf("application_root JSON round trip: %s %v", body, err)
	}
	for _, invalid := range []string{
		`{"application_root":7}`,
		`{"application_root":["/srv/app"]}`,
	} {
		if err := json.Unmarshal([]byte(invalid), &module); err == nil {
			t.Fatal("invalid application_root JSON accepted")
		}
	}
}
