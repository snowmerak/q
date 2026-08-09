package loom

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Stats struct {
	Artifacts int   `json:"artifacts"`
	Blobs     int   `json:"blobs"`
	Bytes     int64 `json:"bytes"`
}

type GCOptions struct {
	Roots            []Ref
	ProtectNewerThan time.Time
	TargetBytes      int64
	DryRun           bool
}

type GCResult struct {
	Before           Stats `json:"before"`
	After            Stats `json:"after"`
	ArtifactsRemoved int   `json:"artifacts_removed"`
	BlobsRemoved     int   `json:"blobs_removed"`
	BytesReclaimed   int64 `json:"bytes_reclaimed"`
	DryRun           bool  `json:"dry_run"`
}

func (s *Store) Stats(ctx context.Context) (Stats, error) {
	if ctx == nil {
		return Stats{}, errors.New("loom: stats context is nil")
	}
	if err := ctx.Err(); err != nil {
		return Stats{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statsLocked()
}

func (s *Store) Collect(ctx context.Context, options GCOptions) (GCResult, error) {
	if ctx == nil {
		return GCResult{}, errors.New("loom: GC context is nil")
	}
	if err := ctx.Err(); err != nil {
		return GCResult{}, err
	}
	if options.TargetBytes < 0 {
		return GCResult{}, errors.New("loom: GC target bytes must not be negative")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.collectLocked(ctx, options)
}

func (s *Store) maybeAutoCollect(ctx context.Context, digest string, incoming int64) error {
	s.mu.Lock()
	options := s.options
	_, blobErr := os.Lstat(filepath.Join(s.blobs, digest))
	current, sizeErr := s.storeSizeLocked()
	s.mu.Unlock()
	if blobErr == nil || !errors.Is(blobErr, os.ErrNotExist) {
		return nil
	}
	if sizeErr != nil {
		return sizeErr
	}
	if !options.AutoGC || options.Roots == nil || current+incoming <= int64(float64(options.MaximumStoreBytes)*options.GCTriggerRatio) {
		return nil
	}
	roots, err := options.Roots(ctx)
	if err != nil {
		return fmt.Errorf("loom: collect GC roots: %w", err)
	}
	_, err = s.Collect(ctx, GCOptions{
		Roots: roots, ProtectNewerThan: time.Now().UTC().Add(-options.GCGracePeriod),
		TargetBytes: int64(float64(options.MaximumStoreBytes) * options.GCTargetRatio),
	})
	return err
}

func (s *Store) collectLocked(ctx context.Context, options GCOptions) (GCResult, error) {
	artifacts, err := s.loadArtifactsLocked(ctx)
	if err != nil {
		return GCResult{}, err
	}
	blobSizes, err := s.blobSizesLocked()
	if err != nil {
		return GCResult{}, err
	}
	before := Stats{Artifacts: len(artifacts), Blobs: len(blobSizes)}
	for _, size := range blobSizes {
		before.Bytes += size
	}

	marked := make(map[Ref]struct{}, len(options.Roots))
	var mark func(Ref)
	mark = func(ref Ref) {
		if _, exists := marked[ref]; exists {
			return
		}
		artifact, exists := artifacts[ref]
		if !exists {
			return
		}
		marked[ref] = struct{}{}
		for _, parent := range artifact.Parents {
			mark(parent)
		}
	}
	for _, root := range options.Roots {
		mark(root)
	}
	if !options.ProtectNewerThan.IsZero() {
		for ref, artifact := range artifacts {
			if !artifact.CreatedAt.Before(options.ProtectNewerThan) {
				mark(ref)
			}
		}
	}

	digestRefs := make(map[string]int, len(blobSizes))
	for _, artifact := range artifacts {
		digestRefs[artifact.Digest]++
	}
	result := GCResult{Before: before, After: before, DryRun: options.DryRun}
	for digest, size := range blobSizes {
		if digestRefs[digest] != 0 {
			continue
		}
		if !options.DryRun {
			if err := os.Remove(filepath.Join(s.blobs, digest)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return GCResult{}, fmt.Errorf("loom: remove orphan blob %q: %w", digest, err)
			}
		}
		result.BlobsRemoved++
		result.BytesReclaimed += size
		result.After.Blobs--
		result.After.Bytes -= size
		delete(blobSizes, digest)
	}

	candidates := make([]Artifact, 0, len(artifacts))
	for ref, artifact := range artifacts {
		if _, keep := marked[ref]; !keep {
			candidates = append(candidates, artifact)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].CreatedAt.Equal(candidates[j].CreatedAt) {
			return candidates[i].Ref < candidates[j].Ref
		}
		return candidates[i].CreatedAt.Before(candidates[j].CreatedAt)
	})
	for _, artifact := range candidates {
		if err := ctx.Err(); err != nil {
			return GCResult{}, err
		}
		if options.TargetBytes > 0 && result.After.Bytes <= options.TargetBytes {
			break
		}
		if !options.DryRun {
			if err := os.Remove(filepath.Join(s.artifacts, artifact.Ref.ID()+".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
				return GCResult{}, fmt.Errorf("loom: remove artifact %s: %w", artifact.Ref, err)
			}
		}
		result.ArtifactsRemoved++
		result.After.Artifacts--
		digestRefs[artifact.Digest]--
		if digestRefs[artifact.Digest] == 0 {
			size, exists := blobSizes[artifact.Digest]
			if exists {
				if !options.DryRun {
					if err := os.Remove(filepath.Join(s.blobs, artifact.Digest)); err != nil && !errors.Is(err, os.ErrNotExist) {
						return GCResult{}, fmt.Errorf("loom: remove blob %q: %w", artifact.Digest, err)
					}
				}
				result.BlobsRemoved++
				result.BytesReclaimed += size
				result.After.Blobs--
				result.After.Bytes -= size
				delete(blobSizes, artifact.Digest)
			}
		}
	}
	return result, nil
}

func (s *Store) loadArtifactsLocked(ctx context.Context) (map[Ref]Artifact, error) {
	entries, err := os.ReadDir(s.artifacts)
	if err != nil {
		return nil, fmt.Errorf("loom: list artifacts: %w", err)
	}
	result := make(map[Ref]Artifact, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		file, err := openRegularFile(filepath.Join(s.artifacts, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("loom: open artifact metadata %q: %w", entry.Name(), err)
		}
		var artifact Artifact
		decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
		decoder.DisallowUnknownFields()
		decodeErr := decoder.Decode(&artifact)
		closeErr := file.Close()
		if decodeErr != nil {
			return nil, fmt.Errorf("loom: decode artifact metadata %q: %w", entry.Name(), decodeErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("loom: close artifact metadata %q: %w", entry.Name(), closeErr)
		}
		if _, err := ParseRef(artifact.Ref.String()); err != nil {
			return nil, fmt.Errorf("loom: invalid artifact metadata %q: %w", entry.Name(), err)
		}
		if artifact.Ref.ID()+".json" != entry.Name() || len(artifact.Digest) != 64 {
			return nil, fmt.Errorf("loom: invalid artifact metadata %q", entry.Name())
		}
		if _, err := hex.DecodeString(artifact.Digest); err != nil {
			return nil, fmt.Errorf("loom: invalid artifact digest in %q", entry.Name())
		}
		result[artifact.Ref] = artifact
	}
	return result, nil
}

func (s *Store) blobSizesLocked() (map[string]int64, error) {
	entries, err := os.ReadDir(s.blobs)
	if err != nil {
		return nil, fmt.Errorf("loom: list blobs: %w", err)
	}
	result := make(map[string]int64, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("loom: inspect blob %q: %w", entry.Name(), err)
		}
		if entry.Type()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || len(entry.Name()) != 64 || strings.ContainsAny(entry.Name(), `/\\`) {
			return nil, fmt.Errorf("loom: invalid blob %q", entry.Name())
		}
		if _, err := hex.DecodeString(entry.Name()); err != nil {
			return nil, fmt.Errorf("loom: invalid blob %q", entry.Name())
		}
		result[entry.Name()] = info.Size()
	}
	return result, nil
}

func (s *Store) statsLocked() (Stats, error) {
	artifacts, err := os.ReadDir(s.artifacts)
	if err != nil {
		return Stats{}, fmt.Errorf("loom: list artifacts: %w", err)
	}
	blobs, err := s.blobSizesLocked()
	if err != nil {
		return Stats{}, err
	}
	stats := Stats{Blobs: len(blobs)}
	for _, entry := range artifacts {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".json" {
			stats.Artifacts++
		}
	}
	for _, size := range blobs {
		stats.Bytes += size
	}
	return stats, nil
}
