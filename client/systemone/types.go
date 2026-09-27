package systemone

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// Request evaluates all named questions against one shared state. State may be
// text, any JSON value, or nil for explicit JSON null. Use EvaluateRaw when
// exact JSON bytes or native extension fields must be preserved.
type Request struct {
	Model     string              `json:"model,omitempty"`
	State     any                 `json:"state"`
	Questions map[string]Question `json:"questions"`
}

type QuestionType string

const (
	QuestionChoice QuestionType = "choice"
	QuestionScore  QuestionType = "score"
	QuestionNoul   QuestionType = "noul"
)

// Question uses the native wire types. For Choice, Criteria is a map of named
// options; for Score, an ordered slice; for Noul, it may be omitted or contain
// true/false descriptions. The server validates question bounds and content.
type Question struct {
	Type         QuestionType `json:"type"`
	Instructions any          `json:"instructions,omitempty"`
	Criteria     any          `json:"criteria,omitempty"`
}

type CallOptions struct {
	// IdempotencyKey identifies one logical operation. Retain the key and exact
	// request to recover after a timeout; this client performs no retries.
	IdempotencyKey string
}

// Result contains native answers plus the exact response JSON and headers.
// Platform charge and request IDs are in Header, not provider body fields.
type Result struct {
	Model   string            `json:"model"`
	Answers map[string]Answer `json:"answers"`
	Usage   *Usage            `json:"usage,omitempty"`
	Raw     json.RawMessage   `json:"-"`
	Header  http.Header       `json:"-"`
}

type Answer struct {
	Type          QuestionType       `json:"type"`
	Choice        string             `json:"choice,omitempty"`
	Score         *float64           `json:"score,omitempty"`
	Noul          *float64           `json:"noul,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Legend        map[string]string  `json:"legend,omitempty"`
	Raw           json.RawMessage    `json:"-"`
}

func (answer *Answer) UnmarshalJSON(data []byte) error {
	type fields Answer
	var decoded fields
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*answer = Answer(decoded)
	answer.Raw = append(answer.Raw[:0], data...)
	return nil
}

// Usage token counts are pointers because the provider may omit or null them.
type Usage struct {
	InputTokens  *int64 `json:"input_tokens"`
	OutputTokens *int64 `json:"output_tokens"`
}

type Model struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	ReleaseDate string `json:"release_date"`
}

type ModelsResult struct {
	Models []Model         `json:"models"`
	Raw    json.RawMessage `json:"-"`
	Header http.Header     `json:"-"`
}

// APIError exposes stable classification headers while retaining the raw error
// body for callers that need provider details. Error() never prints that body.
type APIError struct {
	StatusCode int
	Code       string
	Source     string
	RequestID  string
	RetryAfter string
	Header     http.Header
	Body       []byte
}

func (err *APIError) Error() string {
	if err.Code != "" {
		return fmt.Sprintf("systemone: HTTP %d (%s)", err.StatusCode, err.Code)
	}
	return fmt.Sprintf("systemone: HTTP %d", err.StatusCode)
}
