package lsp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

type Diagnostic struct {
	Range              Range           `json:"range"`
	Severity           int             `json:"severity,omitempty"`
	Code               any             `json:"code,omitempty"`
	Source             string          `json:"source,omitempty"`
	Message            string          `json:"message"`
	Tags               []int           `json:"tags,omitempty"`
	RelatedInformation json.RawMessage `json:"relatedInformation,omitempty"`
}

type FileRequest struct {
	Path     string `json:"path"`
	Language string `json:"language,omitempty"`
}

type PositionRequest struct {
	Path     string `json:"path"`
	Language string `json:"language,omitempty"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
}

type ReferencesRequest struct {
	Path               string `json:"path"`
	Language           string `json:"language,omitempty"`
	Line               int    `json:"line"`
	Column             int    `json:"column"`
	IncludeDeclaration bool   `json:"include_declaration,omitempty"`
}

type DiagnosticsRequest struct {
	Path             string `json:"path"`
	Language         string `json:"language,omitempty"`
	WaitMilliseconds int    `json:"wait_milliseconds,omitempty"`
}

type WorkspaceSymbolsRequest struct {
	Query    string `json:"query"`
	Path     string `json:"path,omitempty"`
	Language string `json:"language,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

type SourcePosition struct {
	Line   int `json:"line"`
	Column int `json:"column"`
}

type SourceRange struct {
	Start SourcePosition `json:"start"`
	End   SourcePosition `json:"end"`
}

type LocationResult struct {
	Path     string      `json:"path"`
	URI      DocumentURI `json:"uri"`
	Range    SourceRange `json:"range"`
	External bool        `json:"external,omitempty"`
}

type HoverResult struct {
	Contents any          `json:"contents"`
	Range    *SourceRange `json:"range,omitempty"`
}

type DiagnosticResult struct {
	Path         string      `json:"path"`
	URI          DocumentURI `json:"uri"`
	Range        SourceRange `json:"range"`
	Severity     int         `json:"severity,omitempty"`
	SeverityName string      `json:"severity_name,omitempty"`
	Code         any         `json:"code,omitempty"`
	Source       string      `json:"source,omitempty"`
	Message      string      `json:"message"`
	Tags         []int       `json:"tags,omitempty"`
}

type DiagnosticStatus string

const (
	DiagnosticStatusClean       DiagnosticStatus = "clean"
	DiagnosticStatusIssues      DiagnosticStatus = "issues"
	DiagnosticStatusUnavailable DiagnosticStatus = "unavailable"
)

type DiagnosticsResult struct {
	Path        string             `json:"path"`
	Status      DiagnosticStatus   `json:"status"`
	Published   bool               `json:"published"`
	Version     *int               `json:"version,omitempty"`
	Diagnostics []DiagnosticResult `json:"diagnostics"`
}

type SymbolResult struct {
	Name          string         `json:"name"`
	Kind          int            `json:"kind"`
	KindName      string         `json:"kind_name"`
	ContainerName string         `json:"container_name,omitempty"`
	Detail        string         `json:"detail,omitempty"`
	Location      LocationResult `json:"location"`
}

func (m *Manager) Hover(ctx context.Context, request PositionRequest) (*HoverResult, error) {
	session, _, document, _, position, err := m.preparePosition(ctx, request)
	if err != nil {
		return nil, err
	}
	var raw json.RawMessage
	if err := session.client.Request(ctx, "textDocument/hover", TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: document.URI}, Position: position,
	}, &raw); err != nil {
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var wire struct {
		Contents any    `json:"contents"`
		Range    *Range `json:"range,omitempty"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&wire); err != nil {
		return nil, fmt.Errorf("lsp: decode hover: %w", err)
	}
	result := &HoverResult{Contents: wire.Contents}
	if wire.Range != nil {
		converted := sourceRange(*wire.Range)
		result.Range = &converted
	}
	return result, nil
}

func (m *Manager) Definition(ctx context.Context, request PositionRequest) ([]LocationResult, error) {
	return m.locationRequest(ctx, "textDocument/definition", request)
}

func (m *Manager) References(ctx context.Context, request ReferencesRequest) ([]LocationResult, error) {
	positionRequest := PositionRequest{Path: request.Path, Language: request.Language, Line: request.Line, Column: request.Column}
	session, _, document, _, position, err := m.preparePosition(ctx, positionRequest)
	if err != nil {
		return nil, err
	}
	var raw json.RawMessage
	err = session.client.Request(ctx, "textDocument/references", map[string]any{
		"textDocument": TextDocumentIdentifier{URI: document.URI}, "position": position,
		"context": map[string]any{"includeDeclaration": request.IncludeDeclaration},
	}, &raw)
	if err != nil {
		return nil, err
	}
	return m.normalizeLocations(raw)
}

func (m *Manager) locationRequest(ctx context.Context, method string, request PositionRequest) ([]LocationResult, error) {
	session, _, document, _, position, err := m.preparePosition(ctx, request)
	if err != nil {
		return nil, err
	}
	var raw json.RawMessage
	if err := session.client.Request(ctx, method, TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: document.URI}, Position: position,
	}, &raw); err != nil {
		return nil, err
	}
	return m.normalizeLocations(raw)
}

func (m *Manager) DocumentSymbols(ctx context.Context, request FileRequest) ([]SymbolResult, error) {
	session, absPath, err := m.sessionForFile(ctx, request.Path, request.Language)
	if err != nil {
		return nil, err
	}
	document, _, _, err := session.syncDocument(ctx, absPath)
	if err != nil {
		return nil, err
	}
	var raw json.RawMessage
	if err := session.client.Request(ctx, "textDocument/documentSymbol", map[string]any{
		"textDocument": TextDocumentIdentifier{URI: document.URI},
	}, &raw); err != nil {
		return nil, err
	}
	return m.normalizeSymbols(raw, document.URI)
}

func (m *Manager) WorkspaceSymbols(ctx context.Context, request WorkspaceSymbolsRequest) ([]SymbolResult, error) {
	request.Query = strings.TrimSpace(request.Query)
	if request.Query == "" {
		return nil, errors.New("lsp: workspace symbol query is required")
	}
	limit := request.Limit
	if limit == 0 {
		limit = 100
	}
	if limit < 1 || limit > 500 {
		return nil, errors.New("lsp: workspace symbol limit must be between 1 and 500")
	}
	sessions, err := m.sessionsForWorkspace(ctx, request.Path, strings.ToLower(strings.TrimSpace(request.Language)))
	if err != nil {
		return nil, err
	}
	type outcome struct {
		symbols []SymbolResult
		err     error
	}
	outcomes := make(chan outcome, len(sessions))
	var wait sync.WaitGroup
	for _, session := range sessions {
		session := session
		wait.Add(1)
		go func() {
			defer wait.Done()
			var raw json.RawMessage
			err := session.client.Request(ctx, "workspace/symbol", map[string]any{"query": request.Query}, &raw)
			if err != nil {
				outcomes <- outcome{err: fmt.Errorf("%s/%s: %w", session.root.Path, session.root.Language, err)}
				return
			}
			symbols, err := m.normalizeSymbols(raw, "")
			outcomes <- outcome{symbols: symbols, err: err}
		}()
	}
	wait.Wait()
	close(outcomes)
	seen := make(map[string]struct{})
	var result []SymbolResult
	var resultErr error
	for outcome := range outcomes {
		if outcome.err != nil {
			resultErr = errors.Join(resultErr, outcome.err)
			continue
		}
		for _, symbol := range outcome.symbols {
			key := symbol.Name + "\x00" + symbol.Location.Path + "\x00" + fmt.Sprint(symbol.Location.Range.Start.Line, ":", symbol.Location.Range.Start.Column)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, symbol)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Name == result[j].Name {
			return result[i].Location.Path < result[j].Location.Path
		}
		return result[i].Name < result[j].Name
	})
	if len(result) > limit {
		result = result[:limit]
	}
	if len(result) == 0 && resultErr != nil {
		return nil, resultErr
	}
	return result, nil
}

func (m *Manager) Diagnostics(ctx context.Context, request DiagnosticsRequest) (DiagnosticsResult, error) {
	session, absPath, err := m.sessionForFile(ctx, request.Path, request.Language)
	if err != nil {
		return DiagnosticsResult{}, err
	}
	started := time.Now()
	document, _, changed, err := session.syncDocument(ctx, absPath)
	if err != nil {
		return DiagnosticsResult{}, err
	}
	waitDuration := time.Second
	if request.WaitMilliseconds != 0 {
		if request.WaitMilliseconds < 0 || request.WaitMilliseconds > 5000 {
			return DiagnosticsResult{}, errors.New("lsp: diagnostic wait must be between 0 and 5000 milliseconds")
		}
		waitDuration = time.Duration(request.WaitMilliseconds) * time.Millisecond
	}
	for {
		session.diagMu.Lock()
		snapshot, published := session.diagnostics[document.URI]
		changedChannel := session.diagChanged
		session.diagMu.Unlock()
		if published && (!changed || !snapshot.PublishedAt.Before(started)) {
			return m.diagnosticsResult(document.URI, snapshot, true), nil
		}
		if waitDuration <= 0 {
			return DiagnosticsResult{
				Path: m.location(document.URI, Range{}).Path, Status: DiagnosticStatusUnavailable,
				Published: published, Diagnostics: []DiagnosticResult{},
			}, nil
		}
		timer := time.NewTimer(waitDuration)
		select {
		case <-changedChannel:
			if !timer.Stop() {
				<-timer.C
			}
			continue
		case <-timer.C:
			if published {
				return m.diagnosticsResult(document.URI, snapshot, true), nil
			}
			return DiagnosticsResult{
				Path: m.location(document.URI, Range{}).Path, Status: DiagnosticStatusUnavailable,
				Published: false, Diagnostics: []DiagnosticResult{},
			}, nil
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return DiagnosticsResult{}, ctx.Err()
		}
	}
}

func (m *Manager) preparePosition(ctx context.Context, request PositionRequest) (*managedSession, string, openDocument, []byte, Position, error) {
	session, absPath, err := m.sessionForFile(ctx, request.Path, request.Language)
	if err != nil {
		return nil, "", openDocument{}, nil, Position{}, err
	}
	document, body, _, err := session.syncDocument(ctx, absPath)
	if err != nil {
		return nil, "", openDocument{}, nil, Position{}, err
	}
	position, err := lspPosition(body, request.Line, request.Column)
	return session, absPath, document, body, position, err
}

func (m *Manager) diagnosticsResult(uri DocumentURI, snapshot diagnosticSnapshot, published bool) DiagnosticsResult {
	status := DiagnosticStatusUnavailable
	if published {
		status = DiagnosticStatusClean
		if len(snapshot.Diagnostics) > 0 {
			status = DiagnosticStatusIssues
		}
	}
	result := DiagnosticsResult{
		Path: m.location(uri, Range{}).Path, Status: status, Published: published,
		Version: snapshot.Version, Diagnostics: make([]DiagnosticResult, 0, len(snapshot.Diagnostics)),
	}
	for _, diagnostic := range snapshot.Diagnostics {
		result.Diagnostics = append(result.Diagnostics, DiagnosticResult{
			Path: result.Path, URI: uri, Range: sourceRange(diagnostic.Range), Severity: diagnostic.Severity,
			SeverityName: diagnosticSeverityName(diagnostic.Severity), Code: diagnostic.Code, Source: diagnostic.Source,
			Message: diagnostic.Message, Tags: append([]int(nil), diagnostic.Tags...),
		})
	}
	return result
}

func (m *Manager) normalizeLocations(raw json.RawMessage) ([]LocationResult, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return []LocationResult{}, nil
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("lsp: decode locations: %w", err)
	}
	var maps []map[string]any
	var collect func(any)
	collect = func(current any) {
		switch typed := current.(type) {
		case []any:
			for _, item := range typed {
				collect(item)
			}
		case map[string]any:
			maps = append(maps, typed)
		}
	}
	collect(value)
	result := make([]LocationResult, 0, len(maps))
	for _, item := range maps {
		uriValue, rangeValue := item["uri"], item["range"]
		if uriValue == nil {
			uriValue, rangeValue = item["targetUri"], item["targetSelectionRange"]
			if rangeValue == nil {
				rangeValue = item["targetRange"]
			}
		}
		uri, ok := uriValue.(string)
		if !ok {
			continue
		}
		rangeBody, _ := json.Marshal(rangeValue)
		var wireRange Range
		if json.Unmarshal(rangeBody, &wireRange) != nil {
			continue
		}
		result = append(result, m.location(DocumentURI(uri), wireRange))
	}
	return result, nil
}

func (m *Manager) normalizeSymbols(raw json.RawMessage, defaultURI DocumentURI) ([]SymbolResult, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return []SymbolResult{}, nil
	}
	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("lsp: decode symbols: %w", err)
	}
	var result []SymbolResult
	var walk func([]map[string]any, string)
	walk = func(values []map[string]any, parent string) {
		for _, item := range values {
			name, _ := item["name"].(string)
			detail, _ := item["detail"].(string)
			container, _ := item["containerName"].(string)
			if container == "" {
				container = parent
			}
			kind := int(numberValue(item["kind"]))
			uri := defaultURI
			rangeValue := item["selectionRange"]
			if rangeValue == nil {
				rangeValue = item["range"]
			}
			if location, ok := item["location"].(map[string]any); ok {
				if value, ok := location["uri"].(string); ok {
					uri = DocumentURI(value)
				}
				rangeValue = location["range"]
			}
			rangeBody, _ := json.Marshal(rangeValue)
			var wireRange Range
			if uri != "" && json.Unmarshal(rangeBody, &wireRange) == nil {
				result = append(result, SymbolResult{Name: name, Kind: kind, KindName: symbolKindName(kind), ContainerName: container, Detail: detail, Location: m.location(uri, wireRange)})
			}
			if children, ok := item["children"].([]any); ok {
				childMaps := make([]map[string]any, 0, len(children))
				for _, child := range children {
					if mapped, ok := child.(map[string]any); ok {
						childMaps = append(childMaps, mapped)
					}
				}
				walk(childMaps, name)
			}
		}
	}
	walk(items, "")
	return result, nil
}

func (m *Manager) location(uri DocumentURI, wireRange Range) LocationResult {
	result := LocationResult{URI: uri, Range: sourceRange(wireRange)}
	path, err := pathFromURI(uri)
	if err != nil {
		result.Path, result.External = string(uri), true
		return result
	}
	if pathInside(m.root, path) {
		relative, _ := filepath.Rel(m.root, path)
		result.Path = filepath.ToSlash(relative)
	} else {
		result.Path, result.External = filepath.ToSlash(path), true
	}
	return result
}

func sourceRange(value Range) SourceRange {
	return SourceRange{Start: SourcePosition{Line: int(value.Start.Line) + 1, Column: int(value.Start.Character) + 1}, End: SourcePosition{Line: int(value.End.Line) + 1, Column: int(value.End.Character) + 1}}
}

func numberValue(value any) int64 {
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case json.Number:
		number, _ := typed.Int64()
		return number
	default:
		return 0
	}
}

func diagnosticSeverityName(value int) string {
	switch value {
	case 1:
		return "error"
	case 2:
		return "warning"
	case 3:
		return "information"
	case 4:
		return "hint"
	default:
		return ""
	}
}

func symbolKindName(value int) string {
	names := []string{"", "file", "module", "namespace", "package", "class", "method", "property", "field", "constructor", "enum", "interface", "function", "variable", "constant", "string", "number", "boolean", "array", "object", "key", "null", "enum_member", "struct", "event", "operator", "type_parameter"}
	if value >= 0 && value < len(names) {
		return names[value]
	}
	return ""
}
