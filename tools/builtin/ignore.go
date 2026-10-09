package builtin

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/snowmerak/q/internal/qignore"
)

const workspaceIgnoreFile = ".qignore"

type discoveryIgnore struct{ patterns qignore.Matcher }

func loadDiscoveryIgnore(root string) (discoveryIgnore, error) {
	body, err := os.ReadFile(filepath.Join(root, workspaceIgnoreFile))
	if errors.Is(err, os.ErrNotExist) {
		return discoveryIgnore{}, nil
	}
	if err != nil {
		return discoveryIgnore{}, err
	}
	return parseDiscoveryIgnore(string(body)), nil
}

func parseDiscoveryIgnore(body string) discoveryIgnore {
	return discoveryIgnore{patterns: qignore.Parse(body)}
}

func (ignore discoveryIgnore) matches(path string, directory bool) bool {
	path = strings.TrimPrefix(filepath.ToSlash(filepath.Clean(path)), "./")
	if path == ".q" && directory {
		return true
	}
	return ignore.patterns.Matches(path, directory)
}
