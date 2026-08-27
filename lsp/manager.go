package lsp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	maximumDocumentBytes = 16 << 20
	shutdownTimeout      = 5 * time.Second
)

type Manager struct {
	ctx           context.Context
	cancel        context.CancelFunc
	root          string
	global        GlobalConfig
	workspace     WorkspaceConfig
	mu            sync.Mutex
	sessions      map[string]*sessionSlot
	failures      map[string]string
	closed        bool
	discoverRoots func(context.Context) ([]RootConfig, error)
	discovery     *discoveryAttempt
}

type sessionSlot struct {
	ready   chan struct{}
	session *managedSession
	err     error
}

type managedSession struct {
	root         RootConfig
	absRoot      string
	serverID     string
	server       ServerConfig
	client       *Client
	capabilities json.RawMessage
	docMu        sync.Mutex
	documents    map[string]openDocument
	diagMu       sync.Mutex
	diagnostics  map[DocumentURI]diagnosticSnapshot
	diagChanged  chan struct{}
	stderr       *synchronizedBuffer
}

type openDocument struct {
	URI      DocumentURI
	Language string
	Version  int
	Digest   [sha256.Size]byte
}

type diagnosticSnapshot struct {
	Version     *int
	Diagnostics []Diagnostic
	PublishedAt time.Time
}

type synchronizedBuffer struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (b *synchronizedBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.data.Len()+len(value) > 64<<10 {
		body := append([]byte(nil), b.data.Bytes()...)
		body = append(body, value...)
		if len(body) > 64<<10 {
			body = body[len(body)-(64<<10):]
		}
		b.data.Reset()
		_, _ = b.data.Write(body)
		return len(value), nil
	}
	return b.data.Write(value)
}

func (b *synchronizedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.String()
}

type SessionStatus struct {
	Root     string   `json:"root"`
	Language string   `json:"language"`
	Server   string   `json:"server,omitempty"`
	Command  string   `json:"command,omitempty"`
	Args     []string `json:"args,omitempty"`
	State    string   `json:"state"`
	Error    string   `json:"error,omitempty"`
	Source   string   `json:"source,omitempty"`
}

type StatusResult struct {
	Workspace string          `json:"workspace"`
	Sessions  []SessionStatus `json:"sessions"`
}

func NewManager(ctx context.Context, root string, global GlobalConfig, workspace WorkspaceConfig, options ...ManagerOption) (*Manager, error) {
	if ctx == nil {
		return nil, errors.New("lsp: manager context is nil")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("lsp: resolve workspace root: %w", err)
	}
	absRoot, err = filepath.EvalSymlinks(filepath.Clean(absRoot))
	if err != nil {
		return nil, fmt.Errorf("lsp: resolve workspace root symlinks: %w", err)
	}
	info, err := os.Stat(absRoot)
	if err != nil || !info.IsDir() {
		if err == nil {
			err = errors.New("not a directory")
		}
		return nil, fmt.Errorf("lsp: workspace root: %w", err)
	}
	global, err = global.Normalized()
	if err != nil {
		return nil, err
	}
	if err := global.Validate(); err != nil {
		return nil, err
	}
	if workspace.Version == 0 {
		workspace.Version = WorkspaceConfigVersion
	}
	workspace.Roots = append([]RootConfig(nil), workspace.Roots...)
	for index := range workspace.Roots {
		normalized, normalizeErr := NormalizeRoot(workspace.Roots[index])
		if normalizeErr != nil {
			return nil, fmt.Errorf("lsp: root %d: %w", index+1, normalizeErr)
		}
		workspace.Roots[index] = normalized
	}
	if err := workspace.Validate(global); err != nil {
		return nil, err
	}
	managerCtx, cancel := context.WithCancel(ctx)
	manager := &Manager{
		ctx: managerCtx, cancel: cancel, root: absRoot, global: global, workspace: workspace,
		sessions: make(map[string]*sessionSlot), failures: make(map[string]string),
	}
	for _, option := range options {
		option(manager)
	}
	return manager, nil
}

func (m *Manager) Status(_ context.Context) StatusResult {
	result := StatusResult{Workspace: m.root}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, root := range m.workspace.Roots {
		status := SessionStatus{Root: root.Path, Language: root.Language, Source: root.Source, State: "idle"}
		disabled := root.Disabled
		serverID, resolved := ResolveServer(m.global, root)
		if !resolved {
			if disabled {
				status.State = "disabled"
			} else {
				status.State = "unresolved"
				status.Error = "no enabled server is selected for the language"
			}
			result.Sessions = append(result.Sessions, status)
			continue
		}
		server := m.global.Servers[serverID]
		status.Server, status.Command, status.Args = serverID, server.Command, append([]string(nil), server.Args...)
		if disabled {
			status.State = "disabled"
			result.Sessions = append(result.Sessions, status)
			continue
		}
		key := sessionKey(root)
		if slot := m.sessions[key]; slot != nil {
			select {
			case <-slot.ready:
				if slot.err != nil {
					status.State, status.Error = "failed", slot.err.Error()
				} else {
					status.State = "running"
				}
			default:
				status.State = "starting"
			}
		} else if failure := m.failures[key]; failure != "" {
			status.State, status.Error = "failed", failure
		}
		result.Sessions = append(result.Sessions, status)
	}
	sort.Slice(result.Sessions, func(i, j int) bool {
		if result.Sessions[i].Root == result.Sessions[j].Root {
			return result.Sessions[i].Language < result.Sessions[j].Language
		}
		return result.Sessions[i].Root < result.Sessions[j].Root
	})
	return result
}

func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	slots := make([]*sessionSlot, 0, len(m.sessions))
	for _, slot := range m.sessions {
		slots = append(slots, slot)
	}
	m.mu.Unlock()
	var closeErr error
	for _, slot := range slots {
		<-slot.ready
		if slot.session == nil {
			continue
		}
		closeErr = errors.Join(closeErr, slot.session.close())
	}
	m.cancel()
	return closeErr
}

func (m *Manager) sessionForFile(ctx context.Context, path, language string) (*managedSession, string, error) {
	absPath, relative, err := m.resolveFile(path)
	if err != nil {
		return nil, "", err
	}
	root, err := m.route(relative, language)
	if errors.Is(err, errNoProjectRoot) || (err == nil && !m.hasServer(root)) {
		if discoveryErr := m.autoDiscover(ctx); discoveryErr != nil {
			return nil, "", errors.Join(err, discoveryErr)
		}
		root, err = m.route(relative, language)
	}
	if err != nil {
		return nil, "", err
	}
	session, err := m.getSession(ctx, root)
	return session, absPath, err
}

func (m *Manager) sessionsForWorkspace(ctx context.Context, path, language string) ([]*managedSession, error) {
	if strings.TrimSpace(path) != "" {
		session, _, err := m.sessionForFile(ctx, path, language)
		if err != nil {
			return nil, err
		}
		return []*managedSession{session}, nil
	}
	roots := m.enabledRoots(language)
	if len(roots) == 0 {
		if err := m.autoDiscover(ctx); err != nil {
			return nil, err
		}
		roots = m.enabledRoots(language)
	}
	if len(roots) == 0 {
		return nil, errors.New("lsp: no enabled, resolved project roots")
	}
	type outcome struct {
		session *managedSession
		err     error
	}
	outcomes := make(chan outcome, len(roots))
	var wait sync.WaitGroup
	for _, root := range roots {
		root := root
		wait.Add(1)
		go func() {
			defer wait.Done()
			session, err := m.getSession(ctx, root)
			outcomes <- outcome{session: session, err: err}
		}()
	}
	wait.Wait()
	close(outcomes)
	result := make([]*managedSession, 0, len(roots))
	var startErr error
	for outcome := range outcomes {
		if outcome.err != nil {
			startErr = errors.Join(startErr, outcome.err)
			continue
		}
		result = append(result, outcome.session)
	}
	if len(result) == 0 && startErr != nil {
		return nil, startErr
	}
	return result, nil
}

func (m *Manager) getSession(ctx context.Context, root RootConfig) (*managedSession, error) {
	key := sessionKey(root)
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrClosed
	}
	if existing := m.sessions[key]; existing != nil {
		m.mu.Unlock()
		select {
		case <-existing.ready:
			return existing.session, existing.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	slot := &sessionSlot{ready: make(chan struct{})}
	m.sessions[key] = slot
	delete(m.failures, key)
	m.mu.Unlock()

	session, err := m.startSession(ctx, root)
	m.mu.Lock()
	slot.session, slot.err = session, err
	if err != nil {
		m.failures[key] = err.Error()
		delete(m.sessions, key)
	}
	close(slot.ready)
	m.mu.Unlock()
	return session, err
}

func (m *Manager) startSession(ctx context.Context, root RootConfig) (*managedSession, error) {
	m.mu.Lock()
	serverID, ok := ResolveServer(m.global, root)
	server := m.global.Servers[serverID]
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("lsp: no server resolved for %s at %s", root.Language, root.Path)
	}
	absRoot := filepath.Join(m.root, filepath.FromSlash(root.Path))
	evaluated, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return nil, fmt.Errorf("lsp: resolve project root %s: %w", root.Path, err)
	}
	if !pathInside(m.root, evaluated) {
		return nil, fmt.Errorf("lsp: project root %s escapes workspace", root.Path)
	}
	info, err := os.Stat(evaluated)
	if err != nil || !info.IsDir() {
		if err == nil {
			err = errors.New("not a directory")
		}
		return nil, fmt.Errorf("lsp: project root %s: %w", root.Path, err)
	}
	s := &managedSession{
		root: root, absRoot: evaluated, serverID: serverID, server: server,
		documents: make(map[string]openDocument), diagnostics: make(map[DocumentURI]diagnosticSnapshot),
		diagChanged: make(chan struct{}), stderr: &synchronizedBuffer{},
	}
	handler := HandlerFuncs{RequestFunc: s.serverRequest, NotificationFunc: s.serverNotification}
	client, err := Start(m.ctx, Config{Command: server.Command, Args: server.Args, Directory: evaluated, Stderr: s.stderr, Handler: handler})
	if err != nil {
		return nil, fmt.Errorf("lsp: start %s: %w", serverID, err)
	}
	s.client = client
	rootURI, err := FileURI(evaluated)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	processID := os.Getpid()
	initializeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	result, err := client.Initialize(initializeCtx, InitializeParams{
		ProcessID: &processID, ClientInfo: &ClientInfo{Name: "q", Version: "0.1.0"}, RootURI: &rootURI,
		Capabilities: map[string]any{
			"general":   map[string]any{"positionEncodings": []string{"utf-16"}},
			"workspace": map[string]any{"configuration": true, "workspaceFolders": true, "symbol": map[string]any{}},
			"textDocument": map[string]any{
				"synchronization":    map[string]any{"dynamicRegistration": true, "didSave": true},
				"publishDiagnostics": map[string]any{"relatedInformation": true},
				"hover":              map[string]any{}, "definition": map[string]any{"linkSupport": true},
				"references": map[string]any{}, "documentSymbol": map[string]any{"hierarchicalDocumentSymbolSupport": true},
			},
		},
		WorkspaceFolders: []map[string]any{{"uri": rootURI, "name": filepath.Base(evaluated)}},
	})
	if err != nil {
		_ = client.Close()
		suffix := strings.TrimSpace(s.stderr.String())
		if suffix != "" {
			return nil, fmt.Errorf("lsp: initialize %s: %w: %s", serverID, err, suffix)
		}
		return nil, fmt.Errorf("lsp: initialize %s: %w", serverID, err)
	}
	s.capabilities = append(json.RawMessage(nil), result.Capabilities...)
	return s, nil
}

func (s *managedSession) serverRequest(_ context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case "workspace/applyEdit":
		return nil, &ResponseError{Code: InvalidRequestCode, Message: "q's LSP integration is read-only; workspace/applyEdit is disabled"}
	case "workspace/configuration":
		var request struct {
			Items []json.RawMessage `json:"items"`
		}
		if json.Unmarshal(params, &request) != nil {
			return []any{}, nil
		}
		return make([]any, len(request.Items)), nil
	case "workspace/workspaceFolders":
		uri, _ := FileURI(s.absRoot)
		return []map[string]any{{"uri": uri, "name": filepath.Base(s.absRoot)}}, nil
	case "client/registerCapability", "client/unregisterCapability", "window/workDoneProgress/create":
		return nil, nil
	default:
		return nil, &ResponseError{Code: MethodNotFoundCode, Message: "unsupported read-only client method: " + method}
	}
}

func (s *managedSession) serverNotification(_ context.Context, method string, params json.RawMessage) {
	if method != "textDocument/publishDiagnostics" {
		return
	}
	var publication struct {
		URI         DocumentURI  `json:"uri"`
		Version     *int         `json:"version,omitempty"`
		Diagnostics []Diagnostic `json:"diagnostics"`
	}
	if json.Unmarshal(params, &publication) != nil {
		return
	}
	s.diagMu.Lock()
	s.diagnostics[publication.URI] = diagnosticSnapshot{
		Version: publication.Version, Diagnostics: append([]Diagnostic(nil), publication.Diagnostics...), PublishedAt: time.Now(),
	}
	close(s.diagChanged)
	s.diagChanged = make(chan struct{})
	s.diagMu.Unlock()
}

func (s *managedSession) syncDocument(ctx context.Context, absPath string) (openDocument, []byte, bool, error) {
	info, err := os.Stat(absPath)
	if err != nil {
		return openDocument{}, nil, false, fmt.Errorf("lsp: inspect document: %w", err)
	}
	if !info.Mode().IsRegular() {
		return openDocument{}, nil, false, errors.New("lsp: document is not a regular file")
	}
	if info.Size() > maximumDocumentBytes {
		return openDocument{}, nil, false, fmt.Errorf("lsp: document exceeds %d bytes", maximumDocumentBytes)
	}
	body, err := os.ReadFile(absPath)
	if err != nil {
		return openDocument{}, nil, false, fmt.Errorf("lsp: read document: %w", err)
	}
	if !utf8.Valid(body) {
		return openDocument{}, nil, false, errors.New("lsp: document is not UTF-8")
	}
	uri, err := FileURI(absPath)
	if err != nil {
		return openDocument{}, nil, false, err
	}
	digest := sha256.Sum256(body)
	s.docMu.Lock()
	defer s.docMu.Unlock()
	document, exists := s.documents[absPath]
	changed := !exists || document.Digest != digest
	if !exists {
		document = openDocument{URI: uri, Language: s.root.Language, Version: 1, Digest: digest}
	} else if changed {
		document.Version++
		document.Digest = digest
	}
	if !exists {
		err = s.client.Notify(ctx, "textDocument/didOpen", map[string]any{"textDocument": map[string]any{
			"uri": uri, "languageId": s.root.Language, "version": document.Version, "text": string(body),
		}})
	} else if changed {
		err = s.client.Notify(ctx, "textDocument/didChange", map[string]any{
			"textDocument":   map[string]any{"uri": uri, "version": document.Version},
			"contentChanges": []map[string]any{{"text": string(body)}},
		})
	}
	if err != nil {
		return openDocument{}, nil, false, err
	}
	if changed {
		s.documents[absPath] = document
	}
	return document, body, changed, nil
}

func (s *managedSession) close() error {
	s.docMu.Lock()
	documents := make([]openDocument, 0, len(s.documents))
	for _, document := range s.documents {
		documents = append(documents, document)
	}
	s.docMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	for _, document := range documents {
		_ = s.client.Notify(ctx, "textDocument/didClose", map[string]any{"textDocument": map[string]any{"uri": document.URI}})
	}
	err := s.client.Shutdown(ctx)
	if errors.Is(err, ErrClosed) {
		return nil
	}
	return err
}

func (m *Manager) resolveFile(path string) (string, string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", "", errors.New("lsp: path is required")
	}
	absPath := path
	if !filepath.IsAbs(absPath) {
		absPath = filepath.Join(m.root, filepath.FromSlash(path))
	}
	absPath = filepath.Clean(absPath)
	if !pathInside(m.root, absPath) {
		return "", "", errors.New("lsp: path escapes workspace")
	}
	evaluated, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return "", "", fmt.Errorf("lsp: resolve path: %w", err)
	}
	if !pathInside(m.root, evaluated) {
		return "", "", errors.New("lsp: path escapes workspace through symlink")
	}
	relative, err := filepath.Rel(m.root, evaluated)
	if err != nil {
		return "", "", err
	}
	return evaluated, filepath.ToSlash(relative), nil
}

func (m *Manager) route(relative, language string) (RootConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	language = strings.ToLower(strings.TrimSpace(language))
	inferred := inferLanguage(relative)
	type candidate struct {
		root  RootConfig
		depth int
	}
	var candidates []candidate
	for _, root := range m.workspace.Roots {
		rootPath := filepath.Clean(filepath.FromSlash(root.Path))
		filePath := filepath.Clean(filepath.FromSlash(relative))
		rel, err := filepath.Rel(rootPath, filePath)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if language != "" && !strings.EqualFold(root.Language, language) {
			continue
		}
		depth := 0
		if rootPath != "." {
			depth = len(strings.Split(filepath.ToSlash(rootPath), "/"))
		}
		candidates = append(candidates, candidate{root, depth})
	}
	if len(candidates) == 0 {
		return RootConfig{}, fmt.Errorf("%w contains %s", errNoProjectRoot, relative)
	}
	maxDepth := 0
	for _, candidate := range candidates {
		if candidate.depth > maxDepth {
			maxDepth = candidate.depth
		}
	}
	var deepest []RootConfig
	for _, candidate := range candidates {
		if candidate.depth == maxDepth {
			deepest = append(deepest, candidate.root)
		}
	}
	if len(deepest) == 1 {
		return enabledRoot(deepest[0])
	}
	if inferred != "" {
		for _, root := range deepest {
			if root.Language == inferred {
				return enabledRoot(root)
			}
		}
	}
	return RootConfig{}, fmt.Errorf("lsp: %s matches multiple project languages; specify language", relative)
}

func sessionKey(root RootConfig) string {
	return strings.ToLower(filepath.ToSlash(root.Path)) + "\x00" + root.Language
}

func inferLanguage(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".rs":
		return "rust"
	case ".ts", ".tsx", ".mts", ".cts":
		return "typescript"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "javascript"
	case ".py", ".pyi":
		return "python"
	default:
		return ""
	}
}

func pathInside(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && !filepath.IsAbs(relative) && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func lspPosition(body []byte, line, column int) (Position, error) {
	if line < 1 || column < 1 {
		return Position{}, errors.New("lsp: line and column must be positive and 1-based")
	}
	lines := bytes.Split(body, []byte("\n"))
	if line > len(lines) {
		return Position{}, fmt.Errorf("lsp: line %d exceeds document line count %d", line, len(lines))
	}
	lineBody := bytes.TrimSuffix(lines[line-1], []byte("\r"))
	units := utf16.Encode([]rune(string(lineBody)))
	if column-1 > len(units) {
		return Position{}, fmt.Errorf("lsp: column %d exceeds line length %d", column, len(units)+1)
	}
	return Position{Line: uint32(line - 1), Character: uint32(column - 1)}, nil
}

func pathFromURI(uri DocumentURI) (string, error) {
	parsed, err := url.Parse(string(uri))
	if err != nil || parsed.Scheme != "file" {
		return "", fmt.Errorf("lsp: unsupported document URI %q", uri)
	}
	path := parsed.Path
	if parsed.Host != "" {
		path = "//" + parsed.Host + path
	}
	if len(path) >= 3 && path[0] == '/' && path[2] == ':' {
		path = path[1:]
	}
	return filepath.FromSlash(path), nil
}
