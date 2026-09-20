package providerhost

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/snowmerak/q/client"
)

const maxPendingUsageResponse = 4 << 20

// UsageTrackingHandler records provider-reported token usage from successful
// Gateway responses. Recording is best effort and never changes the response.
// Q usage metadata is consumed here and is not forwarded to provider handlers.
func UsageTrackingHandler(recorder client.UsageRecorder, next http.Handler) http.Handler {
	if next == nil {
		next = http.NotFoundHandler()
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		roleHeader := request.Header.Get(client.UsageRoleHeader)
		eventID := strings.TrimSpace(request.Header.Get(client.UsageEventIDHeader))
		request.Header.Del(client.UsageRoleHeader)
		request.Header.Del(client.UsageEventIDHeader)

		if recorder == nil || request.Method != http.MethodPost || !usageEndpoint(request.URL.Path) {
			next.ServeHTTP(writer, request)
			return
		}
		role := client.UsageRoleGateway
		if strings.TrimSpace(roleHeader) != "" {
			role = client.NormalizeUsageRole(roleHeader)
		}
		if !client.ValidUsageEventID(eventID) {
			eventID = client.NewUsageEventID()
		}
		observer := &usageResponseObserver{
			recorder: recorder,
			eventID:  eventID,
			role:     role,
			now:      time.Now,
		}
		next.ServeHTTP(&usageResponseWriter{ResponseWriter: writer, observer: observer}, request)
	})
}

func usageEndpoint(path string) bool {
	switch path {
	case "/v1/chat/completions", "/v1/responses", "/v1/embeddings":
		return true
	default:
		return false
	}
}

type usageResponseWriter struct {
	http.ResponseWriter
	observer *usageResponseObserver
	status   int
}

func (w *usageResponseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *usageResponseWriter) Write(value []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.status >= 200 && w.status < 300 {
		w.observer.observe(value, w.Header().Get("Content-Type"))
	}
	return w.ResponseWriter.Write(value)
}

func (w *usageResponseWriter) Flush() {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *usageResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

type usageResponseObserver struct {
	recorder  client.UsageRecorder
	eventID   string
	role      string
	model     string
	pending   []byte
	attempted bool
	now       func() time.Time
}

func (o *usageResponseObserver) observe(value []byte, contentType string) {
	if o == nil || o.attempted || len(value) == 0 {
		return
	}
	if strings.HasPrefix(strings.ToLower(contentType), "text/event-stream") {
		o.observeSSE(value)
		return
	}
	o.observeJSON(value)
}

func (o *usageResponseObserver) observeJSON(value []byte) {
	trimmed := bytes.TrimSpace(value)
	if json.Valid(trimmed) {
		o.consumeJSON(trimmed)
		return
	}
	if len(o.pending)+len(value) > maxPendingUsageResponse {
		o.pending = nil
		return
	}
	o.pending = append(o.pending, value...)
	trimmed = bytes.TrimSpace(o.pending)
	if json.Valid(trimmed) {
		o.consumeJSON(trimmed)
		o.pending = nil
	}
}

func (o *usageResponseObserver) observeSSE(value []byte) {
	// llm-provider writes chat JSON separately between the "data: " prefix and
	// line ending. Parsing that write directly avoids buffering a large chunk.
	if trimmed := bytes.TrimSpace(value); json.Valid(trimmed) {
		o.consumeJSON(trimmed)
		if o.attempted {
			return
		}
	}
	if len(value) > maxPendingUsageResponse {
		o.consumeSSEEvents(value)
		o.pending = nil
		return
	}
	if len(o.pending)+len(value) > maxPendingUsageResponse {
		o.pending = nil
		return
	}
	o.pending = append(o.pending, value...)
	o.pending = append([]byte(nil), o.consumeSSEEvents(o.pending)...)
}

func (o *usageResponseObserver) consumeSSEEvents(value []byte) []byte {
	for len(value) > 0 && !o.attempted {
		index, width := sseBoundary(value)
		if index < 0 {
			break
		}
		o.consumeSSEEvent(value[:index])
		value = value[index+width:]
	}
	return value
}

func sseBoundary(value []byte) (int, int) {
	lf := bytes.Index(value, []byte("\n\n"))
	crlf := bytes.Index(value, []byte("\r\n\r\n"))
	switch {
	case lf < 0:
		return crlf, 4
	case crlf < 0 || lf < crlf:
		return lf, 2
	default:
		return crlf, 4
	}
}

func (o *usageResponseObserver) consumeSSEEvent(event []byte) {
	var data []byte
	parts := 0
	for _, line := range bytes.Split(event, []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		part := bytes.TrimPrefix(line, []byte("data:"))
		part = bytes.TrimPrefix(part, []byte(" "))
		parts++
		if parts == 1 {
			data = part
			continue
		}
		if len(data)+len(part)+1 > maxPendingUsageResponse {
			return
		}
		if parts == 2 {
			data = append([]byte(nil), data...)
		}
		if len(data) > 0 {
			data = append(data, '\n')
		}
		data = append(data, part...)
	}
	if len(data) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
		return
	}
	o.consumeJSON(data)
}

type gatewayUsageEnvelope struct {
	Model    string            `json:"model"`
	Usage    *gatewayWireUsage `json:"usage"`
	Response *struct {
		Model string            `json:"model"`
		Usage *gatewayWireUsage `json:"usage"`
	} `json:"response"`
}

type gatewayWireUsage struct {
	PromptTokens     int                  `json:"prompt_tokens"`
	CompletionTokens int                  `json:"completion_tokens"`
	InputTokens      int                  `json:"input_tokens"`
	OutputTokens     int                  `json:"output_tokens"`
	TotalTokens      int                  `json:"total_tokens"`
	PromptDetails    *gatewayTokenDetails `json:"prompt_tokens_details"`
	InputDetails     *gatewayTokenDetails `json:"input_tokens_details"`
}

type gatewayTokenDetails struct {
	CachedTokens     int `json:"cached_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
}

func (o *usageResponseObserver) consumeJSON(value []byte) {
	if o == nil || o.attempted {
		return
	}
	var envelope gatewayUsageEnvelope
	if err := json.Unmarshal(value, &envelope); err != nil {
		return
	}
	model := strings.TrimSpace(envelope.Model)
	usage := envelope.Usage
	if envelope.Response != nil {
		if nestedModel := strings.TrimSpace(envelope.Response.Model); nestedModel != "" {
			model = nestedModel
		}
		if envelope.Response.Usage != nil {
			usage = envelope.Response.Usage
		}
	}
	if model != "" {
		o.model = model
	}
	if usage == nil {
		return
	}
	o.attempted = true
	prompt := max(usage.PromptTokens, usage.InputTokens)
	completion := max(usage.CompletionTokens, usage.OutputTokens)
	total := max(0, usage.TotalTokens)
	if prompt == 0 && total >= completion {
		prompt = total - completion
	}
	if completion == 0 && total >= prompt {
		completion = total - prompt
	}
	if total == 0 {
		total = prompt + completion
	}
	details := usage.PromptDetails
	if details == nil {
		details = usage.InputDetails
	}
	record := client.UsageRecord{
		EventID:          o.eventID,
		At:               o.now().UTC(),
		Model:            o.model,
		Role:             o.role,
		PromptTokens:     max(0, prompt),
		CompletionTokens: max(0, completion),
		TotalTokens:      max(0, total),
		CacheEstimated:   details == nil,
	}
	if details != nil {
		record.CachedTokens = max(0, details.CachedTokens)
		record.CacheWriteTokens = max(0, details.CacheWriteTokens)
	}
	_ = o.recorder.RecordUsage(record)
}

var _ http.Flusher = (*usageResponseWriter)(nil)
