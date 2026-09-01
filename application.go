package transport

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	rootCapacity         = 4096
	styleCapacity        = 12288
	scriptCapacity       = 24576
	imageCapacity        = 8192
	manifestCapacity     = 65536
	applicationFormat    = "naivefox-application-v1"
	applicationWireGraph = "navigation-assets-api-v1"
	applicationManifest  = "application.json"
	profileMarker        = "__NFC_PROFILE__"
	readerMarker         = "__NFC_READER__"
	lifecycleMarker      = "__NFC_LIFECYCLE__"
)

type assetSpec struct {
	path string
	file string
	size int
	mime string
	svg  bool
}

var applicationAssetSpecs = []assetSpec{
	{path: "/", file: "index.html", size: rootCapacity, mime: "text/html; charset=utf-8"},
	{path: "/assets/site.css", file: "assets/site.css", size: styleCapacity, mime: "text/css"},
	{path: "/assets/app.js", file: "assets/app.js", size: scriptCapacity, mime: "text/javascript"},
	{path: "/assets/image-1.svg", file: "assets/image-1.svg", size: imageCapacity, mime: "image/svg+xml", svg: true},
	{path: "/assets/image-2.svg", file: "assets/image-2.svg", size: imageCapacity, mime: "image/svg+xml", svg: true},
	{path: "/assets/image-3.svg", file: "assets/image-3.svg", size: imageCapacity, mime: "image/svg+xml", svg: true},
	{path: "/assets/image-4.svg", file: "assets/image-4.svg", size: imageCapacity, mime: "image/svg+xml", svg: true},
}

var applicationRuntimeSpecs = []assetSpec{
	{file: "runtime/read-cell.js", size: scriptCapacity},
	{file: "runtime/lifecycle.js", size: scriptCapacity},
}

type applicationAsset struct {
	body []byte
	mime string
}

type applicationFiles struct {
	assets map[string]applicationAsset
}

type applicationManifestFile struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type applicationManifestDocument struct {
	Format    string                             `json:"format"`
	WireGraph string                             `json:"wire_graph"`
	Files     map[string]applicationManifestFile `json:"files"`
}

func assetDefinition(path string) (assetSpec, bool) {
	for _, spec := range applicationAssetSpecs {
		if spec.path == path {
			return spec, true
		}
	}
	return assetSpec{}, false
}

func readApplicationFile(root *os.Root, spec assetSpec) ([]byte, error) {
	name := filepath.FromSlash(spec.file)
	before, err := root.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", spec.file, err)
	}
	if before.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s: symbolic links are not allowed", spec.file)
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: not a regular file", spec.file)
	}
	if before.Size() <= 0 {
		return nil, fmt.Errorf("%s: file is empty", spec.file)
	}
	if before.Size() > int64(spec.size) {
		return nil, fmt.Errorf("%s: source exceeds %d bytes", spec.file, spec.size)
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", spec.file, err)
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", spec.file, err)
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return nil, fmt.Errorf("%s: file changed during application load", spec.file)
	}
	body, err := io.ReadAll(io.LimitReader(file, int64(spec.size+1)))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", spec.file, err)
	}
	if len(body) != int(after.Size()) {
		return nil, fmt.Errorf("%s: file changed during application load", spec.file)
	}
	if len(body) > spec.size {
		return nil, fmt.Errorf("%s: source exceeds %d bytes", spec.file, spec.size)
	}
	if !utf8.Valid(body) || bytes.IndexByte(body, 0) >= 0 {
		return nil, fmt.Errorf("%s: expected NUL-free UTF-8 text", spec.file)
	}
	if spec.svg {
		lower := bytes.ToLower(body)
		if !bytes.Contains(lower, []byte("<svg")) || !bytes.Contains(lower, []byte("</svg>")) {
			return nil, fmt.Errorf("%s: expected complete SVG markup", spec.file)
		}
	}
	return body, nil
}

func expectedApplicationManifestFiles() map[string]assetSpec {
	expected := make(map[string]assetSpec, len(applicationAssetSpecs)+len(applicationRuntimeSpecs))
	for _, spec := range applicationAssetSpecs {
		expected[spec.file] = spec
	}
	for _, spec := range applicationRuntimeSpecs {
		expected[spec.file] = spec
	}
	return expected
}

func validateUniqueJSONKeys(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	var value func() error
	value = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := make(map[string]bool)
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("non-string JSON object key")
				}
				if seen[key] {
					return fmt.Errorf("duplicate JSON key %q", key)
				}
				seen[key] = true
				if err := value(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := value(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return errors.New("unexpected JSON delimiter")
		}
	}
	if err := value(); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return errors.New("trailing JSON")
		}
		return err
	}
	return nil
}

func readApplicationManifest(root *os.Root) ([]byte, applicationManifestDocument, error) {
	body, err := readApplicationFile(root, assetSpec{file: applicationManifest, size: manifestCapacity})
	if err != nil {
		return nil, applicationManifestDocument{}, err
	}
	if err := validateUniqueJSONKeys(body); err != nil {
		return nil, applicationManifestDocument{}, fmt.Errorf("%s: %w", applicationManifest, err)
	}
	var manifest applicationManifestDocument
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return nil, applicationManifestDocument{}, fmt.Errorf("%s: %w", applicationManifest, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, applicationManifestDocument{}, fmt.Errorf("%s: trailing JSON", applicationManifest)
	}
	if manifest.Format != applicationFormat {
		return nil, applicationManifestDocument{}, fmt.Errorf("%s: unsupported format %q", applicationManifest, manifest.Format)
	}
	if manifest.WireGraph != applicationWireGraph {
		return nil, applicationManifestDocument{}, fmt.Errorf("%s: unsupported wire_graph %q", applicationManifest, manifest.WireGraph)
	}
	expected := expectedApplicationManifestFiles()
	if len(manifest.Files) != len(expected) {
		return nil, applicationManifestDocument{}, fmt.Errorf("%s: expected exactly %d file entries", applicationManifest, len(expected))
	}
	for relative := range expected {
		entry, ok := manifest.Files[relative]
		if !ok {
			return nil, applicationManifestDocument{}, fmt.Errorf("%s: missing %s entry", applicationManifest, relative)
		}
		digest, err := hex.DecodeString(entry.SHA256)
		if err != nil || len(digest) != sha256.Size || strings.ToLower(entry.SHA256) != entry.SHA256 {
			return nil, applicationManifestDocument{}, fmt.Errorf("%s: invalid SHA-256 for %s", applicationManifest, relative)
		}
		if entry.Size <= 0 || entry.Size > int64(expected[relative].size) {
			return nil, applicationManifestDocument{}, fmt.Errorf("%s: invalid size for %s", applicationManifest, relative)
		}
	}
	for relative := range manifest.Files {
		if _, ok := expected[relative]; !ok {
			return nil, applicationManifestDocument{}, fmt.Errorf("%s: unexpected %s entry", applicationManifest, relative)
		}
	}
	return body, manifest, nil
}

func verifyApplicationManifestFile(relative string, body []byte, entry applicationManifestFile) error {
	if int64(len(body)) != entry.Size {
		return fmt.Errorf("%s: size does not match %s", relative, applicationManifest)
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != entry.SHA256 {
		return fmt.Errorf("%s: SHA-256 does not match %s", relative, applicationManifest)
	}
	return nil
}

func replaceApplicationMarker(body []byte, marker string, replacement []byte) ([]byte, error) {
	token := []byte(marker)
	if bytes.Count(body, token) != 1 {
		return nil, fmt.Errorf("assets/app.js: expected exactly one %s marker", marker)
	}
	return bytes.Replace(body, token, replacement, 1), nil
}

func paddedApplicationAsset(body []byte, capacity int) []byte {
	padded := bytes.Repeat([]byte{' '}, capacity)
	copy(padded, body)
	return padded
}

func loadApplication(root string, profile appProfile) (applicationFiles, error) {
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
	defer directory.Close()

	manifestBody, manifest, err := readApplicationManifest(directory)
	if err != nil {
		return applicationFiles{}, err
	}
	sources := make(map[string][]byte, len(applicationAssetSpecs))
	for _, spec := range applicationAssetSpecs {
		body, err := readApplicationFile(directory, spec)
		if err != nil {
			return applicationFiles{}, err
		}
		if err := verifyApplicationManifestFile(spec.file, body, manifest.Files[spec.file]); err != nil {
			return applicationFiles{}, err
		}
		sources[spec.path] = body
	}
	for _, spec := range applicationAssetSpecs[1:] {
		if bytes.Count(sources["/"], []byte(spec.path)) != 1 {
			return applicationFiles{}, fmt.Errorf("index.html: expected exactly one reference to %s", spec.path)
		}
	}

	runtime := make(map[string][]byte, len(applicationRuntimeSpecs))
	for _, spec := range applicationRuntimeSpecs {
		body, err := readApplicationFile(directory, spec)
		if err != nil {
			return applicationFiles{}, err
		}
		if err := verifyApplicationManifestFile(spec.file, body, manifest.Files[spec.file]); err != nil {
			return applicationFiles{}, err
		}
		runtime[spec.file] = body
	}
	finalManifestBody, _, err := readApplicationManifest(directory)
	if err != nil {
		return applicationFiles{}, err
	}
	if !bytes.Equal(manifestBody, finalManifestBody) {
		return applicationFiles{}, errors.New("application.json changed during application load")
	}

	script := sources["/assets/app.js"]
	encodedProfile, err := json.Marshal(profile)
	if err != nil {
		return applicationFiles{}, err
	}
	for _, replacement := range []struct {
		marker string
		body   []byte
	}{
		{readerMarker, runtime["runtime/read-cell.js"]},
		{lifecycleMarker, runtime["runtime/lifecycle.js"]},
		{profileMarker, encodedProfile},
	} {
		script, err = replaceApplicationMarker(script, replacement.marker, replacement.body)
		if err != nil {
			return applicationFiles{}, err
		}
	}
	if len(script) > scriptCapacity {
		return applicationFiles{}, fmt.Errorf("assembled assets/app.js exceeds %d bytes", scriptCapacity)
	}
	sources["/assets/app.js"] = script

	application := applicationFiles{assets: make(map[string]applicationAsset, len(applicationAssetSpecs))}
	for _, spec := range applicationAssetSpecs {
		application.assets[spec.path] = applicationAsset{
			body: paddedApplicationAsset(sources[spec.path], spec.size),
			mime: spec.mime,
		}
	}
	return application, nil
}

func (application applicationFiles) asset(path string) (applicationAsset, bool) {
	asset, ok := application.assets[path]
	return asset, ok
}
