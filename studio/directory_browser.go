package studio

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

type directoryEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type directoryListing struct {
	Home        string           `json:"home"`
	Current     string           `json:"current"`
	Parent      string           `json:"parent,omitempty"`
	Roots       []string         `json:"roots"`
	Directories []directoryEntry `json:"directories"`
	CanSelect   bool             `json:"can_select"`
}

func browseDirectories(value string) (directoryListing, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return directoryListing{}, fmt.Errorf("locate home directory: %w", err)
	}
	return listDirectories(value, home)
}

func listDirectories(value, home string) (directoryListing, error) {
	canonicalHome, err := resolveBrowsableDirectory(home)
	if err != nil {
		return directoryListing{}, fmt.Errorf("resolve home directory: %w", err)
	}
	if strings.TrimSpace(value) == "" {
		value = canonicalHome
	}
	current, err := resolveBrowsableDirectory(value)
	if err != nil {
		return directoryListing{}, err
	}
	entries, err := os.ReadDir(current)
	if err != nil {
		return directoryListing{}, fmt.Errorf("read directory: %w", err)
	}

	directories := make([]directoryEntry, 0, len(entries))
	for _, entry := range entries {
		entryPath := filepath.Join(current, entry.Name())
		isDirectory := entry.IsDir()
		if entry.Type()&os.ModeSymlink != 0 {
			if info, statErr := os.Stat(entryPath); statErr == nil {
				isDirectory = info.IsDir()
			}
		}
		if isDirectory {
			directories = append(directories, directoryEntry{Name: entry.Name(), Path: entryPath})
		}
	}
	sort.Slice(directories, func(left, right int) bool {
		return strings.ToLower(directories[left].Name) < strings.ToLower(directories[right].Name)
	})

	parent := filepath.Dir(current)
	if parent == current {
		parent = ""
	}
	homeInfo, _ := os.Stat(canonicalHome)
	currentInfo, _ := os.Stat(current)
	return directoryListing{
		Home:        canonicalHome,
		Current:     current,
		Parent:      parent,
		Roots:       directoryRoots(canonicalHome),
		Directories: directories,
		CanSelect:   homeInfo == nil || currentInfo == nil || !os.SameFile(homeInfo, currentInfo),
	}, nil
}

func resolveBrowsableDirectory(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("directory path is required")
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("resolve directory: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(filepath.Clean(absolute))
	if err != nil {
		return "", fmt.Errorf("resolve directory: %w", err)
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return "", errors.New("path must identify an accessible directory")
	}
	return canonical, nil
}

func directoryRoots(home string) []string {
	if runtime.GOOS != "windows" {
		return []string{string(filepath.Separator)}
	}

	roots := make([]string, 0, 4)
	for letter := 'A'; letter <= 'Z'; letter++ {
		root := string(letter) + `:\`
		if info, err := os.Stat(root); err == nil && info.IsDir() {
			roots = append(roots, root)
		}
	}
	volume := filepath.VolumeName(home)
	if len(roots) == 0 && volume != "" {
		roots = append(roots, volume+string(filepath.Separator))
	}
	return roots
}
