package sessionstore

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	goformersearch "github.com/MichaelAyles/goformersearch"
	"github.com/snowmerak/q/worklock"
)

const (
	VectorIndexVersion    = 1
	defaultHNSWM          = 16
	defaultEfConstruction = 200
	defaultEfSearch       = 64
	maximumHNSWM          = 128
	maximumEfConstruction = 10000
	maximumEfSearch       = 10000
)

// OpenOptions controls optional derived indexes. A zero value opens the text
// store without a vector index.
type OpenOptions struct {
	Vector        VectorConfig
	WorkspaceLock *worklock.Lock
	// Directory selects the store directory below root. The zero value uses
	// ".q" for workspace compatibility. It must be a relative child path.
	Directory string
}

// VectorConfig identifies the embeddings included in one HNSW graph. Model
// and Dimensions must either both be set or both be empty. HNSW parameters use
// conservative defaults when omitted.
type VectorConfig struct {
	Model          string
	Dimensions     int
	M              int
	EfConstruction int
	EfSearch       int
}

func (c VectorConfig) Enabled() bool {
	return c.Model != "" && c.Dimensions > 0
}

func normalizeVectorConfig(c VectorConfig) (VectorConfig, error) {
	if c.Model != strings.TrimSpace(c.Model) {
		return VectorConfig{}, errors.New("sessionstore: vector model must not have surrounding whitespace")
	}
	if c.Model == "" && c.Dimensions == 0 {
		if c.M != 0 || c.EfConstruction != 0 || c.EfSearch != 0 {
			return VectorConfig{}, errors.New("sessionstore: vector model and dimensions are required when HNSW parameters are configured")
		}
		return VectorConfig{}, nil
	}
	if c.Model == "" || c.Dimensions < 1 {
		return VectorConfig{}, errors.New("sessionstore: vector model and positive dimensions are required together")
	}
	if c.M == 0 {
		c.M = defaultHNSWM
	}
	if c.M < 2 || c.M > maximumHNSWM {
		return VectorConfig{}, fmt.Errorf("sessionstore: HNSW M must be between 2 and %d", maximumHNSWM)
	}
	if c.EfConstruction == 0 {
		c.EfConstruction = defaultEfConstruction
	}
	if c.EfConstruction < 1 || c.EfConstruction > maximumEfConstruction {
		return VectorConfig{}, fmt.Errorf("sessionstore: HNSW ef_construction must be between 1 and %d", maximumEfConstruction)
	}
	if c.EfSearch == 0 {
		c.EfSearch = defaultEfSearch
	}
	if c.EfSearch < 1 || c.EfSearch > maximumEfSearch {
		return VectorConfig{}, fmt.Errorf("sessionstore: HNSW ef_search must be between 1 and %d", maximumEfSearch)
	}
	return c, nil
}

type vectorIDState struct {
	Version  int               `json:"version"`
	NextID   uint64            `json:"next_id"`
	RecordID map[string]string `json:"record_ids"`
}

type vectorIndex struct {
	config     VectorConfig
	graph      *goformersearch.HNSWIndex
	idToRecord map[uint64]string
	recordToID map[string]uint64
	nextID     uint64
	dirty      bool
}

type vectorResult struct {
	RecordID   string
	Similarity float64
}

func newVectorIndex(config VectorConfig) *vectorIndex {
	graph := goformersearch.NewHNSWIndex(
		config.Dimensions,
		goformersearch.WithM(config.M),
		goformersearch.WithEfConstruction(config.EfConstruction),
		goformersearch.WithEfSearch(config.EfSearch),
	)
	return &vectorIndex{
		config: config, graph: graph, idToRecord: make(map[uint64]string),
		recordToID: make(map[string]uint64), nextID: 1,
	}
}

func loadVectorIndex(graphPath, idsPath string, config VectorConfig) (*vectorIndex, error) {
	file, err := os.Open(graphPath)
	if err != nil {
		return nil, err
	}
	graph, err := goformersearch.LoadHNSW(bufio.NewReader(file))
	closeErr := file.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if graph.Dims() != config.Dimensions {
		return nil, fmt.Errorf("HNSW dimensions %d do not match configured dimensions %d", graph.Dims(), config.Dimensions)
	}
	graph.SetEfSearch(config.EfSearch)
	body, err := os.ReadFile(idsPath)
	if err != nil {
		return nil, err
	}
	var state vectorIDState
	if err := json.Unmarshal(body, &state); err != nil {
		return nil, fmt.Errorf("decode HNSW ID map: %w", err)
	}
	if state.Version != VectorIndexVersion || len(state.RecordID) != graph.Len() {
		return nil, errors.New("HNSW ID map does not match graph")
	}
	index := &vectorIndex{
		config: config, graph: graph, idToRecord: make(map[uint64]string, len(state.RecordID)),
		recordToID: make(map[string]uint64, len(state.RecordID)), nextID: state.NextID,
	}
	for encodedID, recordID := range state.RecordID {
		var id uint64
		if _, err := fmt.Sscan(encodedID, &id); err != nil || id == 0 || recordID == "" {
			return nil, errors.New("HNSW ID map contains an invalid entry")
		}
		if _, duplicate := index.recordToID[recordID]; duplicate {
			return nil, errors.New("HNSW ID map contains a duplicate record")
		}
		index.idToRecord[id] = recordID
		index.recordToID[recordID] = id
	}
	if index.nextID == 0 {
		return nil, errors.New("HNSW ID map has an invalid next ID")
	}
	return index, nil
}

func (v *vectorIndex) matches(embedding *Embedding) bool {
	return embedding != nil && embedding.Model == v.config.Model &&
		embedding.Dimensions == v.config.Dimensions && len(embedding.Vector) == v.config.Dimensions
}

// addRecord only accepts a record not already present. Updates and removals
// rebuild the derived graph because goformersearch intentionally has no delete
// operation.
func (v *vectorIndex) addRecord(record Record) (bool, error) {
	if !v.matches(record.Embedding) {
		return false, nil
	}
	if _, exists := v.recordToID[record.ID]; exists {
		return false, fmt.Errorf("sessionstore: HNSW record %q already exists", record.ID)
	}
	if v.nextID == 0 {
		return false, errors.New("sessionstore: HNSW numeric ID space exhausted")
	}
	id := v.nextID
	v.nextID++
	vector := normalizedVector(record.Embedding.Vector)
	v.graph.Add(id, vector)
	v.idToRecord[id] = record.ID
	v.recordToID[record.ID] = id
	v.dirty = true
	return true, nil
}

func (v *vectorIndex) search(vector []float32, limit int) ([]vectorResult, error) {
	if len(vector) != v.config.Dimensions {
		return nil, fmt.Errorf("sessionstore: query vector has %d dimensions; want %d", len(vector), v.config.Dimensions)
	}
	if limit < 1 || v.graph.Len() == 0 {
		return nil, nil
	}
	if err := validateFiniteNonzeroVector(vector); err != nil {
		return nil, fmt.Errorf("sessionstore: query vector: %w", err)
	}
	results := v.graph.Search(normalizedVector(vector), min(limit, v.graph.Len()))
	out := make([]vectorResult, 0, len(results))
	for _, result := range results {
		recordID, ok := v.idToRecord[result.ID]
		if !ok {
			return nil, fmt.Errorf("sessionstore: HNSW result ID %d is not mapped", result.ID)
		}
		out = append(out, vectorResult{RecordID: recordID, Similarity: float64(result.Similarity)})
	}
	return out, nil
}

func normalizedVector(vector []float32) []float32 {
	result := cloneFloats(vector)
	var squared float64
	for _, value := range result {
		squared += float64(value) * float64(value)
	}
	norm := math.Sqrt(squared)
	for index := range result {
		result[index] = float32(float64(result[index]) / norm)
	}
	return result
}

func validateFiniteNonzeroVector(vector []float32) error {
	var magnitude float64
	for _, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return errors.New("contains a non-finite value")
		}
		magnitude += float64(value) * float64(value)
	}
	if magnitude == 0 {
		return errors.New("must not be zero")
	}
	return nil
}

func (v *vectorIndex) save(graphPath, idsPath string) error {
	if err := writeIndexAtomic(graphPath, func(writer *bufio.Writer) error {
		return goformersearch.Save(writer, v.graph)
	}); err != nil {
		return err
	}
	state := vectorIDState{Version: VectorIndexVersion, NextID: v.nextID, RecordID: make(map[string]string, len(v.idToRecord))}
	for id, recordID := range v.idToRecord {
		state.RecordID[fmt.Sprint(id)] = recordID
	}
	body, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := writeAtomic(idsPath, append(body, '\n'), 0o600); err != nil {
		return err
	}
	v.dirty = false
	return nil
}

func writeIndexAtomic(path string, encode func(*bufio.Writer) error) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, ".vectors-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(temporary)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	writer := bufio.NewWriter(file)
	if err := encode(writer); err != nil {
		_ = file.Close()
		return err
	}
	if err := writer.Flush(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := replaceFile(temporary, path); err != nil {
		return err
	}
	keep = true
	return nil
}
