package providerhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
)

const (
	modelGroupPrefix               = "group/"
	defaultCircuitFailureThreshold = 3
	defaultCircuitOpenDuration     = 30 * time.Second
)

// ModelGroupHandler exposes q model groups through an existing Gateway HTTP
// handler. Group configuration is loaded per request so TUI saves take effect
// without restarting the Gateway process.
func ModelGroupHandler(inner http.Handler, store config.Store) http.Handler {
	if inner == nil {
		inner = http.NotFoundHandler()
	}
	return &modelGroupHandler{
		inner: inner, store: store,
		circuits: make(map[string]*gatewayModelCircuit), now: time.Now,
	}
}

type gatewayModelCircuit struct {
	failures  int
	openUntil time.Time
	halfOpen  bool
}

type modelGroupHandler struct {
	inner http.Handler
	store config.Store

	mu       sync.Mutex
	circuits map[string]*gatewayModelCircuit
	now      func() time.Time
}

func (h *modelGroupHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	groups, err := h.loadGroups()
	if err != nil {
		writeGroupError(writer, http.StatusInternalServerError, err)
		return
	}
	if len(groups) == 0 {
		h.inner.ServeHTTP(writer, request)
		return
	}
	switch {
	case request.Method == http.MethodGet && request.URL.Path == "/v1/models":
		h.serveModels(writer, request, groups)
	case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/v1/models/"):
		h.serveModel(writer, request, groups)
	case request.Method == http.MethodPost && request.URL.Path == "/v1/chat/completions":
		h.serveChat(writer, request, groups)
	default:
		h.inner.ServeHTTP(writer, request)
	}
}

func (h *modelGroupHandler) loadGroups() (map[string]config.ModelGroupConfig, error) {
	value, err := h.store.Load()
	if errors.Is(err, config.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return value.ModelGroups, nil
}

func (h *modelGroupHandler) serveModels(writer http.ResponseWriter, request *http.Request, groups map[string]config.ModelGroupConfig) {
	recorded := httptest.NewRecorder()
	h.inner.ServeHTTP(recorded, request)
	if recorded.Code != http.StatusOK {
		copyRecordedResponse(writer, recorded)
		return
	}
	var envelope struct {
		Object string         `json:"object"`
		Data   []client.Model `json:"data"`
	}
	if err := json.Unmarshal(recorded.Body.Bytes(), &envelope); err != nil {
		writeGroupError(writer, http.StatusBadGateway, fmt.Errorf("model groups: decode Gateway models: %w", err))
		return
	}
	envelope.Data = append(envelope.Data, syntheticGroupModels(groups, envelope.Data)...)
	sort.Slice(envelope.Data, func(i, j int) bool { return envelope.Data[i].ID < envelope.Data[j].ID })
	copyHeader(writer.Header(), recorded.Header())
	writer.Header().Del("Content-Length")
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(writer).Encode(envelope)
}

func (h *modelGroupHandler) serveModel(writer http.ResponseWriter, request *http.Request, groups map[string]config.ModelGroupConfig) {
	id := strings.TrimPrefix(request.URL.Path, "/v1/models/")
	if !strings.HasPrefix(id, modelGroupPrefix) {
		h.inner.ServeHTTP(writer, request)
		return
	}
	modelsRequest := request.Clone(request.Context())
	modelsRequest.URL.Path = "/v1/models"
	recorded := httptest.NewRecorder()
	h.serveModels(recorded, modelsRequest, groups)
	if recorded.Code != http.StatusOK {
		copyRecordedResponse(writer, recorded)
		return
	}
	var envelope struct {
		Data []client.Model `json:"data"`
	}
	if err := json.Unmarshal(recorded.Body.Bytes(), &envelope); err != nil {
		writeGroupError(writer, http.StatusBadGateway, err)
		return
	}
	for _, model := range envelope.Data {
		if model.ID == id {
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(model)
			return
		}
	}
	writeGroupError(writer, http.StatusNotFound, fmt.Errorf("model group %q was not found or has unavailable candidates", id))
}

func syntheticGroupModels(groups map[string]config.ModelGroupConfig, models []client.Model) []client.Model {
	byID := make(map[string]client.Model, len(models))
	for _, model := range models {
		byID[model.ID] = model
	}
	names := make([]string, 0, len(groups))
	for name := range groups {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]client.Model, 0, len(names))
	for _, name := range names {
		group := groups[name]
		members := make([]client.Model, 0, len(group.Candidates))
		available := true
		for _, candidate := range group.Candidates {
			model, found := byID[candidate.Model]
			if !found {
				available = false
				break
			}
			members = append(members, model)
		}
		if !available || len(members) == 0 {
			continue
		}
		result = append(result, client.Model{
			ID: modelGroupPrefix + name, Object: "model", OwnedBy: "q-model-group",
			ContextLength:   minimumModelLimit(members, func(model client.Model) int64 { return model.ContextLength }),
			MaxOutputTokens: minimumModelLimit(members, func(model client.Model) int64 { return model.MaxOutputTokens }),
		})
	}
	return result
}

func minimumModelLimit(models []client.Model, selectLimit func(client.Model) int64) int64 {
	var minimum int64
	for _, model := range models {
		value := selectLimit(model)
		if value <= 0 {
			return 0
		}
		if minimum == 0 || value < minimum {
			minimum = value
		}
	}
	return minimum
}

func (h *modelGroupHandler) serveChat(writer http.ResponseWriter, request *http.Request, groups map[string]config.ModelGroupConfig) {
	body, err := io.ReadAll(io.LimitReader(request.Body, 16<<20))
	if err != nil {
		writeGroupError(writer, http.StatusBadRequest, fmt.Errorf("model groups: read request: %w", err))
		return
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		writeGroupError(writer, http.StatusBadRequest, fmt.Errorf("model groups: decode request: %w", err))
		return
	}
	var externalModel string
	if err := json.Unmarshal(fields["model"], &externalModel); err != nil || externalModel == "" {
		h.serveInnerBody(writer, request, body)
		return
	}
	name, grouped := strings.CutPrefix(externalModel, modelGroupPrefix)
	group, found := groups[name]
	if !grouped || !found {
		h.serveInnerBody(writer, request, body)
		return
	}

	var last *httptest.ResponseRecorder
	for _, candidate := range group.Candidates {
		if !h.allow(candidate.Model) {
			continue
		}
		attemptBody, err := rewriteGroupRequest(fields, candidate)
		if err != nil {
			writeGroupError(writer, http.StatusInternalServerError, err)
			return
		}
		attemptContext := request.Context()
		cancel := func() {}
		if candidate.Timeout > 0 {
			attemptContext, cancel = context.WithTimeout(attemptContext, candidate.Timeout)
		}
		attempt := request.Clone(attemptContext)
		attempt.Body = io.NopCloser(bytes.NewReader(attemptBody))
		attempt.ContentLength = int64(len(attemptBody))
		candidateWriter := newGroupCandidateWriter(writer, externalModel)
		h.inner.ServeHTTP(candidateWriter, attempt)
		attemptErr := attemptContext.Err()
		cancel()
		if candidateWriter.committed {
			h.succeed(candidate.Model)
			candidateWriter.finish()
			return
		}
		recorded := candidateWriter.recorder()
		last = recorded
		if request.Context().Err() != nil {
			h.neutral(candidate.Model)
			copyRecordedResponse(writer, recorded)
			return
		}
		if errors.Is(attemptErr, context.DeadlineExceeded) || recorded.Code >= 500 {
			h.fail(candidate.Model)
			continue
		}
		h.succeed(candidate.Model)
		rewriteRecordedModel(recorded, externalModel)
		copyRecordedResponse(writer, recorded)
		return
	}
	if last != nil {
		copyRecordedResponse(writer, last)
		return
	}
	writeGroupError(writer, http.StatusServiceUnavailable, fmt.Errorf("model group %q has no candidate with an available circuit", name))
}

type groupCandidateWriter struct {
	target        http.ResponseWriter
	externalModel string
	header        http.Header
	status        int
	body          bytes.Buffer
	committed     bool
}

func newGroupCandidateWriter(target http.ResponseWriter, externalModel string) *groupCandidateWriter {
	return &groupCandidateWriter{target: target, externalModel: externalModel, header: make(http.Header), status: http.StatusOK}
}

func (w *groupCandidateWriter) Header() http.Header { return w.header }

func (w *groupCandidateWriter) WriteHeader(status int) {
	if w.committed || w.body.Len() > 0 {
		return
	}
	w.status = status
}

func (w *groupCandidateWriter) Write(value []byte) (int, error) {
	return w.body.Write(value)
}

func (w *groupCandidateWriter) Flush() {
	if w.status >= 500 {
		return
	}
	if !w.committed {
		copyHeader(w.target.Header(), w.header)
		w.target.Header().Del("Content-Length")
		w.target.WriteHeader(w.status)
		w.committed = true
	}
	w.flushBody()
	if flusher, ok := w.target.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *groupCandidateWriter) finish() {
	if !w.committed {
		return
	}
	w.flushBody()
}

func (w *groupCandidateWriter) flushBody() {
	if w.body.Len() == 0 {
		return
	}
	body := w.body.Bytes()
	if strings.HasPrefix(w.header.Get("Content-Type"), "text/event-stream") {
		body = rewriteSSEModels(body, w.externalModel)
	}
	_, _ = w.target.Write(body)
	w.body.Reset()
}

func (w *groupCandidateWriter) recorder() *httptest.ResponseRecorder {
	recorded := httptest.NewRecorder()
	copyHeader(recorded.Header(), w.header)
	recorded.Code = w.status
	recorded.Body.Write(w.body.Bytes())
	return recorded
}

func rewriteGroupRequest(fields map[string]json.RawMessage, candidate config.ModelCandidateConfig) ([]byte, error) {
	copyFields := make(map[string]json.RawMessage, len(fields)+1)
	for key, value := range fields {
		copyFields[key] = value
	}
	model, _ := json.Marshal(candidate.Model)
	copyFields["model"] = model
	if candidate.ReasoningEffort != "" {
		effort, _ := json.Marshal(candidate.ReasoningEffort)
		copyFields["reasoning_effort"] = effort
	}
	return json.Marshal(copyFields)
}

func (h *modelGroupHandler) serveInnerBody(writer http.ResponseWriter, request *http.Request, body []byte) {
	forward := request.Clone(request.Context())
	forward.Body = io.NopCloser(bytes.NewReader(body))
	forward.ContentLength = int64(len(body))
	h.inner.ServeHTTP(writer, forward)
}

func rewriteRecordedModel(recorded *httptest.ResponseRecorder, externalModel string) {
	contentType := recorded.Header().Get("Content-Type")
	if strings.HasPrefix(contentType, "text/event-stream") {
		recorded.Body = bytes.NewBuffer(rewriteSSEModels(recorded.Body.Bytes(), externalModel))
		recorded.Header().Del("Content-Length")
		return
	}
	var value map[string]any
	if json.Unmarshal(recorded.Body.Bytes(), &value) != nil {
		return
	}
	value["model"] = externalModel
	body, err := json.Marshal(value)
	if err == nil {
		recorded.Body = bytes.NewBuffer(append(body, '\n'))
		recorded.Header().Del("Content-Length")
	}
}

func rewriteSSEModels(body []byte, externalModel string) []byte {
	lines := bytes.Split(body, []byte("\n"))
	for index, line := range lines {
		if !bytes.HasPrefix(line, []byte("data: ")) || bytes.Equal(line, []byte("data: [DONE]")) {
			continue
		}
		var value map[string]any
		if json.Unmarshal(bytes.TrimPrefix(line, []byte("data: ")), &value) != nil {
			continue
		}
		value["model"] = externalModel
		encoded, err := json.Marshal(value)
		if err == nil {
			lines[index] = append([]byte("data: "), encoded...)
		}
	}
	return bytes.Join(lines, []byte("\n"))
}

func copyRecordedResponse(writer http.ResponseWriter, recorded *httptest.ResponseRecorder) {
	copyHeader(writer.Header(), recorded.Header())
	writer.WriteHeader(recorded.Code)
	_, _ = writer.Write(recorded.Body.Bytes())
}

func copyHeader(target, source http.Header) {
	for key, values := range source {
		target.Del(key)
		for _, value := range values {
			target.Add(key, value)
		}
	}
}

func writeGroupError(writer http.ResponseWriter, status int, err error) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]any{"error": map[string]any{
		"message": err.Error(), "type": "api_error", "code": strings.ToLower(strings.ReplaceAll(http.StatusText(status), " ", "_")),
	}})
}

func (h *modelGroupHandler) allow(model string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.circuits[model]
	if state == nil || state.openUntil.IsZero() {
		return true
	}
	if h.now().Before(state.openUntil) || state.halfOpen {
		return false
	}
	state.halfOpen = true
	return true
}

func (h *modelGroupHandler) succeed(model string) {
	h.mu.Lock()
	delete(h.circuits, model)
	h.mu.Unlock()
}

func (h *modelGroupHandler) neutral(model string) {
	h.mu.Lock()
	if state := h.circuits[model]; state != nil && state.halfOpen {
		delete(h.circuits, model)
	}
	h.mu.Unlock()
}

func (h *modelGroupHandler) fail(model string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.circuits[model]
	if state == nil {
		state = &gatewayModelCircuit{}
		h.circuits[model] = state
	}
	state.failures++
	if state.halfOpen || state.failures >= defaultCircuitFailureThreshold {
		state.failures = defaultCircuitFailureThreshold
		state.openUntil = h.now().Add(defaultCircuitOpenDuration)
		state.halfOpen = false
	}
}
