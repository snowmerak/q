package agentskills

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const maximumSkillPathLinks = 255

func skillDirectoryEntries(path string) ([]os.DirEntry, error) {
	resolved, err := resolveSkillPath(path)
	if err != nil {
		return nil, err
	}
	return os.ReadDir(resolved)
}

// resolveSkillPath follows directory junctions as well as symbolic links.
// On Windows, Go reports junctions as ModeIrregular rather than ModeSymlink,
// so filepath.EvalSymlinks alone cannot resolve paths through them.
func resolveSkillPath(path string) (string, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	linkMode := os.ModeSymlink
	if runtime.GOOS == "windows" {
		linkMode |= os.ModeIrregular
	}
	seen := make(map[string]struct{})
	for links := 0; ; links++ {
		key := path
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if _, exists := seen[key]; exists {
			return "", fmt.Errorf("agent skills: link cycle resolving %q", path)
		}
		seen[key] = struct{}{}
		if links > maximumSkillPathLinks {
			return "", fmt.Errorf("agent skills: path exceeds %d links", maximumSkillPathLinks)
		}
		volume := filepath.VolumeName(path)
		separator := string(filepath.Separator)
		root := volume + separator
		parts := strings.Split(strings.TrimPrefix(path[len(volume):], separator), separator)
		current := root
		followed := false
		for index, part := range parts {
			switch part {
			case "", ".":
				continue
			case "..":
				current = filepath.Dir(current)
				continue
			}
			current = filepath.Join(current, part)
			info, err := os.Lstat(current)
			if err != nil {
				return "", err
			}
			if info.Mode()&linkMode == 0 {
				if index < len(parts)-1 && !info.IsDir() {
					return "", fmt.Errorf("agent skills: path component %q is not a directory", current)
				}
				continue
			}
			target, err := os.Readlink(current)
			if err != nil {
				return "", fmt.Errorf("agent skills: resolve link %q: %w", current, err)
			}
			if !filepath.IsAbs(target) {
				target = filepath.Dir(current) + separator + target
			}
			// Keep .. until preceding links have been followed. Cleaning a link
			// target here could select a different directory from the OS target.
			path = target
			if index < len(parts)-1 {
				path += separator + strings.Join(parts[index+1:], separator)
			}
			followed = true
			break
		}
		if !followed {
			return current, nil
		}
	}
}
