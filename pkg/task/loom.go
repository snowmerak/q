package task

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
	"github.com/snowmerak/q/v2/pkg/domain"
	"github.com/snowmerak/q/v2/pkg/storage"
)

const (
	defaultInlineThresholdBytes = 16 << 10
	defaultLoomReadBytes        = 64 << 10
	maxLoomReadBytes            = 1 << 20
	defaultMaxArtifactBytes     = 64 << 20
	defaultMaxStoreBytes        = 256 << 20
	defaultGCTriggerRatio       = 0.80
	defaultGCTargetRatio        = 0.60
	defaultGCGracePeriod        = 1 * time.Hour
	defaultGCInterval           = 15 * time.Minute
	transientReadLeaseTTL       = 2 * time.Minute
	loomEvalTimeout             = 2 * time.Second
	loomEvalMaxOutputBytes      = 64 << 10
)

type LoomStore struct {
	rdb             storage.RDBStore
	blob            storage.BlobStore
	inlineThreshold int

	opMu   sync.Mutex
	leases map[string]transientLease
	nowFn  func() time.Time

	cfgMu sync.RWMutex
	opts  LoomOptions

	gcMu     sync.Mutex
	gcCancel context.CancelFunc
	gcWG     sync.WaitGroup
}

// NewLoomStore constructs a LoomStore with the given single-artifact cap and default store/GC options.
func NewLoomStore(rdb storage.RDBStore, blob storage.BlobStore, maxArtifactBytes int64) *LoomStore {
	if maxArtifactBytes <= 0 {
		maxArtifactBytes = defaultMaxArtifactBytes
	}
	maxStore := int64(defaultMaxStoreBytes)
	if maxArtifactBytes > maxStore {
		maxStore = maxArtifactBytes
	}
	return NewLoomStoreWithOptions(rdb, blob, LoomOptions{
		MaxArtifactBytes: maxArtifactBytes,
		MaxStoreBytes:    maxStore,
		AutoGC:           true,
		GCTriggerRatio:   defaultGCTriggerRatio,
		GCTargetRatio:    defaultGCTargetRatio,
		GCGracePeriod:    defaultGCGracePeriod,
	})
}

// NewLoomStoreWithOptions constructs a LoomStore configured with explicit limits and GC settings.
func NewLoomStoreWithOptions(rdb storage.RDBStore, blob storage.BlobStore, opts LoomOptions) *LoomStore {
	return &LoomStore{
		rdb:             rdb,
		blob:            blob,
		inlineThreshold: defaultInlineThresholdBytes,
		leases:          make(map[string]transientLease),
		nowFn:           func() time.Time { return time.Now().UTC() },
		opts:            normalizeLoomOptions(opts),
	}
}

// Configure updates store limits and GC behavior dynamically for hot-reload.
func (l *LoomStore) Configure(opts LoomOptions) {
	if l == nil {
		return
	}
	normalized := normalizeLoomOptions(opts)
	l.cfgMu.Lock()
	l.opts = normalized
	l.cfgMu.Unlock()
}

func (l *LoomStore) currentOptions() LoomOptions {
	l.cfgMu.RLock()
	opts := l.opts
	l.cfgMu.RUnlock()
	return opts
}

func (l *LoomStore) Put(ctx context.Context, content, mediaType string, meta map[string]any) (domain.Artifact, error) {
	if l == nil || l.rdb == nil || l.blob == nil {
		return domain.Artifact{}, errors.New("loom: artifact store unavailable")
	}

	l.opMu.Lock()
	defer l.opMu.Unlock()

	opts := l.currentOptions()
	incoming := int64(len(content))
	if incoming > opts.MaxArtifactBytes {
		return domain.Artifact{}, fmt.Errorf("loom: content size %d exceeds max %d", incoming, opts.MaxArtifactBytes)
	}

	if opts.MaxStoreBytes > 0 {
		artifacts, err := l.rdb.Artifacts().ListByKind(ctx, domain.ArtifactKindLoom)
		if err != nil {
			return domain.Artifact{}, err
		}
		stats := summarizeLoomArtifacts(artifacts, opts.MaxStoreBytes)
		currentBytes := stats.Bytes
		triggerBytes := int64(float64(opts.MaxStoreBytes) * opts.GCTriggerRatio)
		if opts.AutoGC && currentBytes+incoming > triggerBytes {
			targetBytes := int64(float64(opts.MaxStoreBytes) * opts.GCTargetRatio)
			gcRes, err := l.collectLocked(ctx, opts, LoomGCOptions{
				TargetBytes: targetBytes,
			})
			if err != nil {
				return domain.Artifact{}, fmt.Errorf("loom: auto-gc failed: %w", err)
			}
			currentBytes = gcRes.After.Bytes
		}
		if currentBytes+incoming > opts.MaxStoreBytes {
			return domain.Artifact{}, fmt.Errorf("loom: store size %d + %d exceeds max_store_bytes %d", currentBytes, incoming, opts.MaxStoreBytes)
		}
	}

	if mediaType == "" {
		mediaType = "text/plain; charset=utf-8"
	}
	id := strings.ReplaceAll(domain.NewID(), "-", "")
	blobKey := "loom/" + id
	size, shaHex, err := l.blob.Put(ctx, blobKey, strings.NewReader(content), mediaType)
	if err != nil {
		return domain.Artifact{}, err
	}
	metaBytes, _ := json.Marshal(meta)
	now := l.nowFn().UTC().UnixMilli()
	art := domain.Artifact{
		ID:             id,
		Kind:           domain.ArtifactKindLoom,
		MediaType:      mediaType,
		ByteSize:       size,
		SHA256:         shaHex,
		BlobKey:        blobKey,
		SourceMetaJSON: string(metaBytes),
		CreatedAt:      now,
		LastAccessedAt: now,
	}
	if err := l.rdb.Artifacts().Create(ctx, art); err != nil {
		_ = l.blob.Delete(ctx, blobKey)
		return domain.Artifact{}, err
	}
	return art, nil
}

// SpillIfLarge keeps short outputs inline and offloads outputs exceeding 16 KiB
// into immutable Loom storage, returning a compact summary + loom://<32hex> URI.
func (l *LoomStore) SpillIfLarge(ctx context.Context, content, mediaType string, meta map[string]any) (inline string, artifactID string, spilled bool, err error) {
	if l == nil || l.rdb == nil || l.blob == nil || len(content) <= l.inlineThreshold {
		return content, "", false, nil
	}
	art, err := l.Put(ctx, content, mediaType, meta)
	if err != nil {
		return "", "", false, err
	}
	preview := content
	if len(preview) > 1024 {
		preview = preview[:1024] + "\n... [truncated]"
	}
	msg := fmt.Sprintf("Output exceeded inline limit (%d bytes) and was stored as Loom artifact loom://%s.\nUse loom_inspect, loom_read, or loom_eval with id=%q.\n\nPreview:\n%s",
		art.ByteSize, art.ID, "loom://"+art.ID, preview)
	return msg, art.ID, true, nil
}

func NormalizeLoomID(ref string) string {
	ref = strings.TrimSpace(ref)
	return strings.TrimPrefix(ref, "loom://")
}

func (l *LoomStore) ReadAll(ctx context.Context, ref string) (domain.Artifact, []byte, error) {
	id := NormalizeLoomID(ref)
	lease := l.AcquireLease(transientReadLeaseTTL, id)
	defer lease.Release()

	art, err := l.rdb.Artifacts().Get(ctx, id)
	if err != nil {
		return domain.Artifact{}, nil, err
	}
	rc, err := l.blob.Open(ctx, art.BlobKey)
	if err != nil {
		return domain.Artifact{}, nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, maxLoomReadBytes*8))
	if err != nil {
		return domain.Artifact{}, nil, err
	}
	_ = l.rdb.Artifacts().Touch(ctx, art.ID, l.nowFn().UTC().UnixMilli())
	return art, data, nil
}

func (l *LoomStore) ReadRange(ctx context.Context, ref string, offset, limit int64) (domain.Artifact, string, bool, error) {
	id := NormalizeLoomID(ref)
	lease := l.AcquireLease(transientReadLeaseTTL, id)
	defer lease.Release()

	art, err := l.rdb.Artifacts().Get(ctx, id)
	if err != nil {
		return domain.Artifact{}, "", false, err
	}
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = defaultLoomReadBytes
	}
	if limit > maxLoomReadBytes {
		limit = maxLoomReadBytes
	}
	rc, err := l.blob.Open(ctx, art.BlobKey)
	if err != nil {
		return domain.Artifact{}, "", false, err
	}
	defer rc.Close()

	if offset > 0 {
		if _, err := io.CopyN(io.Discard, rc, offset); err != nil && err != io.EOF {
			return domain.Artifact{}, "", false, err
		}
	}
	buf, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return domain.Artifact{}, "", false, err
	}
	truncated := int64(len(buf)) > limit
	if truncated {
		buf = buf[:limit]
	}
	_ = l.rdb.Artifacts().Touch(ctx, art.ID, l.nowFn().UTC().UnixMilli())
	return art, string(buf), truncated, nil
}

func registerLoomTools(r *Registry, loom *LoomStore) {
	r.register(ToolSpec{
		Name:        "loom_inspect",
		Description: "Inspect metadata, size, line count, and preview for a loom://<id> artifact.",
		Source:      domain.ToolSourceBuiltin,
		Risk:        RiskRead,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string", "description": "Loom URI (loom://...) or artifact ID."},
			},
			"required": []string{"id"},
		},
		Handler: func(ctx context.Context, _ CallContext, argsJSON string) (ToolOutput, error) {
			if loom == nil {
				return ToolOutput{Content: "Loom store unavailable", IsError: true}, nil
			}
			var args struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
				return ToolOutput{Content: "invalid args: " + err.Error(), IsError: true}, nil
			}
			art, preview, truncated, err := loom.ReadRange(ctx, args.ID, 0, 2048)
			if err != nil {
				return ToolOutput{Content: "loom_inspect failed: " + err.Error(), IsError: true}, nil
			}
			lineCount := bytes.Count([]byte(preview), []byte{'\n'}) + 1
			out, _ := json.MarshalIndent(map[string]any{
				"uri":             "loom://" + art.ID,
				"media_type":      art.MediaType,
				"byte_size":       art.ByteSize,
				"sha256":          art.SHA256,
				"preview_lines":   lineCount,
				"preview":         preview,
				"preview_partial": truncated,
			}, "", "  ")
			return ToolOutput{Content: string(out)}, nil
		},
	})

	r.register(ToolSpec{
		Name:        "loom_read",
		Description: "Read a byte range from a loom://<id> artifact (default 64 KiB, max 1 MiB).",
		Source:      domain.ToolSourceBuiltin,
		Risk:        RiskRead,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":     map[string]any{"type": "string"},
				"offset": map[string]any{"type": "integer"},
				"limit":  map[string]any{"type": "integer"},
			},
			"required": []string{"id"},
		},
		Handler: func(ctx context.Context, _ CallContext, argsJSON string) (ToolOutput, error) {
			if loom == nil {
				return ToolOutput{Content: "Loom store unavailable", IsError: true}, nil
			}
			var args struct {
				ID     string `json:"id"`
				Offset int64  `json:"offset"`
				Limit  int64  `json:"limit"`
			}
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
				return ToolOutput{Content: "invalid args: " + err.Error(), IsError: true}, nil
			}
			art, text, truncated, err := loom.ReadRange(ctx, args.ID, args.Offset, args.Limit)
			if err != nil {
				return ToolOutput{Content: "loom_read failed: " + err.Error(), IsError: true}, nil
			}
			header := fmt.Sprintf("[loom://%s bytes=%d offset=%d truncated=%v]\n", art.ID, art.ByteSize, args.Offset, truncated)
			return ToolOutput{Content: header + text}, nil
		},
	})

	r.register(ToolSpec{
		Name:        "loom_eval",
		Description: "Run a bounded JavaScript expression/program against a loom://<id> artifact. Variables exposed: `content` (string) and `data` (parsed JSON if valid, else null).",
		Source:      domain.ToolSourceBuiltin,
		Risk:        RiskRead,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":     map[string]any{"type": "string"},
				"script": map[string]any{"type": "string", "description": "JS expression or IIFE returning a string or JSON-serializable value."},
			},
			"required": []string{"id", "script"},
		},
		Handler: func(ctx context.Context, _ CallContext, argsJSON string) (ToolOutput, error) {
			if loom == nil {
				return ToolOutput{Content: "Loom store unavailable", IsError: true}, nil
			}
			var args struct {
				ID     string `json:"id"`
				Script string `json:"script"`
			}
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
				return ToolOutput{Content: "invalid args: " + err.Error(), IsError: true}, nil
			}
			if strings.TrimSpace(args.Script) == "" {
				return ToolOutput{Content: "script is required", IsError: true}, nil
			}
			_, raw, err := loom.ReadAll(ctx, args.ID)
			if err != nil {
				return ToolOutput{Content: "loom_eval read failed: " + err.Error(), IsError: true}, nil
			}
			result, evalErr := evalLoomJS(ctx, string(raw), args.Script, loomEvalTimeout)
			if evalErr != nil {
				return ToolOutput{Content: "loom_eval error: " + evalErr.Error(), IsError: true}, nil
			}
			if len(result) > loomEvalMaxOutputBytes {
				result = result[:loomEvalMaxOutputBytes] + "\n... [output truncated at 64 KiB]"
			}
			return ToolOutput{Content: result}, nil
		},
	})
}

func evalLoomJS(ctx context.Context, content, script string, timeout time.Duration) (string, error) {
	vm := goja.New()
	_ = vm.Set("content", content)

	var parsed any
	if json.Unmarshal([]byte(content), &parsed) == nil {
		_ = vm.Set("data", parsed)
	} else {
		_ = vm.Set("data", nil)
	}

	if timeout <= 0 {
		timeout = loomEvalTimeout
	}
	timer := time.AfterFunc(timeout, func() {
		vm.Interrupt("loom_eval timeout")
	})
	defer timer.Stop()

	stopCtx := context.AfterFunc(ctx, func() {
		vm.Interrupt("loom_eval cancelled")
	})
	defer stopCtx()

	val, err := vm.RunString(script)
	if err != nil {
		return "", err
	}
	if val == nil || goja.IsUndefined(val) || goja.IsNull(val) {
		return "", nil
	}
	exported := val.Export()
	if s, ok := exported.(string); ok {
		return s, nil
	}
	b, err := json.MarshalIndent(exported, "", "  ")
	if err != nil {
		return val.String(), nil
	}
	return string(b), nil
}
