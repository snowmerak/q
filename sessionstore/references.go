package sessionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

var loomReferencePattern = regexp.MustCompile(`loom://[0-9a-fA-F]{32}`)

func ExtractLoomReferences(values ...string) []string {
	seen := make(map[string]struct{})
	for _, value := range values {
		for _, ref := range loomReferencePattern.FindAllString(value, -1) {
			seen[ref] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for ref := range seen {
		result = append(result, ref)
	}
	sort.Strings(result)
	return result
}

// LoomReferences returns every Loom reference retained by durable records.
// It scans explicit refs and legacy content/payload fields so records written
// before Loom refs were indexed remain valid GC roots.
func (s *Store) LoomReferences(ctx context.Context) ([]string, error) {
	if ctx == nil {
		return nil, errors.New("sessionstore: Loom reference context is nil")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.requireOpenLocked(); err != nil {
		return nil, err
	}
	return loomReferencesInDirectory(ctx, s.recordsDir, s.loadRecordFileLocked)
}

func LoomReferencesAt(ctx context.Context, root string) ([]string, error) {
	if ctx == nil {
		return nil, errors.New("sessionstore: Loom reference context is nil")
	}
	recordsDir := filepath.Join(root, ".q", "data", "records")
	return loomReferencesInDirectory(ctx, recordsDir, func(path string) (Record, error) {
		file, err := os.Open(path)
		if err != nil {
			return Record{}, err
		}
		defer file.Close()
		var record Record
		if err := json.NewDecoder(file).Decode(&record); err != nil {
			return Record{}, err
		}
		return record, nil
	})
}

func loomReferencesInDirectory(ctx context.Context, recordsDir string, load func(string) (Record, error)) ([]string, error) {
	entries, err := os.ReadDir(recordsDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("sessionstore: list records for Loom references: %w", err)
	}
	seen := make(map[string]struct{})
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		record, err := load(filepath.Join(recordsDir, entry.Name()))
		if err != nil {
			return nil, err
		}
		values := append([]string(nil), record.Refs...)
		values = append(values, record.Summary, record.Content, string(record.Payload))
		for _, ref := range ExtractLoomReferences(values...) {
			seen[ref] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for ref := range seen {
		result = append(result, ref)
	}
	sort.Strings(result)
	return result, nil
}
