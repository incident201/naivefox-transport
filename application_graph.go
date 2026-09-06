package transport

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
)

type siteResource struct {
	URI  string
	Path string
	Kind string
}

// discoverApplication reads only the initial document's declarative resources.
// CSS, scripts, images and other pages are never recursively traversed.
func discoverApplication(body []byte) ([]siteResource, error) {
	if len(body) == 0 || !utf8.Valid(body) || bytes.IndexByte(body, 0) >= 0 {
		return nil, errors.New("index.html must be nonempty NUL-free UTF-8")
	}
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	var resources []siteResource
	seen := map[string]string{}
	var visit func(*html.Node) error
	visit = func(n *html.Node) error {
		if n.Type == html.ElementNode {
			if n.Data == "template" || n.Data == "noscript" {
				return nil
			}
			attrs := map[string]string{}
			for _, a := range n.Attr {
				// HTML uses the first occurrence of an attribute.
				if _, exists := attrs[a.Key]; !exists {
					attrs[a.Key] = a.Val
				}
			}
			ref, kind, err := applicationDeclaration(n.Data, n.Namespace, attrs)
			if err != nil {
				return err
			}
			if ref != "" {
				resource, err := applicationReference(ref, kind)
				if err != nil {
					return err
				}
				if resource.URI != "" {
					previous, exists := seen[resource.URI]
					if exists && previous != resource.Kind {
						return fmt.Errorf("conflicting resource kinds for %s", resource.URI)
					}
					if !exists {
						seen[resource.URI] = resource.Kind
						resources = append(resources, resource)
					}
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			if err := visit(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(doc); err != nil {
		return nil, err
	}
	return resources, nil
}

func applicationDeclaration(tag, namespace string, a map[string]string) (string, string, error) {
	fail := func() (string, string, error) {
		return "", "", fmt.Errorf("unsupported automatic resource declaration: <%s>", tag)
	}
	has := func(key string) bool { _, ok := a[key]; return ok }
	unsupported := func(keys ...string) bool {
		for _, k := range keys {
			if has(k) {
				return true
			}
		}
		return false
	}
	if namespace != "" {
		if unsupported("href", "src") {
			return fail()
		}
		return "", "", nil
	}
	if tag == "base" {
		return fail()
	}
	if tag == "meta" && (strings.EqualFold(a["http-equiv"], "refresh") || strings.EqualFold(a["http-equiv"], "content-security-policy") || strings.EqualFold(a["name"], "referrer")) {
		return fail()
	}
	if tag == "iframe" || tag == "frame" || tag == "object" || tag == "embed" || tag == "audio" || tag == "video" || tag == "source" || tag == "picture" {
		return fail()
	}
	kind, ref := "", ""
	switch tag {
	case "link":
		rel := strings.ToLower(strings.Join(strings.Fields(a["rel"]), " "))
		switch rel {
		case "stylesheet":
			if unsupported("disabled", "media", "title") {
				return fail()
			}
			kind, ref = "style", a["href"]
		case "preload":
			if !strings.EqualFold(a["as"], "image") || unsupported("imagesrcset", "imagesizes", "media") {
				return fail()
			}
			kind, ref = "image", a["href"]
		case "icon", "shortcut icon":
			if unsupported("media", "sizes") {
				return fail()
			}
			kind, ref = "image", a["href"]
		case "", "canonical", "alternate", "author", "help", "license", "next", "prev", "search":
			return "", "", nil
		default:
			return fail()
		}
	case "script":
		if !has("src") {
			if strings.EqualFold(a["type"], "module") || strings.EqualFold(a["type"], "importmap") || strings.EqualFold(a["type"], "speculationrules") {
				return fail()
			}
			return "", "", nil
		}
		if !has("defer") || unsupported("async", "nomodule") || (a["type"] != "" && !strings.EqualFold(a["type"], "text/javascript") && !strings.EqualFold(a["type"], "application/javascript")) {
			return fail()
		}
		kind, ref = "script", a["src"]
	case "img":
		if unsupported("srcset", "sizes") || (has("loading") && !strings.EqualFold(a["loading"], "eager")) {
			return fail()
		}
		kind, ref = "image", a["src"]
	case "input":
		if strings.EqualFold(a["type"], "image") {
			return fail()
		}
		return "", "", nil
	default:
		return "", "", nil
	}
	if unsupported("crossorigin", "integrity", "nonce", "referrerpolicy", "fetchpriority", "charset") || strings.TrimSpace(ref) == "" {
		return fail()
	}
	return ref, kind, nil
}

func applicationReference(raw, kind string) (siteResource, error) {
	raw = strings.Trim(raw, " \t\r\n\f")
	if strings.HasPrefix(strings.ToLower(raw), "data:") {
		return siteResource{}, nil
	}
	if strings.ContainsAny(raw, "\\\x00\r\n\t") || strings.HasPrefix(raw, "//") {
		return siteResource{}, errors.New("resource URL must be a local relative or root-relative URL")
	}
	u, err := url.Parse(raw)
	if err != nil || u.IsAbs() || u.Host != "" || u.User != nil || u.Opaque != "" {
		return siteResource{}, fmt.Errorf("invalid local resource URL %q", raw)
	}
	for _, component := range strings.Split(u.Path, "/") {
		if component == ".." {
			return siteResource{}, errors.New("resource path traversal")
		}
	}
	base := &url.URL{Path: "/"}
	u = base.ResolveReference(u)
	u.Fragment, u.RawFragment = "", ""
	if u.Path == "/" || u.Path == "/index.html" || isCarrierPath(u.Path) || u.Path == "/api/realtime" || strings.HasPrefix(u.Path, "/__lab/") {
		return siteResource{}, fmt.Errorf("resource collides with reserved route %s", u.Path)
	}
	if strings.ContainsAny(u.Path, "\\\x00") || path.Clean(u.Path) != u.Path || strings.HasSuffix(u.Path, "/") {
		return siteResource{}, errors.New("resource must name a canonical regular file")
	}
	return siteResource{URI: u.RequestURI(), Path: u.Path, Kind: kind}, nil
}
