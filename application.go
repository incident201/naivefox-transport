package transport

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"
)

const (
	rootCapacity   = 4096
	styleCapacity  = 12288
	scriptCapacity = 24576
	imageCapacity  = 8192
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

type applicationAsset struct {
	body []byte
	mime string
}

type applicationFiles struct {
	assets map[string]applicationAsset
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

func readApplicationSources(root *os.Root) (map[string][]byte, error) {
	sources := make(map[string][]byte, len(applicationAssetSpecs))
	for _, spec := range applicationAssetSpecs {
		body, err := readApplicationFile(root, spec)
		if err != nil {
			return nil, err
		}
		sources[spec.path] = body
	}
	return sources, nil
}

func paddedApplicationAsset(body []byte, capacity int) []byte {
	padded := bytes.Repeat([]byte{' '}, capacity)
	copy(padded, body)
	return padded
}

func loadApplication(root string) (applicationFiles, error) {
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

	sources, err := readApplicationSources(directory)
	if err != nil {
		return applicationFiles{}, err
	}
	verification, err := readApplicationSources(directory)
	if err != nil {
		return applicationFiles{}, err
	}
	for _, spec := range applicationAssetSpecs {
		if !bytes.Equal(sources[spec.path], verification[spec.path]) {
			return applicationFiles{}, fmt.Errorf("%s: file changed during application load", spec.file)
		}
	}
	for _, spec := range applicationAssetSpecs[1:] {
		if bytes.Count(sources["/"], []byte(spec.path)) != 1 {
			return applicationFiles{}, fmt.Errorf("index.html: expected exactly one reference to %s", spec.path)
		}
	}

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
