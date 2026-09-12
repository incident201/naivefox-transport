package transport

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

type applicationAsset struct {
	body []byte
	mime string
}
type applicationFiles struct {
	assets    map[string]applicationAsset
	resources []siteResource
	identity  string
	bodyBytes uint64
	directory *os.Root
}

// readApplicationFile confines named regular files and checks identity before
// and after opening. There is no application size budget or padding.
func readApplicationFile(root *os.Root, name string) ([]byte, error) {
	before, err := root.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: not a regular file or is a symlink", name)
	}
	file, err := openStaticFile(root, name)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return nil, fmt.Errorf("%s: file changed during application load", name)
	}
	body, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}
	final, err := file.Stat()
	if err != nil || int64(len(body)) != after.Size() || final.Size() != after.Size() || !final.ModTime().Equal(after.ModTime()) {
		return nil, fmt.Errorf("%s: file changed during application load", name)
	}
	return body, nil
}

func verifyApplicationFile(root *os.Root, name string, expected []byte) error {
	file, err := openStaticFile(root, name)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != int64(len(expected)) {
		return fmt.Errorf("%s: file changed during application load", name)
	}
	// Verify through a fixed buffer instead of holding a second full site copy.
	var buffer [32768]byte
	offset := 0
	for {
		n, readErr := file.Read(buffer[:])
		if n > len(expected)-offset || !bytes.Equal(buffer[:n], expected[offset:offset+n]) {
			return fmt.Errorf("%s: file changed during application load", name)
		}
		offset += n
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if offset != len(expected) {
		return fmt.Errorf("%s: file changed during application load", name)
	}
	return nil
}

func applicationMIME(name, kind string, body []byte) (string, error) {
	var typ string
	switch kind {
	case "style":
		typ = "text/css"
	case "script":
		typ = "text/javascript"
	case "image":
		typ = mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
		if typ == "" {
			typ = http.DetectContentType(body)
		}
		if !strings.HasPrefix(typ, "image/") {
			return "", fmt.Errorf("%s: expected an image Content-Type", name)
		}
		if strings.HasPrefix(typ, "image/svg+xml") {
			lower := bytes.ToLower(body)
			if !bytes.Contains(lower, []byte("<svg")) || !bytes.Contains(lower, []byte("</svg>")) {
				return "", fmt.Errorf("%s: expected complete SVG markup", name)
			}
		}
	}
	if (kind == "style" || kind == "script") && (!utf8.Valid(body) || bytes.IndexByte(body, 0) >= 0) {
		return "", fmt.Errorf("%s: expected NUL-free UTF-8", name)
	}
	return typ, nil
}

func snapshotField(h hash.Hash, body []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(body)))
	h.Write(length[:])
	h.Write(body)
}

func loadApplication(root string) (application applicationFiles, err error) {
	if root == "" {
		return applicationFiles{}, errors.New("application_root is required")
	}
	if !filepath.IsAbs(root) {
		return applicationFiles{}, errors.New("application_root must be an absolute path")
	}
	directory, err := os.OpenRoot(filepath.Clean(root))
	if err != nil {
		return applicationFiles{}, fmt.Errorf("application_root: %w", err)
	}
	defer func() {
		if err != nil {
			directory.Close()
		}
	}()
	body, err := readApplicationFile(directory, "index.html")
	if err != nil {
		return applicationFiles{}, err
	}
	resources, err := discoverApplication(body)
	if err != nil {
		return applicationFiles{}, err
	}
	application = applicationFiles{assets: map[string]applicationAsset{"/": {body: body, mime: "text/html; charset=utf-8"}}, resources: resources, directory: directory, bodyBytes: uint64(len(body))}
	h := sha256.New()
	snapshotField(h, []byte("naivefox-site"))
	rootDigest := sha256.Sum256(body)
	snapshotField(h, rootDigest[:])
	for _, resource := range resources {
		asset, exists := application.assets[resource.Path]
		if !exists {
			name := filepath.FromSlash(strings.TrimPrefix(resource.Path, "/"))
			data, readErr := readApplicationFile(directory, name)
			if readErr != nil {
				return applicationFiles{}, readErr
			}
			typ, typeErr := applicationMIME(name, resource.Kind, data)
			if typeErr != nil {
				return applicationFiles{}, typeErr
			}
			asset = applicationAsset{body: data, mime: typ}
			application.assets[resource.Path] = asset
		} else {
			typ, typeErr := applicationMIME(resource.Path, resource.Kind, asset.body)
			if typeErr != nil || typ != asset.mime {
				return applicationFiles{}, fmt.Errorf("conflicting resource types for %s", resource.Path)
			}
		}
		if ^uint64(0)-application.bodyBytes < uint64(len(asset.body)) {
			return applicationFiles{}, errors.New("site byte count overflow")
		}
		application.bodyBytes += uint64(len(asset.body))
		snapshotField(h, []byte(resource.URI))
		snapshotField(h, []byte(resource.Kind))
		typ, _, mimeErr := mime.ParseMediaType(asset.mime)
		if mimeErr != nil {
			return applicationFiles{}, mimeErr
		}
		snapshotField(h, []byte(typ))
		digest := sha256.Sum256(asset.body)
		snapshotField(h, digest[:])
	}
	for resourcePath, asset := range application.assets {
		name := strings.TrimPrefix(resourcePath, "/")
		if resourcePath == "/" {
			name = "index.html"
		}
		if err := verifyApplicationFile(directory, filepath.FromSlash(name), asset.body); err != nil {
			return applicationFiles{}, err
		}
	}
	application.identity = hex.EncodeToString(h.Sum(nil))
	return application, nil
}

func (application applicationFiles) asset(path string) (applicationAsset, bool) {
	asset, ok := application.assets[path]
	return asset, ok
}
func (application applicationFiles) close() error {
	if application.directory != nil {
		return application.directory.Close()
	}
	return nil
}
