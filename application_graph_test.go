package transport

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHTMLSiteDiscovery(t *testing.T) {
	cases := []struct {
		name, body string
		want       []string
		invalid    bool
	}{
		{"root-only", "<!doctype html><p>hello</p>", nil, false},
		{"declarations", "<!doctype html><link rel=stylesheet href=css/main.css><script defer src=/js/main.js></script><link rel=preload as=image href=/hero.webp><img src=/hero.webp><link rel=icon href=/favicon.ico>", []string{"/css/main.css", "/js/main.js", "/hero.webp", "/favicon.ico"}, false},
		{"inert", "<!doctype html><template><div><img src=/ignored.png></div></template><noscript><img src=/also-ignored.png></noscript><script>const x='<img src=/fake.png>';</script><a href=/next>next</a><!-- <img src=/comment.png> --><img src=/real.png>", []string{"/real.png"}, false},
		{"unicode-query", "<!doctype html><img src='pics/caf\u00e9.png?a=1&amp;b=2#part'>", []string{"/pics/caf%C3%A9.png?a=1&b=2"}, false},
		{"duplicate-attribute", "<!doctype html><img src=/first.png src=/second.png>", []string{"/first.png"}, false},
		{"data-icon", "<!doctype html><link rel=icon href='data:,'>", nil, false},
		{"large-direct", "<!doctype html><img src=/huge.png>", []string{"/huge.png"}, false},
		{"css-js-leaves", "<!doctype html><link rel=stylesheet href=app.css><script defer src=app.js></script>", []string{"/app.css", "/app.js"}, false},
		{"traversal", "<img src='/assets/%2e%2e/private.png'>", nil, true},
		{"external", "<img src=https://other.example/image.png>", nil, true},
		{"scheme-relative", "<img src=//other.example/image.png>", nil, true},
		{"reserved", "<script defer src=/api/sync></script>", nil, true},
		{"base", "<base href=/other/><img src=x.png>", nil, true},
		{"lazy", "<img loading=lazy src=a.png>", nil, true},
		{"srcset", "<img src=a.png srcset='b.png 2x'>", nil, true},
		{"module", "<script type=module src=a.js></script>", nil, true},
		{"async", "<script async src=a.js></script>", nil, true},
		{"iframe", "<iframe src=child.html></iframe>", nil, true},
		{"conflict", "<link rel=stylesheet href=same><script defer src=same></script>", nil, true},
		{"meta-refresh", "<meta http-equiv=refresh content='0;url=next'>", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := discoverApplication([]byte(tc.body))
			if tc.invalid {
				if err == nil {
					t.Fatal("invalid graph accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("resources=%v, want=%v", got, tc.want)
			}
			for i, want := range tc.want {
				if got[i].URI != want {
					t.Fatalf("resource %d=%q, want %q", i, got[i].URI, want)
				}
			}
		})
	}
}

func TestHTMLSiteNoFixedBudgetOrResourceCount(t *testing.T) {
	root := t.TempDir()
	var html strings.Builder
	html.WriteString("<!doctype html><title>Large valid site</title>")
	html.WriteString(strings.Repeat(" ", 20*1024))
	for i := 0; i < 24; i++ {
		name := fmt.Sprintf("code-%02d.js", i)
		html.WriteString("<script defer src='" + name + "'></script>")
		writeApplicationFile(t, root, name, bytes.Repeat([]byte("/* public script */\n"), 8192))
	}
	writeApplicationFile(t, root, "index.html", []byte(html.String()))
	// Unreferenced contents are not loaded or validated, even if they are invalid.
	writeApplicationFile(t, root, "unreferenced.bin", []byte{0xff, 0})
	site, err := loadApplication(root)
	if err != nil {
		t.Fatal(err)
	}
	defer site.close()
	if len(site.resources) != 24 || len(site.assets) != 25 || site.bodyBytes <= 72*1024 {
		t.Fatalf("incomplete site: resources=%d bytes=%d", len(site.resources), site.bodyBytes)
	}
	if _, ok := site.assets["/unreferenced.bin"]; ok {
		t.Fatal("directory crawler loaded unreferenced content")
	}
	for _, r := range site.resources {
		a, _ := site.asset(r.Path)
		if len(a.body) <= 64*1024 {
			t.Fatal("large resource was truncated")
		}
	}
}

func TestHTMLSiteRootOnlyAndSnapshotIdentity(t *testing.T) {
	root := t.TempDir()
	writeApplicationFile(t, root, "index.html", []byte("<!doctype html><p>Standalone</p>"))
	a, err := loadApplication(root)
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	b, err := loadApplication(root)
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()
	if len(a.resources) != 0 || len(a.assets) != 1 || len(a.identity) != 64 || a.identity != b.identity {
		t.Fatal("root-only snapshot or stable identity failed")
	}
	writeApplicationFile(t, root, "index.html", []byte("<!doctype html><p>Changed</p>"))
	c, err := loadApplication(root)
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	if a.identity == c.identity {
		t.Fatal("modified snapshot reused identity")
	}
	if a.identity != b.identity {
		t.Fatal("source edit mutated retained snapshot")
	}
}

func TestHTMLSiteQueryDedupAndSelectedSnapshot(t *testing.T) {
	root := t.TempDir()
	writeApplicationFile(t, root, "index.html", []byte("<!doctype html><script defer src='app.js?v=1'></script><script defer src='app.js?v=1#part'></script><script defer src='app.js?v=2'></script>"))
	writeApplicationFile(t, root, "app.js", []byte("// source"))
	m := provisionApplicationSite(t, root)
	if len(m.application.resources) != 2 || len(m.application.assets) != 2 {
		t.Fatal("request identities and source dedup disagree")
	}
	if m.application.bodyBytes != uint64(len(m.application.assets["/"].body)+2*len("// source")) {
		t.Fatal("query variants must count actual response bytes")
	}
	first := requestApplicationSite(t, m, "GET", "/app.js?v=1", nil)
	if first.Header().Get("X-App-Site") != m.application.identity || first.Body.String() != "// source" {
		t.Fatal("selected representation identity")
	}
	if err := os.Remove(filepath.Join(root, "app.js")); err != nil {
		t.Fatal(err)
	}
	again := requestApplicationSite(t, m, "GET", "/app.js?v=2", nil)
	if !bytes.Equal(first.Body.Bytes(), again.Body.Bytes()) {
		t.Fatal("selected file read live instead of snapshot")
	}
}
