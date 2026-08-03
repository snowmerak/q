package sessionstore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/blevesearch/bleve/v2"
)

const maximumRecordSize = 64 << 20

type indexState struct {
	MappingVersion    int          `json:"mapping_version"`
	RecordVersion     int          `json:"record_version"`
	RecordCount       uint64       `json:"record_count"`
	SourceFingerprint string       `json:"source_fingerprint"`
	Vector            *vectorState `json:"vector,omitempty"`
	IndexedAt         time.Time    `json:"indexed_at"`
}

type vectorState struct {
	IndexVersion   int    `json:"index_version"`
	Model          string `json:"model"`
	Dimensions     int    `json:"dimensions"`
	M              int    `json:"m"`
	EfConstruction int    `json:"ef_construction"`
	EfSearch       int    `json:"ef_search"`
	RecordCount    int    `json:"record_count"`
}

// IndexingError means the source record was persisted but the derived index
// could not be updated. Reopening or rebuilding the store retries indexing.
type IndexingError struct {
	RecordID string
	Err      error
}

func (e *IndexingError) Error() string {
	return fmt.Sprintf("sessionstore: record %q persisted but indexing failed: %v", e.RecordID, e.Err)
}

func (e *IndexingError) Unwrap() error { return e.Err }

// Store owns the durable record archive and its derived Bleve index.
type Store struct {
	root          string
	recordsDir    string
	indexRoot     string
	indexPath     string
	vectorPath    string
	vectorIDsPath string
	statePath     string
	vectorCfg     VectorConfig

	mu      sync.RWMutex
	index   bleve.Index
	vectors *vectorIndex
	closed  bool
}

// Open opens the workspace-local store rooted at root. Missing, stale, or
// unreadable derived indexes are rebuilt from source records automatically.
func Open(root string) (*Store, error) {
	return OpenWithOptions(root, OpenOptions{})
}

// OpenWithOptions opens the workspace-local store and enables HNSW when the
// supplied vector configuration identifies an embedding model and dimension.
func OpenWithOptions(root string, options OpenOptions) (*Store, error) {
	vectorConfig, err := normalizeVectorConfig(options.Vector)
	if err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("sessionstore: resolve root: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return nil, fmt.Errorf("sessionstore: inspect root: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("sessionstore: root is not a directory")
	}
	workspaceDir := filepath.Join(absolute, ".q")
	store := &Store{
		root:          absolute,
		recordsDir:    filepath.Join(workspaceDir, "data", "records"),
		indexRoot:     filepath.Join(workspaceDir, "index"),
		indexPath:     filepath.Join(workspaceDir, "index", "bleve"),
		vectorPath:    filepath.Join(workspaceDir, "index", "vectors.hnsw"),
		vectorIDsPath: filepath.Join(workspaceDir, "index", "vectors.ids.json"),
		statePath:     filepath.Join(workspaceDir, "index", "state.json"),
		vectorCfg:     vectorConfig,
	}
	if err := os.MkdirAll(store.recordsDir, 0o700); err != nil {
		return nil, fmt.Errorf("sessionstore: create records directory: %w", err)
	}
	if err := os.MkdirAll(store.indexRoot, 0o700); err != nil {
		return nil, fmt.Errorf("sessionstore: create index directory: %w", err)
	}
	_ = os.Chmod(filepath.Join(workspaceDir, "data"), 0o700)
	_ = os.Chmod(store.recordsDir, 0o700)
	_ = os.Chmod(store.indexRoot, 0o700)

	if store.indexStateIsCurrent() {
		opened, openErr := bleve.Open(store.indexPath)
		if openErr == nil {
			store.index = opened
			if vectorConfig.Enabled() {
				store.vectors, openErr = loadVectorIndex(store.vectorPath, store.vectorIDsPath, vectorConfig)
				if openErr != nil {
					_ = store.index.Close()
					store.index = nil
				}
			}
		}
	}
	if store.index == nil {
		if rebuildErr := store.rebuildLocked(); rebuildErr != nil {
			return nil, rebuildErr
		}
		return store, nil
	}
	return store, nil
}

func (s *Store) Root() string { return s.root }

func (s *Store) RecordsDir() string { return s.recordsDir }

func (s *Store) IndexPath() string { return s.indexPath }

func (s *Store) VectorIndexPath() string { return s.vectorPath }

func (s *Store) VectorConfig() VectorConfig { return s.vectorCfg }

// Save atomically persists record before updating the Bleve index. A blank ID,
// version, or timestamp is populated by Save and returned to the caller.
func (s *Store) Save(record Record) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireOpenLocked(); err != nil {
		return Record{}, err
	}

	var previous *Record
	if strings.TrimSpace(record.ID) != "" {
		loaded, err := s.loadRecordLocked(strings.TrimSpace(record.ID))
		if err == nil {
			previous = &loaded
		} else if !errors.Is(err, ErrNotFound) {
			return Record{}, err
		}
	}
	prepared, err := prepareRecord(record, previous, time.Now().UTC())
	if err != nil {
		return Record{}, err
	}
	body, err := json.MarshalIndent(prepared, "", "  ")
	if err != nil {
		return Record{}, fmt.Errorf("sessionstore: encode record: %w", err)
	}
	if len(body) > maximumRecordSize {
		return Record{}, fmt.Errorf("sessionstore: record exceeds %d bytes", maximumRecordSize)
	}
	body = append(body, '\n')
	if err := writeAtomic(s.recordPath(prepared.ID), body, 0o600); err != nil {
		return Record{}, fmt.Errorf("sessionstore: persist record %q: %w", prepared.ID, err)
	}
	if err := s.index.Index(prepared.ID, prepared); err != nil {
		return cloneRecord(prepared), &IndexingError{RecordID: prepared.ID, Err: err}
	}
	if s.vectors != nil {
		if previous == nil {
			if _, err := s.vectors.addRecord(prepared); err != nil {
				return cloneRecord(prepared), &IndexingError{RecordID: prepared.ID, Err: err}
			}
		} else if !embeddingsEqual(previous.Embedding, prepared.Embedding) {
			if err := s.rebuildVectorsLocked(); err != nil {
				s.vectors = nil
				return cloneRecord(prepared), &IndexingError{RecordID: prepared.ID, Err: err}
			}
		}
		if s.vectors.dirty {
			if err := s.vectors.save(s.vectorPath, s.vectorIDsPath); err != nil {
				return cloneRecord(prepared), &IndexingError{RecordID: prepared.ID, Err: fmt.Errorf("persist HNSW index: %w", err)}
			}
		}
	}
	if err := s.writeStateLocked(); err != nil {
		return cloneRecord(prepared), &IndexingError{RecordID: prepared.ID, Err: err}
	}
	return cloneRecord(prepared), nil
}

func (s *Store) Get(id string) (Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.requireOpenLocked(); err != nil {
		return Record{}, err
	}
	return s.loadRecordLocked(id)
}

func (s *Store) loadRecordLocked(id string) (Record, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Record{}, errors.New("sessionstore: record ID is required")
	}
	file, err := os.Open(s.recordPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, fmt.Errorf("sessionstore: open record %q: %w", id, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Record{}, fmt.Errorf("sessionstore: inspect record %q: %w", id, err)
	}
	if info.Size() > maximumRecordSize {
		return Record{}, fmt.Errorf("sessionstore: record %q exceeds %d bytes", id, maximumRecordSize)
	}
	decoder := json.NewDecoder(io.LimitReader(file, maximumRecordSize))
	decoder.DisallowUnknownFields()
	var record Record
	if err := decoder.Decode(&record); err != nil {
		return Record{}, fmt.Errorf("sessionstore: decode record %q: %w", id, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Record{}, fmt.Errorf("sessionstore: decode record %q: multiple JSON values", id)
		}
		return Record{}, fmt.Errorf("sessionstore: decode record %q: %w", id, err)
	}
	if record.ID != id {
		return Record{}, fmt.Errorf("sessionstore: record ID mismatch: requested %q, found %q", id, record.ID)
	}
	if record.Version != RecordVersion {
		return Record{}, fmt.Errorf("sessionstore: unsupported record version %d", record.Version)
	}
	return cloneRecord(record), nil
}

// Rebuild discards the derived Bleve and HNSW indexes and recreates them from
// source records. Source records are never removed.
func (s *Store) Rebuild() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("sessionstore: store is closed")
	}
	return s.rebuildLocked()
}

func (s *Store) rebuildLocked() error {
	if s.index != nil {
		if err := s.index.Close(); err != nil {
			return fmt.Errorf("sessionstore: close index for rebuild: %w", err)
		}
		s.index = nil
	}
	if err := os.RemoveAll(s.indexPath); err != nil {
		return fmt.Errorf("sessionstore: remove derived index: %w", err)
	}
	if err := os.Remove(s.vectorPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("sessionstore: remove derived vector index: %w", err)
	}
	if err := os.Remove(s.vectorIDsPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("sessionstore: remove derived vector ID map: %w", err)
	}
	index, err := bleve.New(s.indexPath, indexMapping())
	if err != nil {
		return fmt.Errorf("sessionstore: create Bleve index: %w", err)
	}
	s.index = index
	if s.vectorCfg.Enabled() {
		s.vectors = newVectorIndex(s.vectorCfg)
	} else {
		s.vectors = nil
	}
	if err := s.syncAllLocked(); err != nil {
		_ = s.index.Close()
		s.index = nil
		s.vectors = nil
		return err
	}
	return nil
}

func (s *Store) syncAllLocked() error {
	entries, err := os.ReadDir(s.recordsDir)
	if err != nil {
		return fmt.Errorf("sessionstore: list source records: %w", err)
	}
	batch := s.index.NewBatch()
	var count uint64
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		record, err := s.loadRecordFileLocked(filepath.Join(s.recordsDir, entry.Name()))
		if err != nil {
			return err
		}
		if entry.Name() != recordFileName(record.ID) {
			return fmt.Errorf("sessionstore: source filename does not match record %q", record.ID)
		}
		if err := batch.Index(record.ID, record); err != nil {
			return fmt.Errorf("sessionstore: prepare record %q for indexing: %w", record.ID, err)
		}
		if s.vectors != nil {
			if _, err := s.vectors.addRecord(record); err != nil {
				return fmt.Errorf("sessionstore: prepare record %q for vector indexing: %w", record.ID, err)
			}
		}
		count++
	}
	if err := s.index.Batch(batch); err != nil {
		return fmt.Errorf("sessionstore: index source records: %w", err)
	}
	if s.vectors != nil {
		if err := s.vectors.save(s.vectorPath, s.vectorIDsPath); err != nil {
			return fmt.Errorf("sessionstore: persist HNSW index: %w", err)
		}
	}
	return s.writeStateWithCountLocked(count)
}

func (s *Store) rebuildVectorsLocked() error {
	if !s.vectorCfg.Enabled() {
		s.vectors = nil
		return nil
	}
	index := newVectorIndex(s.vectorCfg)
	entries, err := os.ReadDir(s.recordsDir)
	if err != nil {
		return fmt.Errorf("sessionstore: list records for HNSW rebuild: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		record, err := s.loadRecordFileLocked(filepath.Join(s.recordsDir, entry.Name()))
		if err != nil {
			return err
		}
		if _, err := index.addRecord(record); err != nil {
			return fmt.Errorf("sessionstore: rebuild HNSW record %q: %w", record.ID, err)
		}
	}
	if err := index.save(s.vectorPath, s.vectorIDsPath); err != nil {
		return fmt.Errorf("sessionstore: persist rebuilt HNSW index: %w", err)
	}
	s.vectors = index
	return nil
}

func (s *Store) loadRecordFileLocked(path string) (Record, error) {
	file, err := os.Open(path)
	if err != nil {
		return Record{}, fmt.Errorf("sessionstore: open source record %s: %w", path, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Record{}, fmt.Errorf("sessionstore: inspect source record %s: %w", path, err)
	}
	if info.Size() > maximumRecordSize {
		return Record{}, fmt.Errorf("sessionstore: source record %s exceeds %d bytes", path, maximumRecordSize)
	}
	decoder := json.NewDecoder(io.LimitReader(file, maximumRecordSize))
	decoder.DisallowUnknownFields()
	var record Record
	if err := decoder.Decode(&record); err != nil {
		return Record{}, fmt.Errorf("sessionstore: decode source record %s: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Record{}, fmt.Errorf("sessionstore: decode source record %s: multiple JSON values", path)
		}
		return Record{}, fmt.Errorf("sessionstore: decode source record %s: %w", path, err)
	}
	if record.ID == "" || record.Kind == "" || record.Version != RecordVersion || record.CreatedAt.IsZero() || record.UpdatedAt.IsZero() {
		return Record{}, fmt.Errorf("sessionstore: invalid source record %s", path)
	}
	return record, nil
}

func (s *Store) indexStateIsCurrent() bool {
	body, err := os.ReadFile(s.statePath)
	if err != nil {
		return false
	}
	var state indexState
	if json.Unmarshal(body, &state) != nil {
		return false
	}
	if state.MappingVersion != MappingVersion || state.RecordVersion != RecordVersion {
		return false
	}
	if s.vectorCfg.Enabled() {
		if state.Vector == nil || state.Vector.IndexVersion != VectorIndexVersion ||
			state.Vector.Model != s.vectorCfg.Model || state.Vector.Dimensions != s.vectorCfg.Dimensions ||
			state.Vector.M != s.vectorCfg.M || state.Vector.EfConstruction != s.vectorCfg.EfConstruction ||
			state.Vector.EfSearch != s.vectorCfg.EfSearch {
			return false
		}
		if _, err := os.Stat(s.vectorPath); err != nil {
			return false
		}
		if _, err := os.Stat(s.vectorIDsPath); err != nil {
			return false
		}
	} else if state.Vector != nil {
		return false
	}
	fingerprint, sourceCount, err := s.sourceFingerprint()
	if err != nil {
		return false
	}
	return state.RecordCount == sourceCount && state.SourceFingerprint == fingerprint
}

func (s *Store) writeStateLocked() error {
	count, err := s.index.DocCount()
	if err != nil {
		return fmt.Errorf("sessionstore: count indexed records: %w", err)
	}
	return s.writeStateWithCountLocked(count)
}

func (s *Store) writeStateWithCountLocked(count uint64) error {
	fingerprint, sourceCount, err := s.sourceFingerprint()
	if err != nil {
		return fmt.Errorf("sessionstore: fingerprint source records: %w", err)
	}
	if sourceCount != count {
		return fmt.Errorf("sessionstore: source record count %d does not match indexed count %d", sourceCount, count)
	}
	state := indexState{
		MappingVersion:    MappingVersion,
		RecordVersion:     RecordVersion,
		RecordCount:       count,
		SourceFingerprint: fingerprint,
		IndexedAt:         time.Now().UTC(),
	}
	if s.vectors != nil {
		state.Vector = &vectorState{
			IndexVersion:   VectorIndexVersion,
			Model:          s.vectorCfg.Model,
			Dimensions:     s.vectorCfg.Dimensions,
			M:              s.vectorCfg.M,
			EfConstruction: s.vectorCfg.EfConstruction,
			EfSearch:       s.vectorCfg.EfSearch,
			RecordCount:    s.vectors.graph.Len(),
		}
	}
	body, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("sessionstore: encode index state: %w", err)
	}
	if err := writeAtomic(s.statePath, append(body, '\n'), 0o600); err != nil {
		return fmt.Errorf("sessionstore: persist index state: %w", err)
	}
	return nil
}

func (s *Store) sourceFingerprint() (string, uint64, error) {
	entries, err := os.ReadDir(s.recordsDir)
	if err != nil {
		return "", 0, err
	}
	hash := sha256.New()
	var count uint64
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return "", 0, err
		}
		_, _ = fmt.Fprintf(hash, "%s\x00%d\x00%d\x00", entry.Name(), info.Size(), info.ModTime().UnixNano())
		count++
	}
	return hex.EncodeToString(hash.Sum(nil)), count, nil
}

func embeddingsEqual(left, right *Embedding) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	if left.Model != right.Model || left.Dimensions != right.Dimensions || len(left.Vector) != len(right.Vector) {
		return false
	}
	for index := range left.Vector {
		if left.Vector[index] != right.Vector[index] {
			return false
		}
	}
	return true
}

func (s *Store) recordPath(id string) string {
	return filepath.Join(s.recordsDir, recordFileName(id))
}

func recordFileName(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:]) + ".json"
}

func writeAtomic(path string, body []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, ".record-*")
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
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(body); err != nil {
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

func (s *Store) requireOpenLocked() error {
	if s.closed {
		return errors.New("sessionstore: store is closed")
	}
	if s.index == nil {
		return errors.New("sessionstore: text index is unavailable; rebuild required")
	}
	if s.vectorCfg.Enabled() && s.vectors == nil {
		return errors.New("sessionstore: vector index is unavailable; rebuild required")
	}
	return nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.index == nil {
		return nil
	}
	err := s.index.Close()
	s.index = nil
	s.vectors = nil
	if err != nil {
		return fmt.Errorf("sessionstore: close Bleve index: %w", err)
	}
	return nil
}
