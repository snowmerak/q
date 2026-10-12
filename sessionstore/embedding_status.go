package sessionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/snowmerak/q/internal/fsopen"
)

// EmbeddingRecordState describes coverage using the owning domain's embedding
// rules. Current means the expected projections match the requested model.
type EmbeddingRecordState struct {
	Eligible bool
	Current  bool
}

type EmbeddingCounts struct {
	Records  int `json:"records"`
	Eligible int `json:"eligible"`
	Current  int `json:"current"`
	Missing  int `json:"missing"`
	Stale    int `json:"stale"`
	Excluded int `json:"excluded"`
}

type EmbeddingKindStatus struct {
	Kind string `json:"kind"`
	EmbeddingCounts
}

type EmbeddingModelStatus struct {
	Model      string `json:"model"`
	Dimensions int    `json:"dimensions"`
	Vectors    int    `json:"vectors"`
}

type EmbeddingIndexStatus struct {
	Model                string    `json:"model"`
	Dimensions           int       `json:"dimensions"`
	Vectors              int       `json:"vectors"`
	IndexedAt            time.Time `json:"indexed_at"`
	ConfigurationMatches bool      `json:"configuration_matches"`
}

// EmbeddingStatus is a read-only observation of source files and persisted
// index metadata, not a transactional snapshot or a probe of HNSW search.
type EmbeddingStatus struct {
	Exists bool `json:"exists"`
	EmbeddingCounts
	Vectors        int                    `json:"vectors"`
	CurrentVectors int                    `json:"current_vectors"`
	InvalidVectors int                    `json:"invalid_vectors"`
	Unreadable     int                    `json:"unreadable"`
	Complete       bool                   `json:"complete"`
	Kinds          []EmbeddingKindStatus  `json:"kinds"`
	Models         []EmbeddingModelStatus `json:"models"`
	Index          *EmbeddingIndexStatus  `json:"index,omitempty"`
	Issues         []string               `json:"issues"`
}

// InspectEmbeddings reads an archive directory without acquiring store ownership,
// opening an index, or creating files. The classifier shares the migration rules
// of its domain. Unreadable records leave a partial result, never a healthy zero.
func InspectEmbeddings(ctx context.Context, directory string, config VectorConfig, classify func(Record, VectorConfig) (EmbeddingRecordState, error)) (EmbeddingStatus, error) {
	result := EmbeddingStatus{Complete: true, Kinds: []EmbeddingKindStatus{}, Models: []EmbeddingModelStatus{}, Issues: []string{}}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	entries, err := os.ReadDir(filepath.Join(directory, "data", "records"))
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	result.Exists = true
	counts := make(map[string]*EmbeddingKindStatus)
	models := make(map[EmbeddingModelStatus]int)
	issue := func(err error) {
		result.Complete = false
		if len(result.Issues) < 10 {
			result.Issues = append(result.Issues, err.Error())
		}
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		record, err := readRecordFile(filepath.Join(directory, "data", "records", entry.Name()))
		if err != nil {
			result.Unreadable++
			issue(err)
			continue
		}
		kind := counts[record.Kind]
		if kind == nil {
			kind = &EmbeddingKindStatus{Kind: record.Kind}
			counts[record.Kind] = kind
		}
		result.Records++
		kind.Records++
		state, err := classify(record, config)
		if err != nil {
			issue(fmt.Errorf("record %s: %w", record.ID, err))
		} else {
			addEmbeddingCounts(&result.EmbeddingCounts, record, state)
			addEmbeddingCounts(&kind.EmbeddingCounts, record, state)
		}
		for _, projection := range recordEmbeddings(record) {
			value := projection.Embedding
			result.Vectors++
			models[EmbeddingModelStatus{Model: value.Model, Dimensions: value.Dimensions}]++
			valid := value.Dimensions > 0 && len(value.Vector) == value.Dimensions
			for _, number := range value.Vector {
				if math.IsNaN(float64(number)) || math.IsInf(float64(number), 0) {
					valid = false
				}
			}
			if !valid {
				result.InvalidVectors++
			} else if config.Enabled() && value.Model == config.Model && value.Dimensions == config.Dimensions {
				result.CurrentVectors++
			}
		}
	}
	for _, kind := range counts {
		result.Kinds = append(result.Kinds, *kind)
	}
	sort.Slice(result.Kinds, func(i, j int) bool { return result.Kinds[i].Kind < result.Kinds[j].Kind })
	for model, count := range models {
		model.Vectors = count
		result.Models = append(result.Models, model)
	}
	sort.Slice(result.Models, func(i, j int) bool {
		if result.Models[i].Model == result.Models[j].Model {
			return result.Models[i].Dimensions < result.Models[j].Dimensions
		}
		return result.Models[i].Model < result.Models[j].Model
	})
	body, err := fsopen.ReadFile(filepath.Join(directory, "index", "state.json"))
	if err == nil {
		var state indexState
		if err := json.Unmarshal(body, &state); err != nil {
			issue(fmt.Errorf("read index metadata: %w", err))
		} else {
			result.Index = &EmbeddingIndexStatus{IndexedAt: state.IndexedAt, ConfigurationMatches: !config.Enabled() && state.Vector == nil}
			if state.Vector != nil {
				result.Index.Model, result.Index.Dimensions, result.Index.Vectors = state.Vector.Model, state.Vector.Dimensions, state.Vector.RecordCount
				result.Index.ConfigurationMatches = config.Enabled() && state.Vector.Model == config.Model && state.Vector.Dimensions == config.Dimensions
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		issue(fmt.Errorf("read index metadata: %w", err))
	}
	return result, ctx.Err()
}

func addEmbeddingCounts(counts *EmbeddingCounts, record Record, state EmbeddingRecordState) {
	if !state.Eligible {
		counts.Excluded++
		return
	}
	counts.Eligible++
	if state.Current {
		counts.Current++
	} else if record.Embedding == nil && len(record.VectorProjections) == 0 {
		counts.Missing++
	} else {
		counts.Stale++
	}
}
