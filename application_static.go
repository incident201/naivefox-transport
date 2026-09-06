package transport

import (
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

func isCarrierPath(path string) bool {
	return path == "/api/sync" || path == "/api/events/brief" ||
		path == "/api/events/state" || strings.HasPrefix(path, "/media/chunk/")
}

func (application applicationFiles) isReservedApplicationPath(path string) bool {
	_, asset := application.asset(path)
	return asset || path == "/index.html" || path == "/api/realtime" ||
		isCarrierPath(path) || strings.HasPrefix(path, "/__lab/")
}

func redirectApplication(w http.ResponseWriter, r *http.Request, target string) {
	location := (&url.URL{Path: target, RawQuery: r.URL.RawQuery, ForceQuery: r.URL.ForceQuery}).String()
	http.Redirect(w, r, location, http.StatusPermanentRedirect)
}

// serveStatic is reached only after the existing transport routes and CONNECT.
// Missing files and unsupported methods leave the original fallback untouched.
func (application applicationFiles) serveStatic(w http.ResponseWriter, r *http.Request) (bool, error) {
	if (r.Method != http.MethodGet && r.Method != http.MethodHead) || application.directory == nil {
		return false, nil
	}
	requestPath := r.URL.Path // net/http has already decoded URL escapes once.
	if !strings.HasPrefix(requestPath, "/") || strings.ContainsAny(requestPath, "\\\x00") {
		return true, caddyhttp.Error(http.StatusBadRequest, nil)
	}
	for _, component := range strings.Split(requestPath, "/") {
		if component == ".." {
			return true, caddyhttp.Error(http.StatusBadRequest, nil)
		}
	}
	canonical := path.Clean(requestPath)
	if canonical == "/index.html" {
		canonical = "/"
	} else if strings.HasSuffix(requestPath, "/") && canonical != "/" && !application.isReservedApplicationPath(canonical) {
		canonical += "/"
	}
	// Redirect before touching disk: normalized carrier and asset aliases must
	// re-enter the transport router, never expose an unpadded source file.
	if canonical != requestPath {
		redirectApplication(w, r, canonical)
		return true, nil
	}
	name := strings.TrimPrefix(canonical, "/")
	info, err := application.directory.Stat(name)
	if err != nil {
		return staticLookupError(err)
	}
	if info.IsDir() {
		name = path.Join(name, "index.html")
		info, err = application.directory.Stat(name)
		if err != nil {
			return staticLookupError(err)
		}
		if !info.Mode().IsRegular() {
			return true, caddyhttp.Error(http.StatusNotFound, nil)
		}
		if !strings.HasSuffix(requestPath, "/") {
			redirectApplication(w, r, requestPath+"/")
			return true, nil
		}
	}
	if !info.Mode().IsRegular() {
		return true, caddyhttp.Error(http.StatusNotFound, nil)
	}
	file, err := openStaticFile(application.directory, name)
	if err != nil {
		return staticLookupError(err)
	}
	defer file.Close()
	// Inspect the opened file too, since an operator may replace it between
	// Stat and Open. Unix opens are nonblocking even if replaced by a FIFO.
	info, err = file.Stat()
	if err != nil {
		return true, caddyhttp.Error(http.StatusInternalServerError, err)
	}
	if !info.Mode().IsRegular() {
		return true, caddyhttp.Error(http.StatusNotFound, nil)
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, name, info.ModTime(), file)
	return true, nil
}

func staticLookupError(err error) (bool, error) {
	if os.IsNotExist(err) {
		return false, nil
	}
	// Includes unreadable files and confined-root rejections. Never try an
	// unrestricted filesystem fallback after a denied lookup.
	return true, caddyhttp.Error(http.StatusForbidden, err)
}
