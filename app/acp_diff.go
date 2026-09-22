package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/third_party/acp-go-sdk"
	qtools "github.com/snowmerak/q/tools"
)

// ACP diff content carries both complete file versions, so never send a
// truncated snapshot as though it were the whole file.
const maxACPFileDiffBytes = 256 << 10

type acpFileSnapshot struct {
	text   string
	exists bool
}

// acpFileDiffRuntime collects display-only file changes around the actual tool
// execution. The model still receives the original result and Loom receipt.
type acpFileDiffRuntime struct {
	base    agentToolRuntime
	root    string
	mu      sync.Mutex
	pending map[string][][]acp.ToolCallContent
}

func newACPFileDiffRuntime(base agentToolRuntime, root string) *acpFileDiffRuntime {
	return &acpFileDiffRuntime{base: base, root: root, pending: make(map[string][][]acp.ToolCallContent)}
}

func (r *acpFileDiffRuntime) Tools() []client.Tool { return r.base.Tools() }

func (r *acpFileDiffRuntime) Environment() qtools.HostEnvironment { return r.base.Environment() }

func (r *acpFileDiffRuntime) SearchSkillHints(ctx context.Context, query string, limit int) (qtools.SkillHintSearchResult, error) {
	searcher, ok := r.base.(skillHintSearcher)
	if !ok {
		return qtools.SkillHintSearchResult{}, errors.New("Agent Skills hint search is unavailable")
	}
	return searcher.SearchSkillHints(ctx, query, limit)
}

func (r *acpFileDiffRuntime) Call(ctx context.Context, call client.ToolCall) (client.ToolResult, error) {
	if call.Function.Name != "write_file" && call.Function.Name != "edit_file" {
		return r.base.Call(ctx, call)
	}
	path, relative, valid := acpFileDiffPath(r.root, call.Function.Arguments)
	var before acpFileSnapshot
	var rootHandle *os.Root
	if valid {
		rootHandle, _ = os.OpenRoot(r.root)
		if rootHandle != nil {
			before, valid = readACPFileSnapshot(rootHandle, relative)
		}
	}
	result, err := r.base.Call(ctx, call)
	var diffs []acp.ToolCallContent
	if rootHandle != nil {
		if valid {
			if after, ok := readACPFileSnapshot(rootHandle, relative); ok && after.exists &&
				(!before.exists || before.text != after.text) {
				if before.exists {
					diffs = append(diffs, acp.ToolDiffContent(path, after.text, before.text))
				} else {
					diffs = append(diffs, acp.ToolDiffContent(path, after.text))
				}
			}
		}
		_ = rootHandle.Close()
	}
	r.mu.Lock()
	r.pending[call.ID] = append(r.pending[call.ID], diffs)
	r.mu.Unlock()
	return result, err
}

func (r *acpFileDiffRuntime) take(callID string) []acp.ToolCallContent {
	r.mu.Lock()
	defer r.mu.Unlock()
	queue := r.pending[callID]
	if len(queue) == 0 {
		return nil
	}
	if len(queue) == 1 {
		delete(r.pending, callID)
	} else {
		r.pending[callID] = queue[1:]
	}
	return queue[0]
}

func acpFileDiffPath(root, arguments string) (path, relative string, ok bool) {
	var input struct {
		Path string `json:"path"`
	}
	if json.Unmarshal([]byte(arguments), &input) != nil || input.Path == "" {
		return "", "", false
	}
	if filepath.VolumeName(input.Path) != "" && !filepath.IsAbs(input.Path) {
		return "", "", false
	}
	path = input.Path
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	path = filepath.Clean(path)
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", "", false
	}
	return path, relative, true
}

func readACPFileSnapshot(root *os.Root, relative string) (acpFileSnapshot, bool) {
	file, err := root.Open(relative)
	if os.IsNotExist(err) {
		return acpFileSnapshot{}, true
	}
	if err != nil {
		return acpFileSnapshot{}, false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxACPFileDiffBytes {
		return acpFileSnapshot{}, false
	}
	data, err := io.ReadAll(io.LimitReader(file, maxACPFileDiffBytes+1))
	if err != nil || len(data) > maxACPFileDiffBytes || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return acpFileSnapshot{}, false
	}
	return acpFileSnapshot{text: string(data), exists: true}, true
}
