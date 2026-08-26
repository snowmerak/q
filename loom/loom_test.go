package loom

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStorePersistsImmutableArtifactAndReadsRanges(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := store.Put(context.Background(), []byte(`{"items":[1,2,3]}`), PutOptions{
		Kind: "test", MediaType: "application/json", Source: map[string]string{"tool": "fixture"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Ref == "" || artifact.Bytes != 17 || artifact.Digest == "" {
		t.Fatalf("artifact = %#v", artifact)
	}

	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := reopened.Inspect(context.Background(), artifact.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Digest != artifact.Digest || metadata.Source["tool"] != "fixture" {
		t.Fatalf("metadata = %#v", metadata)
	}
	first, err := reopened.Read(context.Background(), artifact.Ref, 0, 8)
	if err != nil {
		t.Fatal(err)
	}
	if string(first.Content) != `{"items"` || first.NextOffset != 8 || !first.More {
		t.Fatalf("first range = %#v", first)
	}
	remaining, err := reopened.Read(context.Background(), artifact.Ref, first.NextOffset, 64)
	if err != nil {
		t.Fatal(err)
	}
	if string(first.Content)+string(remaining.Content) != `{"items":[1,2,3]}` || remaining.More {
		t.Fatalf("remaining range = %#v", remaining)
	}
}

func TestStoreRejectsUnknownArtifact(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Inspect(context.Background(), Ref("loom://00000000000000000000000000000000"))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("inspect error = %v", err)
	}
}

func TestStorePublicOperationsWaitForOperationLock(t *testing.T) {
	root := t.TempDir()
	first, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := first.Put(context.Background(), []byte("locked payload"), PutOptions{Kind: "test"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}

	operations := []struct {
		name string
		run  func(context.Context) error
	}{
		{name: "put", run: func(ctx context.Context) error {
			_, err := second.Put(ctx, []byte("another payload"), PutOptions{Kind: "test"})
			return err
		}},
		{name: "inspect", run: func(ctx context.Context) error {
			_, err := second.Inspect(ctx, artifact.Ref)
			return err
		}},
		{name: "read", run: func(ctx context.Context) error {
			_, err := second.Read(ctx, artifact.Ref, 0, 4)
			return err
		}},
		{name: "read-all", run: func(ctx context.Context) error {
			_, err := second.ReadAll(ctx, artifact.Ref, 1<<20)
			return err
		}},
		{name: "stats", run: func(ctx context.Context) error {
			_, err := second.Stats(ctx)
			return err
		}},
		{name: "collect", run: func(ctx context.Context) error {
			_, err := second.Collect(ctx, GCOptions{Roots: []Ref{artifact.Ref}, DryRun: true})
			return err
		}},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			holder, err := acquireOperationFileLock(context.Background(), filepath.Join(first.root, operationLockFileName))
			if err != nil {
				t.Fatal(err)
			}
			waitCtx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
			err = operation.run(waitCtx)
			cancel()
			if !errors.Is(err, context.DeadlineExceeded) {
				_ = holder.Close()
				t.Fatalf("operation while lock is held = %v, want deadline exceeded", err)
			}
			if err := holder.Close(); err != nil {
				t.Fatal(err)
			}
			if err := operation.run(context.Background()); err != nil {
				t.Fatalf("operation after lock release: %v", err)
			}
		})
	}
}

func TestSameStoreOperationGateObservesCancellation(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	<-store.operationGate
	defer func() { store.operationGate <- struct{}{} }()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, err := store.Stats(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("same-store operation gate error = %v, want deadline exceeded", err)
	}
}

func TestPutTimestampsArtifactAfterOperationWait(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	<-store.operationGate
	type putResult struct {
		artifact Artifact
		err      error
	}
	result := make(chan putResult, 1)
	go func() {
		artifact, err := store.Put(context.Background(), []byte("waited"), PutOptions{Kind: "test"})
		result <- putResult{artifact: artifact, err: err}
	}()
	time.Sleep(30 * time.Millisecond)
	releasedAt := time.Now().UTC()
	store.operationGate <- struct{}{}
	stored := <-result
	if stored.err != nil {
		t.Fatal(stored.err)
	}
	if stored.artifact.CreatedAt.Before(releasedAt) {
		t.Fatalf("artifact timestamp %s predates operation release %s", stored.artifact.CreatedAt, releasedAt)
	}
}

func TestTransientRootLeaseProtectsEvaluatorInputs(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := store.Put(context.Background(), []byte("leased"), PutOptions{Kind: "test"})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.acquireTransientRoots(context.Background(), []Ref{artifact.Ref})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := store.Collect(context.Background(), GCOptions{}); err != nil {
		t.Fatal(err)
	} else if result.ArtifactsRemoved != 0 {
		t.Fatalf("GC removed transiently leased artifact: %#v", result)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if result, err := store.Collect(context.Background(), GCOptions{}); err != nil {
		t.Fatal(err)
	} else if result.ArtifactsRemoved != 1 {
		t.Fatalf("GC result after lease release = %#v", result)
	}
}

func TestTransientLeaseCleanupIsBoundedByTimeout(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.acquireTransientRoots(context.Background(), []Ref{
		"loom://0123456789abcdef0123456789abcdef",
	})
	if err != nil {
		t.Fatal(err)
	}
	holder, err := acquireOperationFileLock(context.Background(), filepath.Join(store.root, operationLockFileName))
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	err = lease.Close()
	elapsed := time.Since(started)
	if !errors.Is(err, context.DeadlineExceeded) {
		_ = holder.Close()
		t.Fatalf("lease close error = %v, want deadline exceeded", err)
	}
	if elapsed > time.Second {
		_ = holder.Close()
		t.Fatalf("lease cleanup exceeded bounded wait: %s", elapsed)
	}
	if err := holder.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCollectCleansMalformedExpiredAndAbandonedLeases(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	malformed := filepath.Join(store.leases, "malformed.json")
	expired := filepath.Join(store.leases, "expired.json")
	abandoned := filepath.Join(store.leases, ".lease-abandoned.tmp")
	if err := os.WriteFile(malformed, []byte(`{"version":`), 0o600); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(transientLeaseFile{
		Version: transientLeaseVersion, ExpiresAt: time.Now().UTC().Add(-time.Minute),
		Roots: []Ref{"loom://0123456789abcdef0123456789abcdef"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(expired, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abandoned, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Collect(context.Background(), GCOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{malformed, expired, abandoned} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stale lease %s remains: %v", filepath.Base(path), err)
		}
	}
}

func TestWorkspaceMutationWaitsAcrossRootScanAndSweep(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := store.Put(context.Background(), []byte("old"), PutOptions{Kind: "test"})
	if err != nil {
		t.Fatal(err)
	}
	providerStarted := make(chan struct{})
	allowProvider := make(chan struct{})
	collectDone := make(chan error, 1)
	go func() {
		_, err := store.CollectWithRootProvider(context.Background(), func(context.Context) ([]Ref, error) {
			close(providerStarted)
			<-allowProvider
			return []Ref{artifact.Ref}, nil
		}, GCOptions{})
		collectDone <- err
	}()
	<-providerStarted
	waitCtx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	err = WithWorkspaceOperation(waitCtx, root, func(context.Context) error { return nil })
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		close(allowProvider)
		t.Fatalf("workspace mutation during root scan = %v, want deadline exceeded", err)
	}
	close(allowProvider)
	if err := <-collectDone; err != nil {
		t.Fatal(err)
	}
	if _, err := store.Inspect(context.Background(), artifact.Ref); err != nil {
		t.Fatalf("rooted artifact after coordinated sweep: %v", err)
	}
}

func TestConcurrentStoreInstancesSerializePutReadAndCollect(t *testing.T) {
	root := t.TempDir()
	coordinator, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	keptContent := []byte("permanent root")
	kept, err := coordinator.Put(context.Background(), keptContent, PutOptions{Kind: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Put(context.Background(), []byte("collect this orphan"), PutOptions{Kind: "test"}); err != nil {
		t.Fatal(err)
	}
	protectConcurrentWrites := time.Now().UTC()

	const (
		writerCount    = 4
		putsPerWriter  = 8
		readerCount    = 2
		readsPerReader = 20
		collectCount   = 12
	)
	sharedContent := []byte("shared concurrent payload")
	stores := make([]*Store, writerCount+readerCount+1)
	for index := range stores {
		stores[index], err = Open(root)
		if err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	errorsFound := make(chan error, writerCount*putsPerWriter+readerCount*readsPerReader+collectCount)
	refs := make(chan Ref, writerCount*putsPerWriter)
	var group sync.WaitGroup
	for writer := 0; writer < writerCount; writer++ {
		store := stores[writer]
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			for range putsPerWriter {
				artifact, err := store.Put(context.Background(), sharedContent, PutOptions{Kind: "test"})
				if err != nil {
					errorsFound <- err
					continue
				}
				refs <- artifact.Ref
				content, err := store.ReadAll(context.Background(), artifact.Ref, 1<<20)
				if err != nil {
					errorsFound <- err
				} else if !bytes.Equal(content, sharedContent) {
					errorsFound <- errors.New("loom: concurrent read returned unexpected content")
				}
			}
		}()
	}
	for reader := 0; reader < readerCount; reader++ {
		store := stores[writerCount+reader]
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			for range readsPerReader {
				content, err := store.ReadAll(context.Background(), kept.Ref, 1<<20)
				if err != nil {
					errorsFound <- err
				} else if !bytes.Equal(content, keptContent) {
					errorsFound <- errors.New("loom: rooted read returned unexpected content")
				}
			}
		}()
	}
	collector := stores[len(stores)-1]
	group.Add(1)
	go func() {
		defer group.Done()
		<-start
		for range collectCount {
			if _, err := collector.Collect(context.Background(), GCOptions{
				Roots: []Ref{kept.Ref}, ProtectNewerThan: protectConcurrentWrites, TargetBytes: 1,
			}); err != nil {
				errorsFound <- err
			}
		}
	}()
	close(start)
	group.Wait()
	close(errorsFound)
	close(refs)
	for err := range errorsFound {
		t.Errorf("concurrent operation: %v", err)
	}
	if t.Failed() {
		return
	}

	allRefs := make([]Ref, 0, writerCount*putsPerWriter)
	for ref := range refs {
		allRefs = append(allRefs, ref)
	}
	if len(allRefs) != writerCount*putsPerWriter {
		t.Fatalf("stored refs = %d, want %d", len(allRefs), writerCount*putsPerWriter)
	}
	for _, ref := range allRefs {
		content, err := coordinator.ReadAll(context.Background(), ref, 1<<20)
		if err != nil {
			t.Fatalf("read stored artifact %s: %v", ref, err)
		}
		if !bytes.Equal(content, sharedContent) {
			t.Fatalf("artifact %s content = %q", ref, content)
		}
	}
	stats, err := coordinator.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Artifacts != 1+writerCount*putsPerWriter || stats.Blobs != 2 || stats.Bytes != int64(len(keptContent)+len(sharedContent)) {
		t.Fatalf("stats after concurrent operations = %#v", stats)
	}
}

func TestEvaluatorReadsJSONAndStoresDerivedArtifact(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.Put(context.Background(), []byte(`{"items":[{"kind":"a","value":2},{"kind":"b","value":5},{"kind":"a","value":3}]}`), PutOptions{
		Kind: "mcp-result", MediaType: "application/json",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := (InProcessEvaluator{}).Evaluate(context.Background(), store, EvalRequest{
		Inputs: map[string]Ref{"source": source.Ref},
		Code: `
const rows = loom.json(inputs.source, "/items");
const totals = {};
for (const row of rows) totals[row.kind] = (totals[row.kind] || 0) + row.value;
return {totals, processType: typeof process, requireType: typeof require};`,
	})
	if err != nil {
		t.Fatal(err)
	}
	var value struct {
		Totals      map[string]int `json:"totals"`
		ProcessType string         `json:"processType"`
		RequireType string         `json:"requireType"`
	}
	if err := json.Unmarshal(result.Value, &value); err != nil {
		t.Fatal(err)
	}
	if value.Totals["a"] != 5 || value.Totals["b"] != 5 || value.ProcessType != "undefined" || value.RequireType != "undefined" {
		t.Fatalf("script value = %#v", value)
	}
	if len(result.Artifact.Parents) != 1 || result.Artifact.Parents[0] != source.Ref || result.Artifact.Source["language"] != "javascript" {
		t.Fatalf("derived artifact = %#v", result.Artifact)
	}
	stored, err := store.ReadAll(context.Background(), result.Artifact.Ref, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != string(result.Value) {
		t.Fatalf("stored result = %s, returned = %s", stored, result.Value)
	}
}
func TestEvaluatorRejectsMultipleJSONValues(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.Put(context.Background(), []byte(`{} {}`), PutOptions{Kind: "test", MediaType: "application/json"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = (InProcessEvaluator{}).Evaluate(context.Background(), store, EvalRequest{
		Inputs: map[string]Ref{"source": source.Ref},
		Code:   `return loom.get(inputs.source);`,
	})
	if err == nil {
		t.Fatal("multiple JSON values were accepted")
	}
}

func TestEvaluatorInterruptsRunawayScript(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, err = (InProcessEvaluator{}).Evaluate(ctx, store, EvalRequest{Code: `for (;;) {}`})
	if err == nil {
		t.Fatal("runaway script was not interrupted")
	}
}

func TestWorkerRequestCarriesLimitsWithoutStaleAutoGCRoots(t *testing.T) {
	ctx := context.Background()
	extra := Ref("loom://0123456789abcdef0123456789abcdef")
	store, err := OpenWithOptions(t.TempDir(), StoreOptions{
		MaximumArtifactBytes: 32, MaximumStoreBytes: 64, AutoGC: true,
		GCTriggerRatio: .8, GCTargetRatio: .6, GCGracePeriod: time.Hour,
		Roots: func(context.Context) ([]Ref, error) { return []Ref{extra}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.Put(ctx, []byte(`{}`), PutOptions{Kind: "test", MediaType: "application/json"})
	if err != nil {
		t.Fatal(err)
	}
	request, err := prepareWorkerRequest(ctx, store, EvalRequest{
		Inputs: map[string]Ref{"source": source.Ref}, Code: `return loom.get(inputs.source);`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if request.Store.MaximumArtifactBytes != 32 || request.Store.MaximumStoreBytes != 64 || request.Store.AutoGC {
		t.Fatalf("worker store options = %#v", request.Store)
	}
	if len(request.Roots) != 0 {
		t.Fatalf("worker received non-atomic GC roots = %#v", request.Roots)
	}
}

func TestRunChildUsesConfiguredArtifactLimit(t *testing.T) {
	request := workerRequest{
		WorkspaceRoot: t.TempDir(),
		Eval:          EvalRequest{Code: `return "123456789";`},
		Store: workerStoreOptions{
			MaximumArtifactBytes: 8, MaximumStoreBytes: 64,
			GCTriggerRatio: .8, GCTargetRatio: .6, GCGracePeriod: time.Hour,
		},
	}
	var input bytes.Buffer
	if err := json.NewEncoder(&input).Encode(request); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := RunChild(context.Background(), &input, &output); err != nil {
		t.Fatal(err)
	}
	var response workerResponse
	if err := json.NewDecoder(&output).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(response.Error, "artifact exceeds 8 bytes") {
		t.Fatalf("worker response = %#v", response)
	}
}

func TestRunChildReturnsValueWithoutPersistingDerivedArtifact(t *testing.T) {
	root := t.TempDir()
	request := workerRequest{
		WorkspaceRoot: root,
		Eval:          EvalRequest{Code: `return {value: 7};`},
		Store: workerStoreOptions{
			MaximumArtifactBytes: 64, MaximumStoreBytes: 128,
			GCTriggerRatio: .8, GCTargetRatio: .6, GCGracePeriod: time.Hour,
		},
	}
	var input bytes.Buffer
	if err := json.NewEncoder(&input).Encode(request); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := RunChild(context.Background(), &input, &output); err != nil {
		t.Fatal(err)
	}
	var response workerResponse
	if err := json.NewDecoder(&output).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Error != "" || response.Result.Artifact.Ref != "" || string(response.Result.Value) != `{"value":7}` {
		t.Fatalf("worker response = %#v", response)
	}
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if stats, err := store.Stats(context.Background()); err != nil || stats.Artifacts != 0 {
		t.Fatalf("worker persisted derived artifact: %#v, %v", stats, err)
	}
}

func TestCollectPreservesRootsAndParentLineage(t *testing.T) {
	ctx := context.Background()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.Put(ctx, []byte(`{"source":true}`), PutOptions{Kind: "source", MediaType: "application/json"})
	if err != nil {
		t.Fatal(err)
	}
	derived, err := store.Put(ctx, []byte(`{"derived":true}`), PutOptions{
		Kind: "derived", MediaType: "application/json", Parents: []Ref{source.Ref},
	})
	if err != nil {
		t.Fatal(err)
	}
	unrooted, err := store.Put(ctx, []byte(`{"unused":true}`), PutOptions{Kind: "source", MediaType: "application/json"})
	if err != nil {
		t.Fatal(err)
	}

	result, err := store.Collect(ctx, GCOptions{Roots: []Ref{derived.Ref}})
	if err != nil {
		t.Fatal(err)
	}
	if result.ArtifactsRemoved != 1 || result.BlobsRemoved != 1 || result.BytesReclaimed != unrooted.Bytes {
		t.Fatalf("GC result = %#v", result)
	}
	for _, ref := range []Ref{source.Ref, derived.Ref} {
		if _, err := store.Inspect(ctx, ref); err != nil {
			t.Fatalf("preserved artifact %s: %v", ref, err)
		}
	}
	if _, err := store.Inspect(ctx, unrooted.Ref); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unrooted artifact error = %v", err)
	}
}

func TestCollectDryRunAndSharedBlob(t *testing.T) {
	ctx := context.Background()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	content := []byte(`{"same":true}`)
	kept, err := store.Put(ctx, content, PutOptions{Kind: "test", MediaType: "application/json"})
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := store.Put(ctx, content, PutOptions{Kind: "test", MediaType: "application/json"})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := store.Collect(ctx, GCOptions{Roots: []Ref{kept.Ref}, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if preview.ArtifactsRemoved != 1 || preview.BlobsRemoved != 0 || preview.BytesReclaimed != 0 {
		t.Fatalf("dry-run result = %#v", preview)
	}
	if _, err := store.Inspect(ctx, duplicate.Ref); err != nil {
		t.Fatalf("dry-run removed duplicate: %v", err)
	}
	result, err := store.Collect(ctx, GCOptions{Roots: []Ref{kept.Ref}})
	if err != nil {
		t.Fatal(err)
	}
	if result.ArtifactsRemoved != 1 || result.BlobsRemoved != 0 || result.After.Bytes != int64(len(content)) {
		t.Fatalf("GC result = %#v", result)
	}
}

func TestPutRunsConfiguredAutomaticGC(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	root := t.TempDir()
	var store *Store
	var inspectDuringRoots Ref
	store, err := OpenWithOptions(root, StoreOptions{
		MaximumArtifactBytes: 16, MaximumStoreBytes: 16, AutoGC: true,
		GCTriggerRatio: .75, GCTargetRatio: .25, GCGracePeriod: time.Nanosecond,
		Roots: func(ctx context.Context) ([]Ref, error) {
			if inspectDuringRoots != "" {
				if _, err := store.Inspect(ctx, inspectDuringRoots); err != nil {
					return nil, err
				}
			}
			return nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	old, err := store.Put(ctx, []byte("1234567890"), PutOptions{Kind: "test"})
	if err != nil {
		t.Fatal(err)
	}
	inspectDuringRoots = old.Ref
	metadataPath := filepath.Join(store.artifacts, old.Ref.ID()+".json")
	metadata, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	var artifact Artifact
	if err := json.Unmarshal(metadata, &artifact); err != nil {
		t.Fatal(err)
	}
	artifact.CreatedAt = time.Now().UTC().Add(-time.Hour)
	metadata, err = json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metadataPath, append(metadata, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := store.Put(ctx, []byte("abcdefghij"), PutOptions{Kind: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Inspect(ctx, old.Ref); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old artifact error = %v", err)
	}
	if _, err := store.Inspect(ctx, created.Ref); err != nil {
		t.Fatalf("new artifact error = %v", err)
	}
}
