// Package loom stores immutable tool results and script-derived data outside
// the model request context.
package loom

import (
	"context"
	"crypto/rand"
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
)

const (
	DirectoryName               = "loom"
	DefaultMaximumArtifactBytes = 64 << 20
	DefaultMaximumStoreBytes    = 256 << 20
	DefaultGCTriggerRatio       = 0.80
	DefaultGCTargetRatio        = 0.60
	DefaultGCGracePeriod        = time.Hour
	defaultReadLimit            = 64 << 10
	maximumReadLimit            = 1 << 20
)

var ErrNotFound = errors.New("loom: artifact not found")

type Ref string

func (r Ref) String() string { return string(r) }

func ParseRef(value string) (Ref, error) {
	const prefix = "loom://"
	if !strings.HasPrefix(value, prefix) {
		return "", fmt.Errorf("loom: invalid reference %q", value)
	}
	id := strings.TrimPrefix(value, prefix)
	if len(id) != 32 {
		return "", fmt.Errorf("loom: invalid reference %q", value)
	}
	if _, err := hex.DecodeString(id); err != nil {
		return "", fmt.Errorf("loom: invalid reference %q", value)
	}
	return Ref(value), nil
}

func (r Ref) ID() string { return strings.TrimPrefix(string(r), "loom://") }

type Artifact struct {
	Ref       Ref               `json:"ref"`
	Kind      string            `json:"kind"`
	MediaType string            `json:"media_type"`
	Bytes     int64             `json:"bytes"`
	Digest    string            `json:"digest"`
	CreatedAt time.Time         `json:"created_at"`
	Parents   []Ref             `json:"parents,omitempty"`
	Source    map[string]string `json:"source,omitempty"`
}

type PutOptions struct {
	Kind      string
	MediaType string
	Parents   []Ref
	Source    map[string]string
}

type ReadResult struct {
	Content    []byte
	Offset     int64
	NextOffset int64
	More       bool
}

type RootProvider func(context.Context) ([]Ref, error)

type StoreOptions struct {
	MaximumArtifactBytes int64
	MaximumStoreBytes    int64
	AutoGC               bool
	GCTriggerRatio       float64
	GCTargetRatio        float64
	GCGracePeriod        time.Duration
	Roots                RootProvider
}

type Store struct {
	root      string
	artifacts string
	blobs     string
	options   StoreOptions
	mu        sync.Mutex
}

func Open(workspaceRoot string) (*Store, error) {
	return OpenWithOptions(workspaceRoot, StoreOptions{})
}

func OpenWithOptions(workspaceRoot string, options StoreOptions) (*Store, error) {
	options, err := normalizeStoreOptions(options)
	if err != nil {
		return nil, err
	}
	root := filepath.Join(workspaceRoot, ".q", DirectoryName)
	store := &Store{
		root:      root,
		artifacts: filepath.Join(root, "artifacts"),
		blobs:     filepath.Join(root, "blobs"),
		options:   options,
	}
	for _, directory := range []string{root, store.artifacts, store.blobs} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, fmt.Errorf("loom: create %s: %w", directory, err)
		}
		if err := os.Chmod(directory, 0o700); err != nil {
			return nil, fmt.Errorf("loom: secure %s: %w", directory, err)
		}
	}
	return store, nil
}

func normalizeStoreOptions(options StoreOptions) (StoreOptions, error) {
	if options.MaximumArtifactBytes == 0 {
		options.MaximumArtifactBytes = DefaultMaximumArtifactBytes
	}
	if options.MaximumStoreBytes == 0 {
		options.MaximumStoreBytes = DefaultMaximumStoreBytes
	}
	if options.GCTriggerRatio == 0 {
		options.GCTriggerRatio = DefaultGCTriggerRatio
	}
	if options.GCTargetRatio == 0 {
		options.GCTargetRatio = DefaultGCTargetRatio
	}
	if options.GCGracePeriod == 0 {
		options.GCGracePeriod = DefaultGCGracePeriod
	}
	if options.MaximumArtifactBytes < 1 || options.MaximumStoreBytes < 1 {
		return StoreOptions{}, errors.New("loom: maximum artifact and store sizes must be positive")
	}
	if options.MaximumArtifactBytes > options.MaximumStoreBytes {
		return StoreOptions{}, errors.New("loom: maximum artifact size cannot exceed maximum store size")
	}
	if options.GCTargetRatio <= 0 || options.GCTriggerRatio <= options.GCTargetRatio || options.GCTriggerRatio >= 1 {
		return StoreOptions{}, errors.New("loom: GC ratios must satisfy 0 < target < trigger < 1")
	}
	if options.GCGracePeriod < 0 {
		return StoreOptions{}, errors.New("loom: GC grace period must not be negative")
	}
	return options, nil
}

func (s *Store) Configure(options StoreOptions) error {
	options, err := normalizeStoreOptions(options)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.options = options
	s.mu.Unlock()
	return nil
}

func (s *Store) Options() StoreOptions {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.options
}

func (s *Store) WorkspaceRoot() string {
	return filepath.Dir(filepath.Dir(s.root))
}

func (s *Store) Put(ctx context.Context, content []byte, options PutOptions) (Artifact, error) {
	if err := ctx.Err(); err != nil {
		return Artifact{}, err
	}
	s.mu.Lock()
	maximumArtifactBytes := s.options.MaximumArtifactBytes
	s.mu.Unlock()
	if int64(len(content)) > maximumArtifactBytes {
		return Artifact{}, fmt.Errorf("loom: artifact exceeds %d bytes", maximumArtifactBytes)
	}
	if options.Kind == "" {
		return Artifact{}, errors.New("loom: artifact kind is required")
	}
	if options.MediaType == "" {
		options.MediaType = "application/octet-stream"
	}
	for _, parent := range options.Parents {
		if _, err := ParseRef(parent.String()); err != nil {
			return Artifact{}, err
		}
	}

	digestBytes := sha256.Sum256(content)
	digest := hex.EncodeToString(digestBytes[:])
	if err := s.maybeAutoCollect(ctx, digest, int64(len(content))); err != nil {
		return Artifact{}, err
	}
	id, err := randomID()
	if err != nil {
		return Artifact{}, err
	}
	artifact := Artifact{
		Ref: Ref("loom://" + id), Kind: options.Kind, MediaType: options.MediaType,
		Bytes: int64(len(content)), Digest: digest, CreatedAt: time.Now().UTC(),
		Parents: append([]Ref(nil), options.Parents...), Source: cloneSource(options.Source),
	}
	metadata, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		return Artifact{}, fmt.Errorf("loom: encode artifact metadata: %w", err)
	}
	metadata = append(metadata, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	blobPath := filepath.Join(s.blobs, digest)
	blobCreated := false
	if _, err := os.Lstat(blobPath); errors.Is(err, os.ErrNotExist) {
		size, sizeErr := s.storeSizeLocked()
		if sizeErr != nil {
			return Artifact{}, sizeErr
		}
		if size+int64(len(content)) > s.options.MaximumStoreBytes {
			return Artifact{}, fmt.Errorf("loom: workspace store would exceed %d bytes", s.options.MaximumStoreBytes)
		}
		if err := writeNewFile(blobPath, content); err != nil {
			return Artifact{}, err
		}
		blobCreated = true
	} else if err != nil {
		return Artifact{}, fmt.Errorf("loom: inspect existing blob: %w", err)
	} else if err := validateBlob(blobPath, digest, int64(len(content))); err != nil {
		return Artifact{}, err
	}
	if err := writeNewFile(filepath.Join(s.artifacts, id+".json"), metadata); err != nil {
		if blobCreated {
			_ = os.Remove(blobPath)
		}
		return Artifact{}, err
	}
	return artifact, nil
}

func (s *Store) Inspect(ctx context.Context, ref Ref) (Artifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inspectLocked(ctx, ref)
}

func (s *Store) inspectLocked(ctx context.Context, ref Ref) (Artifact, error) {
	if err := ctx.Err(); err != nil {
		return Artifact{}, err
	}
	parsed, err := ParseRef(ref.String())
	if err != nil {
		return Artifact{}, err
	}
	file, err := openRegularFile(filepath.Join(s.artifacts, parsed.ID()+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return Artifact{}, ErrNotFound
	}
	if err != nil {
		return Artifact{}, fmt.Errorf("loom: open artifact metadata: %w", err)
	}
	defer file.Close()
	var artifact Artifact
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&artifact); err != nil {
		return Artifact{}, fmt.Errorf("loom: decode artifact metadata: %w", err)
	}
	if artifact.Ref != parsed {
		return Artifact{}, errors.New("loom: artifact metadata reference mismatch")
	}
	if len(artifact.Digest) != 64 {
		return Artifact{}, errors.New("loom: artifact metadata has invalid digest")
	}
	if _, err := hex.DecodeString(artifact.Digest); err != nil {
		return Artifact{}, errors.New("loom: artifact metadata has invalid digest")
	}
	if artifact.Bytes < 0 {
		return Artifact{}, errors.New("loom: artifact metadata has invalid size")
	}
	return artifact, nil
}

func (s *Store) Read(ctx context.Context, ref Ref, offset, limit int64) (ReadResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	artifact, err := s.inspectLocked(ctx, ref)
	if err != nil {
		return ReadResult{}, err
	}
	if offset < 0 || offset > artifact.Bytes {
		return ReadResult{}, fmt.Errorf("loom: offset must be between 0 and %d", artifact.Bytes)
	}
	if limit == 0 {
		limit = defaultReadLimit
	}
	if limit < 0 || limit > maximumReadLimit {
		return ReadResult{}, fmt.Errorf("loom: limit must be between 1 and %d", maximumReadLimit)
	}
	file, err := openRegularFile(filepath.Join(s.blobs, artifact.Digest))
	if errors.Is(err, os.ErrNotExist) {
		return ReadResult{}, errors.New("loom: artifact blob is missing")
	}
	if err != nil {
		return ReadResult{}, fmt.Errorf("loom: open artifact blob: %w", err)
	}
	defer file.Close()
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return ReadResult{}, fmt.Errorf("loom: seek artifact blob: %w", err)
	}
	length := min(limit, artifact.Bytes-offset)
	content := make([]byte, length)
	if _, err := io.ReadFull(file, content); err != nil {
		return ReadResult{}, fmt.Errorf("loom: read artifact blob: %w", err)
	}
	next := offset + int64(len(content))
	return ReadResult{Content: content, Offset: offset, NextOffset: next, More: next < artifact.Bytes}, nil
}

func (s *Store) ReadAll(ctx context.Context, ref Ref, maximum int64) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	artifact, err := s.inspectLocked(ctx, ref)
	if err != nil {
		return nil, err
	}
	if maximum > 0 && artifact.Bytes > maximum {
		return nil, fmt.Errorf("loom: artifact has %d bytes, exceeding full-read limit %d", artifact.Bytes, maximum)
	}
	file, err := openRegularFile(filepath.Join(s.blobs, artifact.Digest))
	if errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("loom: artifact blob is missing")
	}
	if err != nil {
		return nil, fmt.Errorf("loom: open artifact blob: %w", err)
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, artifact.Bytes+1))
	if err != nil {
		return nil, fmt.Errorf("loom: read artifact blob: %w", err)
	}
	if int64(len(body)) != artifact.Bytes {
		return nil, errors.New("loom: artifact blob size does not match metadata")
	}
	return body, nil
}

func randomID() (string, error) {
	var body [16]byte
	if _, err := rand.Read(body[:]); err != nil {
		return "", fmt.Errorf("loom: generate artifact id: %w", err)
	}
	return hex.EncodeToString(body[:]), nil
}

func writeNewFile(path string, content []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(content); err != nil {
		return fmt.Errorf("loom: write %s: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("loom: sync %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("loom: close %s: %w", path, err)
	}
	keep = true
	return nil
}
func (s *Store) storeSizeLocked() (int64, error) {
	entries, err := os.ReadDir(s.blobs)
	if err != nil {
		return 0, fmt.Errorf("loom: list blobs: %w", err)
	}
	var total int64
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return 0, fmt.Errorf("loom: inspect blob %q: %w", entry.Name(), err)
		}
		if !info.Mode().IsRegular() {
			return 0, fmt.Errorf("loom: blob %q is not a regular file", entry.Name())
		}
		total += info.Size()
	}
	return total, nil
}

func validateBlob(path, expectedDigest string, expectedSize int64) error {
	file, err := openRegularFile(path)
	if err != nil {
		return fmt.Errorf("loom: open existing blob: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("loom: inspect existing blob: %w", err)
	}
	if info.Size() != expectedSize {
		return errors.New("loom: existing blob size does not match its digest")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return fmt.Errorf("loom: hash existing blob: %w", err)
	}
	if hex.EncodeToString(hash.Sum(nil)) != expectedDigest {
		return errors.New("loom: existing blob content does not match its digest")
	}
	return nil
}

func openRegularFile(path string) (*os.File, error) {
	linkInfo, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !linkInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("loom: %s is not a regular file", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() || !os.SameFile(linkInfo, info) {
		_ = file.Close()
		return nil, fmt.Errorf("loom: %s changed while opening", path)
	}
	return file, nil
}

func cloneSource(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
