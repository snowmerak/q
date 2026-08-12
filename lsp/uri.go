package lsp

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

// FileURI returns the canonical file URI for path. Relative paths are first
// resolved against the current working directory.
func FileURI(path string) (DocumentURI, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("lsp: file URI path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("lsp: resolve file URI path: %w", err)
	}
	slashed := filepath.ToSlash(filepath.Clean(absolute))
	uri := url.URL{Scheme: "file"}
	if strings.HasPrefix(slashed, "//") {
		hostAndPath := strings.TrimPrefix(slashed, "//")
		host, uriPath, found := strings.Cut(hostAndPath, "/")
		if !found || host == "" {
			return "", fmt.Errorf("lsp: invalid UNC file path %q", path)
		}
		uri.Host = host
		uri.Path = "/" + uriPath
	} else {
		if filepath.VolumeName(absolute) != "" && !strings.HasPrefix(slashed, "/") {
			slashed = "/" + slashed
		}
		uri.Path = slashed
	}
	return DocumentURI(uri.String()), nil
}
