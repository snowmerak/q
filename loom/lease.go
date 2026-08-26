package loom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	transientLeaseVersion      = 1
	transientLeaseDuration     = 5 * time.Minute
	transientLeaseCloseTimeout = 250 * time.Millisecond
	maximumLeaseBytes          = 1 << 20
)

type transientLeaseFile struct {
	Version   int       `json:"version"`
	ExpiresAt time.Time `json:"expires_at"`
	Roots     []Ref     `json:"roots"`
}

type transientRootLease struct {
	store *Store
	path  string
	once  sync.Once
	err   error
}

func (s *Store) acquireTransientRoots(ctx context.Context, refs []Ref) (*transientRootLease, error) {
	if len(refs) == 0 {
		return &transientRootLease{}, nil
	}
	roots := make([]Ref, 0, len(refs))
	seen := make(map[Ref]struct{}, len(refs))
	for _, ref := range refs {
		parsed, err := ParseRef(ref.String())
		if err != nil {
			return nil, err
		}
		if _, exists := seen[parsed]; exists {
			continue
		}
		seen[parsed] = struct{}{}
		roots = append(roots, parsed)
	}
	return withStoreOperation(ctx, s, func(context.Context) (*transientRootLease, error) {
		id, err := randomID()
		if err != nil {
			return nil, err
		}
		body, err := json.MarshalIndent(transientLeaseFile{
			Version: transientLeaseVersion, ExpiresAt: time.Now().UTC().Add(transientLeaseDuration), Roots: roots,
		}, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("loom: encode transient root lease: %w", err)
		}
		body = append(body, '\n')
		path := filepath.Join(s.leases, id+".json")
		if err := writeTransientLease(s.leases, path, body); err != nil {
			return nil, fmt.Errorf("loom: create transient root lease: %w", err)
		}
		return &transientRootLease{store: s, path: path}, nil
	})
}

func (l *transientRootLease) Close() error {
	if l == nil || l.store == nil || l.path == "" {
		return nil
	}
	l.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), transientLeaseCloseTimeout)
		defer cancel()
		_, l.err = withStoreOperation(ctx, l.store, func(context.Context) (struct{}, error) {
			err := os.Remove(l.path)
			if errors.Is(err, os.ErrNotExist) {
				err = nil
			}
			if err != nil {
				err = fmt.Errorf("loom: remove transient root lease: %w", err)
			}
			return struct{}{}, err
		})
	})
	return l.err
}

func (s *Store) loadTransientRootsLocked(ctx context.Context, now time.Time) ([]Ref, error) {
	entries, err := os.ReadDir(s.leases)
	if err != nil {
		return nil, fmt.Errorf("loom: list transient root leases: %w", err)
	}
	var roots []Ref
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(s.leases, entry.Name())
		if filepath.Ext(entry.Name()) != ".json" {
			if len(entry.Name()) > 7 && entry.Name()[:7] == ".lease-" {
				if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
					return nil, fmt.Errorf("loom: remove abandoned transient lease %q: %w", entry.Name(), err)
				}
			}
			continue
		}
		file, err := openRegularFile(path)
		if err != nil {
			return nil, fmt.Errorf("loom: open transient root lease %q: %w", entry.Name(), err)
		}
		info, err := file.Stat()
		if err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("loom: inspect transient root lease %q: %w", entry.Name(), err)
		}
		if info.Size() > maximumLeaseBytes {
			_ = file.Close()
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("loom: remove oversized transient root lease %q: %w", entry.Name(), err)
			}
			continue
		}
		var lease transientLeaseFile
		decoder := json.NewDecoder(io.LimitReader(file, maximumLeaseBytes))
		decoder.DisallowUnknownFields()
		decodeErr := decoder.Decode(&lease)
		var trailing any
		if decodeErr == nil {
			if trailingErr := decoder.Decode(&trailing); !errors.Is(trailingErr, io.EOF) {
				decodeErr = trailingErr
				if trailingErr == nil {
					decodeErr = errors.New("multiple JSON values")
				}
			}
		}
		closeErr := file.Close()
		if decodeErr != nil {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("loom: remove malformed transient root lease %q: %w", entry.Name(), err)
			}
			continue
		}
		if closeErr != nil {
			return nil, fmt.Errorf("loom: close transient root lease %q: %w", entry.Name(), closeErr)
		}
		if lease.Version != transientLeaseVersion || lease.ExpiresAt.IsZero() {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("loom: remove invalid transient root lease %q: %w", entry.Name(), err)
			}
			continue
		}
		if !now.Before(lease.ExpiresAt) {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("loom: remove expired transient root lease %q: %w", entry.Name(), err)
			}
			continue
		}
		leaseRoots := make([]Ref, 0, len(lease.Roots))
		valid := true
		for _, ref := range lease.Roots {
			parsed, err := ParseRef(ref.String())
			if err != nil {
				if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
					return nil, fmt.Errorf("loom: remove invalid transient root lease %q: %w", entry.Name(), removeErr)
				}
				valid = false
				break
			}
			leaseRoots = append(leaseRoots, parsed)
		}
		if valid {
			roots = append(roots, leaseRoots...)
		}
	}
	return roots, nil
}

func writeTransientLease(directory, path string, body []byte) error {
	file, err := os.CreateTemp(directory, ".lease-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := file.Name()
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	if _, err := file.Write(body); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	keep = true
	return nil
}
