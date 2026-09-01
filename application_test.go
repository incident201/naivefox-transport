package transport

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
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
	"application.json",
	"index.html",
	"assets/site.css",
	"assets/app.js",
	"assets/image-1.svg",
	"assets/image-2.svg",
	"assets/image-3.svg",
	"assets/image-4.svg",
	"runtime/read-cell.js",
	"runtime/lifecycle.js",
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

func refreshApplicationManifest(t *testing.T, root string) {
	t.Helper()
	manifest := applicationManifestDocument{
		Format: applicationFormat, WireGraph: applicationWireGraph,
		Files: make(map[string]applicationManifestFile),
	}
	for relative := range expectedApplicationManifestFiles() {
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(body)
		manifest.Files[relative] = applicationManifestFile{
			Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:]),
		}
	}
	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	body = append(body, '\n')
	if err := os.WriteFile(filepath.Join(root, applicationManifest), body, 0640); err != nil {
		t.Fatal(err)
	}
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
	custom := []byte("<!doctype html><title>Custom</title><link rel=stylesheet href=/assets/site.css><script src=/assets/app.js></script><img src=/assets/image-1.svg><img src=/assets/image-2.svg><img src=/assets/image-3.svg><img src=/assets/image-4.svg>")
	writeApplicationFile(t, root, "index.html", custom)
	refreshApplicationManifest(t, root)
	profile := profiles[defaultProfile]
	module := &Transport{ApplicationRoot: root, ForwardProxy: testForwardProxy()}
	if err := module.Provision(testCaddyContext(t)); err != nil {
		t.Fatal(err)
	}
	defer module.Cleanup()
	application := module.application
	encodedProfile, _ := json.Marshal(profile)
	for _, spec := range applicationAssetSpecs {
		asset, ok := application.asset(spec.path)
		if !ok || len(asset.body) != spec.size || asset.mime != spec.mime {
			t.Fatalf("asset contract changed for %s", spec.path)
		}
	}
	script, _ := application.asset("/assets/app.js")
	if !bytes.Contains(script.body, encodedProfile) {
		t.Fatal("profile was not injected")
	}
	for _, marker := range []string{profileMarker, readerMarker, lifecycleMarker} {
		if bytes.Contains(script.body, []byte(marker)) {
			t.Fatalf("unexpanded marker %s", marker)
		}
	}
	rootAsset, _ := application.asset("/")
	if !bytes.HasPrefix(rootAsset.body, custom) {
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
	if response.Code != http.StatusOK || response.Body.Len() != rootCapacity || !bytes.HasPrefix(response.Body.Bytes(), custom) {
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

func TestProvisionRequiresExternalApplicationRoot(t *testing.T) {
	module := &Transport{ForwardProxy: testForwardProxy()}
	err := module.Provision(testCaddyContext(t))
	if err == nil || !strings.Contains(err.Error(), "application_root is required") {
		t.Fatalf("missing application_root provision result: %v", err)
	}
}

func TestExternalApplicationRequiresCompleteValidatedBundle(t *testing.T) {
	if _, err := loadApplication("", profiles[defaultProfile]); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatal("missing application_root accepted")
	}
	if _, err := loadApplication("relative/template", profiles[defaultProfile]); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatal("relative application_root accepted")
	}
	notDirectory := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notDirectory, []byte("file"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadApplication(notDirectory, profiles[defaultProfile]); err == nil || !strings.Contains(err.Error(), "directory") {
		t.Fatal("non-directory application_root accepted")
	}

	for _, relative := range applicationTemplateFiles {
		t.Run("missing-"+strings.ReplaceAll(relative, "/", "-"), func(t *testing.T) {
			root := copyApplicationTemplate(t)
			if err := os.Remove(filepath.Join(root, filepath.FromSlash(relative))); err != nil {
				t.Fatal(err)
			}
			if _, err := loadApplication(root, profiles[defaultProfile]); err == nil || !strings.Contains(err.Error(), relative) {
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
			errorPart: "invalid size",
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
		{
			name: "missing-profile-marker", relative: "assets/app.js",
			body:      func(body []byte) []byte { return bytes.ReplaceAll(body, []byte(profileMarker), []byte("null")) },
			errorPart: profileMarker,
		},
		{
			name: "duplicate-reader-marker", relative: "assets/app.js",
			body:      func(body []byte) []byte { return append(body, []byte("\n"+readerMarker+"\n")...) },
			errorPart: readerMarker,
		},
		{
			name: "assembled-script-overflow", relative: "assets/app.js",
			body: func([]byte) []byte {
				return []byte(readerMarker + "\n" + lifecycleMarker + "\nconst profile=" + profileMarker + ";\n" + strings.Repeat("x", 15000))
			},
			errorPart: "assembled",
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
			refreshApplicationManifest(t, root)
			if _, err := loadApplication(root, profiles[defaultProfile]); err == nil || !strings.Contains(err.Error(), test.errorPart) {
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
		if _, err := loadApplication(root, profiles[defaultProfile]); err == nil {
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
		if _, err := loadApplication(root, profiles[defaultProfile]); err == nil || !strings.Contains(err.Error(), "regular") {
			t.Fatalf("directory asset accepted: %v", err)
		}
	})
}

func TestApplicationManifestStrictValidation(t *testing.T) {
	cases := []struct {
		name      string
		mutate    func(map[string]any)
		trailing  bool
		errorPart string
	}{
		{
			name:      "unknown-field",
			mutate:    func(document map[string]any) { document["unknown"] = true },
			errorPart: "unknown field",
		},
		{
			name:      "wrong-format",
			mutate:    func(document map[string]any) { document["format"] = "other" },
			errorPart: "unsupported format",
		},
		{
			name:      "wrong-wire-graph",
			mutate:    func(document map[string]any) { document["wire_graph"] = "other" },
			errorPart: "unsupported wire_graph",
		},
		{
			name: "missing-entry",
			mutate: func(document map[string]any) {
				delete(document["files"].(map[string]any), "assets/site.css")
			},
			errorPart: "exactly",
		},
		{
			name: "extra-entry",
			mutate: func(document map[string]any) {
				document["files"].(map[string]any)["extra.js"] = map[string]any{
					"size": 1, "sha256": strings.Repeat("0", 64),
				}
			},
			errorPart: "exactly",
		},
		{
			name: "bad-hash",
			mutate: func(document map[string]any) {
				document["files"].(map[string]any)["assets/site.css"].(map[string]any)["sha256"] = "bad"
			},
			errorPart: "invalid SHA-256",
		},
		{
			name: "noncanonical-hash",
			mutate: func(document map[string]any) {
				entry := document["files"].(map[string]any)["assets/site.css"].(map[string]any)
				entry["sha256"] = strings.ToUpper(entry["sha256"].(string))
			},
			errorPart: "invalid SHA-256",
		},
		{
			name: "wrong-size",
			mutate: func(document map[string]any) {
				entry := document["files"].(map[string]any)["assets/site.css"].(map[string]any)
				entry["size"] = entry["size"].(float64) + 1
			},
			errorPart: "size does not match",
		},
		{
			name:     "trailing-json",
			mutate:   func(map[string]any) {},
			trailing: true, errorPart: "trailing JSON",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			root := copyApplicationTemplate(t)
			path := filepath.Join(root, applicationManifest)
			var document map[string]any
			if err := json.Unmarshal(mustReadFile(t, path), &document); err != nil {
				t.Fatal(err)
			}
			test.mutate(document)
			body, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if test.trailing {
				body = append(body, []byte("{}")...)
			}
			if err := os.WriteFile(path, body, 0640); err != nil {
				t.Fatal(err)
			}
			if _, err := loadApplication(root, profiles[defaultProfile]); err == nil || !strings.Contains(err.Error(), test.errorPart) {
				t.Fatalf("invalid manifest accepted: %v", err)
			}
		})
	}

	t.Run("duplicate-key", func(t *testing.T) {
		root := copyApplicationTemplate(t)
		path := filepath.Join(root, applicationManifest)
		body := mustReadFile(t, path)
		body = bytes.Replace(body, []byte("{"), []byte("{\n  \"format\": \"duplicate\","), 1)
		if err := os.WriteFile(path, body, 0640); err != nil {
			t.Fatal(err)
		}
		if _, err := loadApplication(root, profiles[defaultProfile]); err == nil || !strings.Contains(err.Error(), "duplicate JSON key") {
			t.Fatalf("duplicate manifest key accepted: %v", err)
		}
	})
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
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
