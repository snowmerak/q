// Package localfs implements storage.BlobStore backed by a local directory.
package localfs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/snowmerak/q/v2/internal/fsutil"
	"github.com/snowmerak/q/v2/pkg/storage"
)

type Store struct {
	root string
}

var _ storage.BlobStore = (*Store)(nil)

func Open(root string) (*Store, error) {
	abs, err := filepath.Abs(strings.TrimSpace(root))
	if err != nil {
		return nil, fmt.Errorf("%w: resolve blob root: %v", storage.ErrInvalidInput, err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("create blob root: %w", err)
	}
	return &Store{root: abs}, nil
}

func (s *Store) resolveKey(key string) (string, error) {
	clean := filepath.Clean(strings.TrimSpace(filepath.FromSlash(key)))
	if clean == "." || clean == ".." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: invalid blob key %q", storage.ErrInvalidInput, key)
	}
	full := filepath.Join(s.root, clean)
	rel, err := filepath.Rel(s.root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: blob key escapes root %q", storage.ErrInvalidInput, key)
	}
	return full, nil
}

func (s *Store) Put(ctx context.Context, key string, r io.Reader, _ string) (int64, string, error) {
	if err := ctx.Err(); err != nil {
		return 0, "", err
	}
	if r == nil {
		return 0, "", fmt.Errorf("%w: nil reader", storage.ErrInvalidInput)
	}
	target, err := s.resolveKey(key)
	if err != nil {
		return 0, "", err
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return 0, "", fmt.Errorf("read blob payload: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return 0, "", err
	}
	sum := sha256.Sum256(data)
	if err := fsutil.WriteFileAtomic(target, data, 0o600); err != nil {
		return 0, "", fmt.Errorf("write blob: %w", err)
	}
	return int64(len(data)), hex.EncodeToString(sum[:]), nil
}

func (s *Store) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	target, err := s.resolveKey(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", storage.ErrNotFound, key)
		}
		return nil, err
	}
	return f, nil
}

func (s *Store) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	target, err := s.resolveKey(key)
	if err != nil {
		return err
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s *Store) Close() error { return nil }
