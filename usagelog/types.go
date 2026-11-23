package usagelog

import "time"

const (
	ServiceName     = "q-usage"
	ProtocolVersion = 1
	Implementation  = "0.2.0"
	HotRetention    = 90 * 24 * time.Hour
)

type Health struct {
	Service         string `json:"service"`
	ProtocolVersion int    `json:"protocol_version"`
	Implementation  string `json:"implementation"`
	Generation      string `json:"generation"`
	Ready           bool   `json:"ready"`
}

func (h Health) Compatible() bool {
	return h.Service == ServiceName && h.ProtocolVersion == ProtocolVersion && h.Ready
}

type Filter struct {
	From    time.Time
	To      time.Time
	Model   string
	Role    string
	Details bool
}

type Totals struct {
	Calls               int64 `json:"calls"`
	PromptTokens        int64 `json:"prompt_tokens"`
	CompletionTokens    int64 `json:"completion_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
	CachedTokens        int64 `json:"cached_tokens"`
	CacheWriteTokens    int64 `json:"cache_write_tokens"`
	EstimatedCalls      int64 `json:"estimated_calls"`
	CacheEstimatedCalls int64 `json:"cache_estimated_calls"`
	CacheHitCalls       int64 `json:"cache_hit_calls,omitempty"`
	CacheHitSamples     int64 `json:"cache_hit_samples,omitempty"`
}

type SeriesPoint struct {
	Bucket string `json:"bucket"`
	Totals
}

type DimensionTotal struct {
	Name string `json:"name"`
	ID   string `json:"id,omitempty"`
	Totals
}

type ModelPerformance struct {
	Name          string   `json:"name"`
	Completed     int64    `json:"completed"`
	Failed        int64    `json:"failed"`
	Stopped       int64    `json:"stopped"`
	Retried       int64    `json:"retried"`
	DurationP50MS *float64 `json:"duration_p50_ms,omitempty"`
	DurationP95MS *float64 `json:"duration_p95_ms,omitempty"`
	TTFTP50MS     *float64 `json:"ttft_p50_ms,omitempty"`
	TTFTP95MS     *float64 `json:"ttft_p95_ms,omitempty"`
	OutputRateP50 *float64 `json:"output_rate_p50,omitempty"`
}

type UsageView struct {
	From        time.Time          `json:"from"`
	To          time.Time          `json:"to"`
	Resolution  string             `json:"resolution"`
	Totals      Totals             `json:"totals"`
	Series      []SeriesPoint      `json:"series"`
	Models      []DimensionTotal   `json:"models"`
	Roles       []DimensionTotal   `json:"roles"`
	Projects    []DimensionTotal   `json:"projects,omitempty"`
	Subagents   []DimensionTotal   `json:"subagents,omitempty"`
	Performance []ModelPerformance `json:"performance,omitempty"`
}

type archiveManifest struct {
	Day         string
	Path        string
	SHA256      string
	Rows        int64
	TotalTokens int64
}
