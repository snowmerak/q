package studio

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/snowmerak/q/changes"
)

const (
	filePreviewBytes  = 256 << 10
	filePreviewLines  = 4000
	fileDirectoryPage = 500
)

type workspaceFileEntry struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Directory   bool   `json:"directory"`
	Symlink     bool   `json:"symlink"`
	Unavailable bool   `json:"unavailable"`
}

type workspaceFileListing struct {
	WorkspaceRoot string               `json:"workspace_root"`
	Path          string               `json:"path"`
	Entries       []workspaceFileEntry `json:"entries"`
	NextOffset    int                  `json:"next_offset"`
}

type workspaceFileContent struct {
	WorkspaceRoot string `json:"workspace_root"`
	Path          string `json:"path"`
	Content       string `json:"content"`
	Size          int64  `json:"size"`
	Binary        bool   `json:"binary"`
	Missing       bool   `json:"missing"`
	Truncated     bool   `json:"truncated"`
}

type workspaceFileChanges struct {
	WorkspaceRoot string         `json:"workspace_root"`
	Available     bool           `json:"available"`
	Reason        string         `json:"reason,omitempty"`
	Files         []changes.File `json:"files"`
}

type workspaceFileDiff struct {
	WorkspaceRoot string               `json:"workspace_root"`
	Path          string               `json:"path"`
	Available     bool                 `json:"available"`
	Reason        string               `json:"reason,omitempty"`
	Sections      []changes.Section    `json:"sections"`
	Comparison    string               `json:"comparison"`
	Content       workspaceFileContent `json:"content"`
	AllAdded      bool                 `json:"all_added"`
}

// Relative filesystem lookups go through os.Root, including symlink traversal.
// A selected workspace may be any directory, but its file paths cannot escape it.
func openFileWorkspace(request *http.Request) (*os.Root, string, string, error) {
	root, err := canonicalWorkspaceDirectory(request.URL.Query().Get("workspace_root"))
	if err != nil {
		return nil, "", "", err
	}
	name := request.URL.Query().Get("path")
	if name == "" {
		name = "."
	}
	name = filepath.Clean(filepath.FromSlash(name))
	if !filepath.IsLocal(name) {
		return nil, "", "", errors.New("path must be relative to the selected workspace")
	}
	for _, part := range strings.Split(filepath.ToSlash(name), "/") {
		if fileViewerMetadata(part) {
			return nil, "", "", errors.New("q and Git metadata are not shown in the file viewer")
		}
	}
	fs, err := os.OpenRoot(root)
	return fs, root, name, err
}

func fileViewerMetadata(name string) bool {
	if runtime.GOOS == "windows" {
		name = strings.ToLower(name)
	}
	return name == ".git" || name == ".q"
}

func serveWorkspaceFiles(writer http.ResponseWriter, request *http.Request) {
	fs, root, name, err := openFileWorkspace(request)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	defer func() { _ = fs.Close() }()
	offset := 0
	if value := request.URL.Query().Get("offset"); value != "" {
		offset, err = strconv.Atoi(value)
		if err != nil || offset < 0 || offset > 100000 {
			writeAPIError(writer, http.StatusBadRequest, errors.New("invalid directory offset"))
			return
		}
	}
	directory, err := fs.Open(name)
	if err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	defer func() { _ = directory.Close() }()
	for remaining := offset; remaining > 0; {
		entries, readErr := directory.ReadDir(min(remaining, fileDirectoryPage))
		remaining -= len(entries)
		if readErr != nil {
			break
		}
	}
	entries, err := directory.ReadDir(fileDirectoryPage + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	result := workspaceFileListing{WorkspaceRoot: root, Path: filepath.ToSlash(name), Entries: []workspaceFileEntry{}, NextOffset: -1}
	if len(entries) > fileDirectoryPage {
		entries = entries[:fileDirectoryPage]
		result.NextOffset = offset + fileDirectoryPage
	}
	for _, entry := range entries {
		if fileViewerMetadata(entry.Name()) {
			continue
		}
		path := filepath.Join(name, entry.Name())
		item := workspaceFileEntry{Name: entry.Name(), Path: filepath.ToSlash(path), Directory: entry.IsDir(), Symlink: entry.Type()&os.ModeSymlink != 0}
		if item.Symlink {
			info, statErr := fs.Stat(path)
			item.Unavailable = statErr != nil
			if statErr == nil {
				item.Directory = info.IsDir()
			}
		}
		result.Entries = append(result.Entries, item)
	}
	sort.Slice(result.Entries, func(i, j int) bool {
		if result.Entries[i].Directory != result.Entries[j].Directory {
			return result.Entries[i].Directory
		}
		return strings.ToLower(result.Entries[i].Name) < strings.ToLower(result.Entries[j].Name)
	})
	writeJSON(writer, http.StatusOK, result)
}

func serveWorkspaceFileContent(writer http.ResponseWriter, request *http.Request) {
	fs, root, name, err := openFileWorkspace(request)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	defer func() { _ = fs.Close() }()
	result, err := readWorkspaceFileContent(fs, root, name)
	if err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func readWorkspaceFileContent(fs *os.Root, root, name string) (workspaceFileContent, error) {
	result := workspaceFileContent{WorkspaceRoot: root, Path: filepath.ToSlash(name)}
	info, err := fs.Stat(name)
	if errors.Is(err, os.ErrNotExist) {
		result.Missing = true
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if !info.Mode().IsRegular() {
		return result, errors.New("only regular files can be previewed")
	}
	file, err := fs.Open(name)
	if err != nil {
		return result, err
	}
	defer func() { _ = file.Close() }()
	// Recheck the opened handle: the path may have changed after Stat.
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return result, errors.New("file changed during lookup; refresh the viewer")
	}
	result.Size = info.Size()
	body, err := io.ReadAll(io.LimitReader(file, filePreviewBytes+1))
	if err != nil {
		return result, err
	}
	result.Truncated = len(body) > filePreviewBytes
	if result.Truncated {
		body = body[:filePreviewBytes]
		// Remove only an incomplete final rune, not malformed bytes inside a file.
		for start := max(0, len(body)-utf8.UTFMax); start < len(body); start++ {
			if utf8.RuneStart(body[start]) && !utf8.FullRune(body[start:]) {
				body = body[:start]
				break
			}
		}
	}
	result.Binary = bytes.ContainsRune(body, 0) || !utf8.Valid(body)
	if !result.Binary {
		lines := 0
		for index, value := range body {
			if value == '\n' {
				lines++
				if lines == filePreviewLines && index+1 < len(body) {
					body = body[:index+1]
					result.Truncated = true
					break
				}
			}
		}
		result.Content = string(body)
	}
	return result, nil
}

func scopedFileChanges(ctx context.Context, root string) (changes.Snapshot, []changes.File, error) {
	snapshot, err := changes.List(ctx, root)
	if err != nil {
		return snapshot, nil, err
	}
	files := []changes.File{}
	for _, file := range snapshot.Files {
		name, err := filepath.Rel(root, filepath.Join(snapshot.Root, filepath.FromSlash(file.Path)))
		if err != nil || !filepath.IsLocal(name) {
			continue
		}
		file.Path = filepath.ToSlash(name)
		if file.OldPath != "" {
			old, err := filepath.Rel(root, filepath.Join(snapshot.Root, filepath.FromSlash(file.OldPath)))
			if err == nil && filepath.IsLocal(old) {
				file.OldPath = filepath.ToSlash(old)
			} else {
				file.OldPath = ""
			}
		}
		files = append(files, file)
	}
	return snapshot, files, nil
}

func serveWorkspaceFileChanges(writer http.ResponseWriter, request *http.Request) {
	fs, root, _, err := openFileWorkspace(request)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	defer func() { _ = fs.Close() }()
	ctx, cancel := context.WithTimeout(request.Context(), 15*time.Second)
	defer cancel()
	_, files, err := scopedFileChanges(ctx, root)
	result := workspaceFileChanges{WorkspaceRoot: root, Available: err == nil, Files: files}
	if err != nil {
		result.Reason = err.Error()
		result.Files = []changes.File{}
	}
	writeJSON(writer, http.StatusOK, result)
}

func serveWorkspaceFileDiff(writer http.ResponseWriter, request *http.Request) {
	fs, root, name, err := openFileWorkspace(request)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	defer func() { _ = fs.Close() }()
	ctx, cancel := context.WithTimeout(request.Context(), 15*time.Second)
	defer cancel()
	snapshot, _, err := scopedFileChanges(ctx, root)
	result := workspaceFileDiff{WorkspaceRoot: root, Path: filepath.ToSlash(name), Available: err == nil, Sections: []changes.Section{}}
	if err != nil {
		result.Reason = err.Error()
		writeJSON(writer, http.StatusOK, result)
		return
	}
	comparison := request.URL.Query().Get("comparison")
	if comparison == "" {
		comparison = "working"
	}
	if comparison != "working" && comparison != "staged" && comparison != "unstaged" {
		writeAPIError(writer, http.StatusBadRequest, errors.New("invalid file comparison"))
		return
	}
	result.Comparison = comparison
	relative, err := filepath.Rel(snapshot.Root, filepath.Join(root, name))
	if err != nil || !filepath.IsLocal(relative) {
		writeAPIError(writer, http.StatusBadRequest, errors.New("file is outside the repository"))
		return
	}
	selected := changes.File{Path: filepath.ToSlash(relative), Status: "  "}
	for _, original := range snapshot.Files {
		if original.Path == selected.Path {
			selected = original
			break
		}
	}
	if comparison == "staged" {
		source, err := changes.ReadIndex(ctx, snapshot.Root, selected)
		if err != nil {
			writeAPIError(writer, http.StatusUnprocessableEntity, err)
			return
		}
		result.Content = workspaceFileContent{WorkspaceRoot: root, Path: result.Path, Content: source.Content, Binary: source.Binary, Missing: source.Missing, Truncated: source.Truncated, Size: int64(len(source.Content))}
	} else {
		result.Content, err = readWorkspaceFileContent(fs, root, name)
		if err != nil {
			writeAPIError(writer, http.StatusUnprocessableEntity, err)
			return
		}
	}
	patch, allAdded, err := changes.ReadComparison(ctx, snapshot.Root, selected, comparison)
	if err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	result.AllAdded = allAdded
	if patch.Patch != "" {
		result.Sections = []changes.Section{patch}
	}
	writeJSON(writer, http.StatusOK, result)
}
