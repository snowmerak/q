package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/snowmerak/q/v2/pkg/domain"
	"github.com/snowmerak/q/v2/pkg/storage"
)

// LoomOptions configures capacity bounds and rooted garbage collection for LoomStore.
type LoomOptions struct {
	MaxArtifactBytes int64
	MaxStoreBytes    int64
	AutoGC           bool
	GCTriggerRatio   float64
	GCTargetRatio    float64
	GCGracePeriod    time.Duration
}

// LoomStats summarizes stored Loom artifacts and their backing blob consumption.
type LoomStats struct {
	Artifacts       int   `json:"artifacts"`
	Blobs           int   `json:"blobs"`
	Bytes           int64 `json:"bytes"`
	MaxStoreBytes   int64 `json:"max_store_bytes"`
	PinnedArtifacts int   `json:"pinned_artifacts"`
}

// LoomGCOptions parameterizes a manual or automated GC pass.
type LoomGCOptions struct {
	DryRun           bool
	ExtraRoots       []string
	ProtectNewerThan time.Time
	IgnoreGrace      bool
	TargetBytes      int64
}

// LoomGCResult reports artifact/blob reclamation metrics for a GC pass.
type LoomGCResult struct {
	Before           LoomStats `json:"before"`
	After            LoomStats `json:"after"`
	ArtifactsRemoved int       `json:"artifacts_removed"`
	BlobsRemoved     int       `json:"blobs_removed"`
	BytesReclaimed   int64     `json:"bytes_reclaimed"`
	DryRun           bool      `json:"dry_run"`
}

// LoomLease protects a set of transient root artifact IDs from concurrent GC
// while an active operation reads or processes them.
type LoomLease struct {
	store *LoomStore
	id    string
	once  sync.Once
}

type transientLease struct {
	expiresAt time.Time
	roots     []string
}

// Release frees the transient root lease. Safe to call multiple times or on nil.
func (l *LoomLease) Release() {
	if l == nil || l.store == nil || l.id == "" {
		return
	}
	l.once.Do(func() {
		l.store.opMu.Lock()
		delete(l.store.leases, l.id)
		l.store.opMu.Unlock()
	})
}

var loomRefPattern = regexp.MustCompile(`(?i)loom://([0-9a-f]{32})`)

// ExtractLoomRefs returns sorted, deduplicated 32-hex artifact IDs referenced via loom:// URIs.
func ExtractLoomRefs(values ...string) []string {
	seen := make(map[string]struct{})
	for _, value := range values {
		for _, match := range loomRefPattern.FindAllStringSubmatch(value, -1) {
			if len(match) < 2 {
				continue
			}
			id := strings.ToLower(match[1])
			seen[id] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

func normalizeLoomOptions(opts LoomOptions) LoomOptions {
	if opts.MaxArtifactBytes <= 0 {
		opts.MaxArtifactBytes = defaultMaxArtifactBytes
	}
	if opts.MaxStoreBytes < 0 {
		opts.MaxStoreBytes = defaultMaxStoreBytes
	} else if opts.MaxStoreBytes == 0 {
		opts.MaxStoreBytes = defaultMaxStoreBytes
		if opts.MaxArtifactBytes > opts.MaxStoreBytes {
			opts.MaxStoreBytes = opts.MaxArtifactBytes
		}
	}
	if !(opts.GCTriggerRatio > 0 && opts.GCTriggerRatio < 1) {
		opts.GCTriggerRatio = defaultGCTriggerRatio
	}
	if !(opts.GCTargetRatio > 0 && opts.GCTargetRatio < opts.GCTriggerRatio) {
		target := defaultGCTargetRatio
		if target >= opts.GCTriggerRatio {
			target = opts.GCTriggerRatio * 0.75
		}
		opts.GCTargetRatio = target
	}
	if opts.GCGracePeriod == 0 {
		opts.GCGracePeriod = defaultGCGracePeriod
	}
	return opts
}

// AcquireLease registers an expiring transient GC root lease for the given artifact IDs/URIs.
func (l *LoomStore) AcquireLease(ttl time.Duration, refs ...string) *LoomLease {
	if l == nil || len(refs) == 0 {
		return &LoomLease{}
	}
	if ttl <= 0 {
		ttl = transientReadLeaseTTL
	}
	normalized := make([]string, 0, len(refs))
	seen := make(map[string]struct{}, len(refs))
	for _, r := range refs {
		id := NormalizeLoomID(r)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		normalized = append(normalized, id)
	}
	if len(normalized) == 0 {
		return &LoomLease{}
	}
	leaseID := domain.NewID()
	l.opMu.Lock()
	if l.leases == nil {
		l.leases = make(map[string]transientLease)
	}
	l.pruneExpiredLeasesLocked(l.nowFn())
	l.leases[leaseID] = transientLease{
		expiresAt: l.nowFn().Add(ttl),
		roots:     normalized,
	}
	l.opMu.Unlock()
	return &LoomLease{store: l, id: leaseID}
}

func (l *LoomStore) pruneExpiredLeasesLocked(now time.Time) {
	for id, lease := range l.leases {
		if !now.Before(lease.expiresAt) {
			delete(l.leases, id)
		}
	}
}

// Stats returns current Loom artifact count, distinct backing blob count, and total bytes.
func (l *LoomStore) Stats(ctx context.Context) (LoomStats, error) {
	if l == nil || l.rdb == nil {
		return LoomStats{}, errors.New("loom: artifact store unavailable")
	}
	if err := ctx.Err(); err != nil {
		return LoomStats{}, err
	}
	l.opMu.Lock()
	defer l.opMu.Unlock()

	opts := l.currentOptions()
	artifacts, err := l.rdb.Artifacts().ListByKind(ctx, domain.ArtifactKindLoom)
	if err != nil {
		return LoomStats{}, err
	}
	return summarizeLoomArtifacts(artifacts, opts.MaxStoreBytes), nil
}

// Collect discovers durable and transient GC roots, preserves reachable ancestors and
// artifacts inside the grace window, and removes unreachable Loom artifacts oldest-first.
func (l *LoomStore) Collect(ctx context.Context, opts LoomGCOptions) (LoomGCResult, error) {
	if l == nil || l.rdb == nil || l.blob == nil {
		return LoomGCResult{}, errors.New("loom: artifact store unavailable")
	}
	if err := ctx.Err(); err != nil {
		return LoomGCResult{}, err
	}
	if opts.TargetBytes < 0 {
		return LoomGCResult{}, errors.New("loom: target_bytes must not be negative")
	}
	l.opMu.Lock()
	defer l.opMu.Unlock()

	return l.collectLocked(ctx, l.currentOptions(), opts)
}

func summarizeLoomArtifacts(artifacts []domain.Artifact, maxStoreBytes int64) LoomStats {
	blobSizes := make(map[string]int64, len(artifacts))
	pinnedCount := 0
	for _, a := range artifacts {
		if _, exists := blobSizes[a.BlobKey]; !exists {
			blobSizes[a.BlobKey] = a.ByteSize
		}
		if a.Pinned {
			pinnedCount++
		}
	}
	var totalBytes int64
	for _, sz := range blobSizes {
		totalBytes += sz
	}
	return LoomStats{
		Artifacts:       len(artifacts),
		Blobs:           len(blobSizes),
		Bytes:           totalBytes,
		MaxStoreBytes:   maxStoreBytes,
		PinnedArtifacts: pinnedCount,
	}
}

func extractParentRefs(sourceMetaJSON string) []string {
	if strings.TrimSpace(sourceMetaJSON) == "" || sourceMetaJSON == "{}" {
		return nil
	}
	var meta struct {
		Parent  string   `json:"parent"`
		Parents []string `json:"parents"`
	}
	if err := json.Unmarshal([]byte(sourceMetaJSON), &meta); err != nil {
		return nil
	}
	out := make([]string, 0, len(meta.Parents)+1)
	if id := NormalizeLoomID(meta.Parent); id != "" {
		out = append(out, id)
	}
	for _, p := range meta.Parents {
		if id := NormalizeLoomID(p); id != "" {
			out = append(out, id)
		}
	}
	return out
}

func (l *LoomStore) collectLocked(ctx context.Context, storeOpts LoomOptions, opts LoomGCOptions) (LoomGCResult, error) {
	artifacts, err := l.rdb.Artifacts().ListByKind(ctx, domain.ArtifactKindLoom)
	if err != nil {
		return LoomGCResult{}, err
	}
	candidates, err := l.rdb.Artifacts().ListLoomRootCandidates(ctx)
	if err != nil {
		return LoomGCResult{}, err
	}

	byID := make(map[string]domain.Artifact, len(artifacts))
	blobSizes := make(map[string]int64, len(artifacts))
	blobRefs := make(map[string]int, len(artifacts))
	for _, a := range artifacts {
		byID[a.ID] = a
		blobRefs[a.BlobKey]++
		if _, exists := blobSizes[a.BlobKey]; !exists {
			blobSizes[a.BlobKey] = a.ByteSize
		}
	}
	before := summarizeLoomArtifacts(artifacts, storeOpts.MaxStoreBytes)
	result := LoomGCResult{
		Before: before,
		After:  before,
		DryRun: opts.DryRun,
	}

	marked := make(map[string]struct{}, len(artifacts))
	var mark func(ref string)
	mark = func(ref string) {
		id := NormalizeLoomID(ref)
		if id == "" {
			return
		}
		if _, exists := marked[id]; exists {
			return
		}
		art, exists := byID[id]
		if !exists {
			return
		}
		marked[id] = struct{}{}
		for _, parent := range extractParentRefs(art.SourceMetaJSON) {
			mark(parent)
		}
	}

	for _, a := range artifacts {
		if a.Pinned {
			mark(a.ID)
		}
	}
	for _, id := range candidates.ActiveInvocationArtifactIDs {
		mark(id)
	}
	for _, id := range ExtractLoomRefs(candidates.ReferencedTexts...) {
		mark(id)
	}
	for _, extra := range opts.ExtraRoots {
		mark(extra)
	}

	now := l.nowFn()
	l.pruneExpiredLeasesLocked(now)
	for _, lease := range l.leases {
		for _, root := range lease.roots {
			mark(root)
		}
	}

	if !opts.IgnoreGrace {
		protectAfter := opts.ProtectNewerThan
		if protectAfter.IsZero() && storeOpts.GCGracePeriod > 0 {
			protectAfter = now.Add(-storeOpts.GCGracePeriod)
		}
		if !protectAfter.IsZero() {
			cutoffMS := protectAfter.UnixMilli()
			for _, a := range artifacts {
				if a.CreatedAt >= cutoffMS {
					mark(a.ID)
				}
			}
		}
	}

	sweepCandidates := make([]domain.Artifact, 0, len(artifacts))
	for _, a := range artifacts {
		if _, keep := marked[a.ID]; !keep {
			sweepCandidates = append(sweepCandidates, a)
		}
	}
	sort.Slice(sweepCandidates, func(i, j int) bool {
		if sweepCandidates[i].CreatedAt == sweepCandidates[j].CreatedAt {
			return sweepCandidates[i].ID < sweepCandidates[j].ID
		}
		return sweepCandidates[i].CreatedAt < sweepCandidates[j].CreatedAt
	})

	for _, a := range sweepCandidates {
		if err := ctx.Err(); err != nil {
			return LoomGCResult{}, err
		}
		if opts.TargetBytes > 0 && result.After.Bytes <= opts.TargetBytes {
			break
		}
		if !opts.DryRun {
			if err := l.rdb.Tx(ctx, func(tx storage.RDBTx) error {
				if err := tx.Artifacts().ClearToolInvocationRefs(ctx, a.ID); err != nil {
					return err
				}
				if err := tx.Artifacts().Delete(ctx, a.ID); err != nil && !errors.Is(err, storage.ErrNotFound) {
					return err
				}
				return nil
			}); err != nil {
				return LoomGCResult{}, fmt.Errorf("loom: delete artifact %s: %w", a.ID, err)
			}
		}
		result.ArtifactsRemoved++
		result.After.Artifacts--
		if a.Pinned {
			result.After.PinnedArtifacts--
		}

		blobRefs[a.BlobKey]--
		if blobRefs[a.BlobKey] == 0 {
			if !opts.DryRun {
				if err := l.blob.Delete(ctx, a.BlobKey); err != nil && !errors.Is(err, storage.ErrNotFound) {
					return LoomGCResult{}, fmt.Errorf("loom: delete blob %s: %w", a.BlobKey, err)
				}
			}
			sz := blobSizes[a.BlobKey]
			result.BlobsRemoved++
			result.BytesReclaimed += sz
			result.After.Blobs--
			result.After.Bytes -= sz
			delete(blobSizes, a.BlobKey)
		}
	}
	return result, nil
}

// StartGCWorker launches a background goroutine that periodically triggers Loom GC
// whenever AutoGC is enabled and total Loom bytes exceed MaxStoreBytes * GCTriggerRatio.
func (l *LoomStore) StartGCWorker(ctx context.Context, interval time.Duration, onError func(error)) {
	if l == nil {
		return
	}
	if interval <= 0 {
		interval = defaultGCInterval
	}
	l.gcMu.Lock()
	if l.gcCancel != nil {
		l.gcMu.Unlock()
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	workerCtx, cancel := context.WithCancel(ctx)
	l.gcCancel = cancel
	l.gcWG.Add(1)
	l.gcMu.Unlock()

	go func() {
		defer l.gcWG.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-workerCtx.Done():
				return
			case <-ticker.C:
				if err := l.runScheduledSweep(workerCtx); err != nil && !errors.Is(err, context.Canceled) && onError != nil {
					onError(err)
				}
			}
		}
	}()
}

// StopGCWorker stops the periodic GC goroutine if running and waits for exit.
func (l *LoomStore) StopGCWorker() {
	if l == nil {
		return
	}
	l.gcMu.Lock()
	cancel := l.gcCancel
	l.gcCancel = nil
	l.gcMu.Unlock()
	if cancel != nil {
		cancel()
	}
	l.gcWG.Wait()
}

func (l *LoomStore) runScheduledSweep(ctx context.Context) error {
	opts := l.currentOptions()
	if !opts.AutoGC || opts.MaxStoreBytes <= 0 {
		return nil
	}
	stats, err := l.Stats(ctx)
	if err != nil {
		return err
	}
	triggerBytes := int64(float64(opts.MaxStoreBytes) * opts.GCTriggerRatio)
	if stats.Bytes <= triggerBytes {
		return nil
	}
	targetBytes := int64(float64(opts.MaxStoreBytes) * opts.GCTargetRatio)
	_, err = l.Collect(ctx, LoomGCOptions{TargetBytes: targetBytes})
	return err
}
