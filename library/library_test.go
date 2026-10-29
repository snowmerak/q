package library

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	llmclient "github.com/snowmerak/q/client"
	"github.com/snowmerak/q/gatewayconfig"
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/worklock"
)

func TestConfigAcceptsOnlyLoopbackAddresses(t *testing.T) {
	for _, value := range []Config{
		{Version: ConfigVersion, Host: "0.0.0.0", Port: 17891},
		{Version: ConfigVersion, Host: "192.168.0.10", Port: 17891},
		{Version: ConfigVersion, Host: "127.0.0.1", ProbeHost: "192.168.0.10", Port: 17891},
	} {
		if err := value.Validate(); err == nil {
			t.Fatalf("non-loopback config was accepted: %#v", value)
		}
	}
	value := Config{Version: ConfigVersion, Host: "::1", Port: 17891}
	if err := value.Validate(); err != nil || value.ListenAddress() != "[::1]:17891" || value.Endpoint() != "http://[::1]:17891/v1" {
		t.Fatalf("IPv6 loopback config = %#v, err = %v", value, err)
	}
}

func TestConfigStoreRoundTrip(t *testing.T) {
	store := ConfigStore{Dir: t.TempDir()}
	want := Config{
		Version: ConfigVersion, Host: "127.0.0.1", Port: 19001,
		ProbeHost: "127.0.0.1", APIKeyEnv: "TEST_LIBRARY_KEY",
	}
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadOrDefault()
	if err != nil {
		t.Fatal(err)
	}
	want.APIKeyEnv = ""
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("config = %#v, want %#v", got, want)
	}
}

func TestConfigStoreLocalizesLegacyExternalAddresses(t *testing.T) {
	store := ConfigStore{Dir: t.TempDir()}
	body := []byte(`{"version":1,"host":"0.0.0.0","port":19001,"probe_host":"192.168.0.10"}`)
	if err := os.WriteFile(store.Path(), body, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadOrDefault()
	if err != nil {
		t.Fatal(err)
	}
	if got.Host != DefaultHost || got.ProbeHost != "" || got.Endpoint() != "http://127.0.0.1:19001/v1" {
		t.Fatalf("localized config = %#v", got)
	}
}

func TestEnsureElectsOneLeaderAndServesStatusWithoutAuthentication(t *testing.T) {
	dir := t.TempDir()
	value := testConfig(t)
	ctx := t.Context()

	first, err := EnsureWithOptions(ctx, testOptions(dir, value))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := first.Close(); err != nil {
			t.Error(err)
		}
	}()
	if !first.IsLeader() {
		t.Fatal("first runtime did not become leader")
	}
	health, err := first.Client().Health(ctx)
	if err != nil || !health.Compatible() || health.StoreID == "" || health.Generation == "" {
		t.Fatalf("health = %#v, err = %v", health, err)
	}
	status, err := first.Client().Status(ctx)
	if err != nil || status.Generation != health.Generation {
		t.Fatalf("status = %#v, err = %v", status, err)
	}
	if _, err := NewClient(value.Endpoint(), "ignored-invalid-key", time.Second).Status(ctx); err != nil {
		t.Fatalf("status required authentication: %v", err)
	}

	second, err := EnsureWithOptions(ctx, testOptions(dir, value))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := second.Close(); err != nil {
			t.Error(err)
		}
	}()
	if second.IsLeader() {
		t.Fatal("second runtime also became leader")
	}
	secondHealth, err := second.Client().Health(ctx)
	if err != nil || secondHealth.Generation != health.Generation {
		t.Fatalf("second health = %#v, err = %v", secondHealth, err)
	}
}

func TestLibraryStartupDoesNotCreateAuthenticationStateOrModifyGatewaySettings(t *testing.T) {
	dir := t.TempDir()
	gatewayStore := gatewayconfig.Store{Dir: dir}
	var gatewayMaster [32]byte
	gatewayMaster[0] = 17
	if _, err := gatewayStore.EnsureMasterKey(gatewayMaster); err != nil {
		t.Fatal(err)
	}
	gatewayKey, err := gatewayconfig.GenerateAPIKey(gatewayMaster, "gateway only", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	gatewaySettings, err := gatewayconfig.AddAPIKey(gatewayconfig.Default(), gatewayKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := gatewayStore.Save(gatewaySettings); err != nil {
		t.Fatal(err)
	}
	gatewayConfigBefore, err := os.ReadFile(gatewayStore.Path())
	if err != nil {
		t.Fatal(err)
	}
	gatewayMasterBefore, err := os.ReadFile(gatewayStore.MasterKeyPath())
	if err != nil {
		t.Fatal(err)
	}

	value := testConfig(t)
	runtime, err := EnsureWithOptions(context.Background(), testOptions(dir, value))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err := NewClient(value.Endpoint(), gatewayKey.Secret, time.Second).Status(context.Background()); err != nil {
		t.Fatalf("Library status required authentication: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "library.key")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Library created authentication state: %v", err)
	}
	gatewayConfigAfter, err := os.ReadFile(gatewayStore.Path())
	if err != nil {
		t.Fatal(err)
	}
	gatewayMasterAfter, err := os.ReadFile(gatewayStore.MasterKeyPath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gatewayConfigAfter, gatewayConfigBefore) || !bytes.Equal(gatewayMasterAfter, gatewayMasterBefore) {
		t.Fatal("starting the Library modified Gateway authentication files")
	}
}

func TestGlobalSkillAPIReconcilesOnlyOnExplicitReload(t *testing.T) {
	dir := t.TempDir()
	skillDirectory := filepath.Join(dir, "skills", "global-api-skill")
	if err := os.MkdirAll(skillDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	writeLibrarySkill := func(description string) {
		body := "---\nname: global-api-skill\ndescription: " + description + "\ntags: [library-api]\n---\n\n# Global API skill\n"
		if err := os.WriteFile(filepath.Join(skillDirectory, "SKILL.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeLibrarySkill("Initial global skill token.")
	value := testConfig(t)
	runtime, err := EnsureWithOptions(context.Background(), testOptions(dir, value))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	}()
	client := runtime.Client()
	publicClient := NewClient(value.Endpoint(), "", time.Second)

	initial, err := publicClient.SearchSkills(context.Background(), SkillSearchRequest{Query: "Initial global skill token"})
	if err != nil {
		t.Fatal(err)
	}
	if len(initial.Hits) != 1 || initial.Hits[0].Scope != "global" {
		t.Fatalf("initial global skill search = %#v", initial)
	}
	resource, err := publicClient.GetSkill(context.Background(), initial.Hits[0].ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if resource.Skill.Name != "global-api-skill" || resource.Path != "SKILL.md" || !bytes.Contains(resource.Content, []byte("# Global API skill")) {
		t.Fatalf("global skill resource = %#v", resource)
	}

	writeLibrarySkill("Updated explicit reload token.")
	beforeReload, err := client.SearchSkills(context.Background(), SkillSearchRequest{Query: "Updated explicit reload token"})
	if err != nil {
		t.Fatal(err)
	}
	if len(beforeReload.Hits) != 1 || beforeReload.Hits[0].Description != "Initial global skill token." {
		t.Fatalf("search implicitly reloaded global skills: %#v", beforeReload.Hits)
	}
	reloaded, err := client.ReloadSkills(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Active != 1 {
		t.Fatalf("reload response = %#v", reloaded)
	}
	afterReload, err := client.SearchSkills(context.Background(), SkillSearchRequest{Query: "Updated explicit reload token"})
	if err != nil {
		t.Fatal(err)
	}
	if len(afterReload.Hits) != 1 || afterReload.Hits[0].ID != initial.Hits[0].ID || afterReload.Hits[0].Description != "Updated explicit reload token." {
		t.Fatalf("reloaded global skill search = %#v", afterReload)
	}
}

func TestGlobalSkillSearchBackfillsAndReassignsEmbeddingModel(t *testing.T) {
	dir := t.TempDir()
	skillDirectory := filepath.Join(dir, "skills", "animal-care")
	if err := os.MkdirAll(skillDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: animal-care\ndescription: Care for a cat.\ntags: [pets]\n---\n\n# Animal care\n"
	if err := os.WriteFile(filepath.Join(skillDirectory, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	value := testConfig(t)
	runtime, err := EnsureWithOptions(context.Background(), testOptions(dir, value))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	}()
	client := runtime.Client()

	lexical, err := client.SearchSkills(context.Background(), SkillSearchRequest{Query: "feline"})
	if err != nil || len(lexical.Hits) != 0 {
		t.Fatalf("BM25-only search = %#v, %v", lexical, err)
	}
	embedder := &semanticSkillEmbedder{}
	if err := client.ConfigureEmbedding(embedder, "embed-v1", 3); err != nil {
		t.Fatal(err)
	}
	stats, err := client.SyncSkillEmbeddings(context.Background())
	if err != nil || stats.Embedded != 1 {
		t.Fatalf("initial skill embedding sync = %#v, %v", stats, err)
	}
	semantic, err := client.SearchSkills(context.Background(), SkillSearchRequest{Query: "feline"})
	if err != nil || len(semantic.Hits) != 1 || semantic.Hits[0].Title != "animal-care" {
		t.Fatalf("semantic skill search = %#v, %v", semantic, err)
	}
	record, err := runtime.leader.archive.Get(semantic.Hits[0].ID)
	if err != nil || record.Embedding != nil || len(record.VectorProjections) != 3 ||
		record.VectorProjections[0].ID != "name" || record.VectorProjections[1].ID != "description" ||
		record.VectorProjections[2].ID != "tag-0" || record.VectorProjections[1].Embedding.Model != "embed-v1" {
		t.Fatalf("initial skill record = %#v, %v", record, err)
	}
	// A saved single-vector skill from the previous format must be reindexed
	// even when the configured model and dimensions have not changed.
	record.Embedding = &sessionstore.Embedding{Model: "embed-v1", Dimensions: 3, Vector: []float32{1, 0, 0}}
	record.VectorProjections = nil
	if _, err := runtime.leader.archive.Save(record); err != nil {
		t.Fatal(err)
	}
	stats, err = client.SyncSkillEmbeddings(context.Background())
	if err != nil || stats.Embedded != 1 {
		t.Fatalf("legacy skill embedding migration = %#v, %v", stats, err)
	}
	record, err = runtime.leader.archive.Get(semantic.Hits[0].ID)
	if err != nil || record.Embedding != nil || len(record.VectorProjections) != 3 {
		t.Fatalf("migrated skill record = %#v, %v", record, err)
	}

	if err := client.ConfigureEmbedding(embedder, "embed-v2", 3); err != nil {
		t.Fatal(err)
	}
	stats, err = client.SyncSkillEmbeddings(context.Background())
	if err != nil || stats.Embedded != 1 {
		t.Fatalf("reassigned skill embedding sync = %#v, %v", stats, err)
	}
	record, err = runtime.leader.archive.Get(semantic.Hits[0].ID)
	if err != nil || record.Embedding != nil || len(record.VectorProjections) != 3 ||
		record.VectorProjections[1].Embedding.Model != "embed-v2" {
		t.Fatalf("reassigned skill record = %#v, %v", record, err)
	}

	if err := client.ConfigureEmbedding(nil, "", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := client.SyncSkillEmbeddings(context.Background()); err != nil {
		t.Fatal(err)
	}
	if runtime.leader.archive.VectorConfig().Enabled() {
		t.Fatalf("disabled vector config = %#v", runtime.leader.archive.VectorConfig())
	}
	lexical, err = client.SearchSkills(context.Background(), SkillSearchRequest{Query: "feline"})
	if err != nil || len(lexical.Hits) != 0 {
		t.Fatalf("search after disabling embeddings = %#v, %v", lexical, err)
	}
}

type semanticSkillEmbedder struct{}

func (*semanticSkillEmbedder) Embed(_ context.Context, request llmclient.EmbeddingRequest) (*llmclient.EmbeddingResponse, error) {
	inputs, ok := request.Input.([]string)
	if !ok {
		return nil, fmt.Errorf("embedding input = %T", request.Input)
	}
	response := &llmclient.EmbeddingResponse{Model: request.Model, Data: make([]llmclient.Embedding, len(inputs))}
	for index, input := range inputs {
		vector := []float64{0, 1, 0}
		if strings.Contains(input, "cat") || strings.Contains(input, "feline") {
			vector = []float64{1, 0, 0}
		}
		response.Data[index] = llmclient.Embedding{Index: index, Embedding: vector}
	}
	return response, nil
}

func TestEnsureConcurrentElectionAndTakeover(t *testing.T) {
	dir := t.TempDir()
	value := testConfig(t)
	ctx := t.Context()
	start := make(chan struct{})
	type result struct {
		runtime *Runtime
		err     error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			<-start
			runtime, err := EnsureWithOptions(ctx, testOptions(dir, value))
			results <- result{runtime: runtime, err: err}
		}()
	}
	close(start)
	var runtimes []*Runtime
	leaders := 0
	for range 2 {
		current := <-results
		if current.err != nil {
			t.Fatal(current.err)
		}
		runtimes = append(runtimes, current.runtime)
		if current.runtime.IsLeader() {
			leaders++
		}
	}
	if leaders != 1 {
		t.Fatalf("leaders = %d", leaders)
	}
	var leaderRuntime *Runtime
	var originalStoreID string
	for _, runtime := range runtimes {
		health, err := runtime.Client().Health(ctx)
		if err != nil {
			t.Fatal(err)
		}
		originalStoreID = health.StoreID
		if runtime.IsLeader() {
			leaderRuntime = runtime
		}
	}
	if err := leaderRuntime.Close(); err != nil {
		t.Fatal(err)
	}

	replacement, err := EnsureWithOptions(ctx, testOptions(dir, value))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := replacement.Close(); err != nil {
			t.Error(err)
		}
	}()
	if !replacement.IsLeader() {
		t.Fatal("replacement did not take over leadership")
	}
	replacementHealth, err := replacement.Client().Health(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if replacementHealth.StoreID != originalStoreID {
		t.Fatalf("store ID changed across takeover: %q != %q", replacementHealth.StoreID, originalStoreID)
	}
	for _, runtime := range runtimes {
		_ = runtime.Close()
	}
}

func TestEnsureTakesOverWhenStartingOwnerReleasesLock(t *testing.T) {
	dir := t.TempDir()
	value := testConfig(t)
	owner, err := worklock.AcquireFile(dir, LockFileName, "stalled library leader")
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	result := make(chan struct {
		runtime *Runtime
		err     error
	}, 1)
	go func() {
		runtime, ensureErr := EnsureWithOptions(ctx, testOptions(dir, value))
		result <- struct {
			runtime *Runtime
			err     error
		}{runtime: runtime, err: ensureErr}
	}()
	time.Sleep(100 * time.Millisecond)
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-result:
		if got.err != nil {
			t.Fatal(got.err)
		}
		defer func() {
			if err := got.runtime.Close(); err != nil {
				t.Error(err)
			}
		}()
		if !got.runtime.IsLeader() {
			t.Fatal("contender did not take over after the stalled owner released the lock")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("contender did not retry lock acquisition")
	}
}

func TestEnsureRejectsUnrelatedServiceOnConfiguredPort(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	defer func() {
		if err := server.Close(); err != nil {
			t.Error(err)
		}
		if err := <-serveDone; err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Error(err)
		}
	}()
	value := Config{
		Version: ConfigVersion, Host: "127.0.0.1",
		Port: listener.Addr().(*net.TCPAddr).Port,
	}.Effective()
	options := testOptions(t.TempDir(), value)
	options.StartupTimeout = 150 * time.Millisecond
	runtime, err := EnsureWithOptions(t.Context(), options)
	if runtime != nil {
		_ = runtime.Close()
		t.Fatal("runtime started on an occupied port")
	}
	if err == nil || !IsAddressInUse(errors.Unwrap(err)) {
		t.Fatalf("collision error = %v", err)
	}
}

func TestEnsureRetriesHealthAfterListenCollision(t *testing.T) {
	directory := t.TempDir()
	var healthCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/health" {
			http.NotFound(writer, request)
			return
		}
		if healthCalls.Add(1) <= 2 {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if err := json.NewEncoder(writer).Encode(Health{
			Service: ServiceName, ProtocolVersion: ProtocolVersion, Ready: true, ConfigDirectory: directory,
		}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	value := Config{Version: ConfigVersion, Host: "127.0.0.1", Port: server.Listener.Addr().(*net.TCPAddr).Port}
	options := testOptions(directory, value)
	runtime, err := EnsureWithOptions(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	}()
	if runtime.IsLeader() || healthCalls.Load() < 3 {
		t.Fatalf("runtime leader = %t, health calls = %d; want follower after listen collision", runtime.IsLeader(), healthCalls.Load())
	}
}

func TestEnsureCancellationDuringProbeDoesNotBindOccupiedPort(t *testing.T) {
	probeStarted := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		select {
		case probeStarted <- struct{}{}:
		default:
		}
		<-request.Context().Done()
	}))
	defer server.Close()
	value := Config{Version: ConfigVersion, Host: "127.0.0.1", Port: server.Listener.Addr().(*net.TCPAddr).Port}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	options := testOptions(t.TempDir(), value)
	result := make(chan error, 1)
	go func() {
		_, err := EnsureWithOptions(ctx, options)
		result <- err
	}()
	select {
	case <-probeStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("health probe did not start")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Ensure error = %v, want context cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ensure did not stop after cancellation")
	}
}

func TestRunHostsForegroundServerUntilCancelled(t *testing.T) {
	dir := t.TempDir()
	value := testConfig(t)
	if err := (ConfigStore{Dir: dir}).Save(value); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var output lockedBuffer
	done := make(chan error, 1)
	go func() { done <- Run(ctx, dir, &output) }()
	client := NewClient(value.Endpoint(), "", 100*time.Millisecond)
	if err := waitUntilReady(context.Background(), client, 5*time.Second); err != nil {
		cancel()
		t.Fatal(err)
	}
	if output.String() == "" {
		cancel()
		t.Fatal("foreground server did not report its endpoint")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("foreground server did not stop after cancellation")
	}
}

func TestRunFollowerTakesOverFailedLeader(t *testing.T) {
	dir := t.TempDir()
	value := testConfig(t)
	if err := (ConfigStore{Dir: dir}).Save(value); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	leader, err := EnsureWithOptions(ctx, testOptions(dir, value))
	if err != nil {
		t.Fatal(err)
	}
	first, err := leader.Client().Health(ctx)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- Run(ctx, dir, &lockedBuffer{}) }()
	time.Sleep(100 * time.Millisecond)
	if err := leader.Close(); err != nil {
		t.Fatal(err)
	}

	client := NewClient(value.Endpoint(), "", 100*time.Millisecond)
	deadline := time.Now().Add(5 * time.Second)
	for {
		health, healthErr := client.Health(context.Background())
		if healthErr == nil && health.Compatible() && health.Generation != first.Generation {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("follower did not take over the failed leader")
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("takeover runtime did not stop after cancellation")
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *lockedBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(value)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}

func testConfig(t *testing.T) Config {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return Config{Version: ConfigVersion, Host: "127.0.0.1", Port: port}
}

func testOptions(dir string, value Config) EnsureOptions {
	return EnsureOptions{
		Dir: dir, Config: value,
		ProbeTimeout: 300 * time.Millisecond, StartupTimeout: 3 * time.Second,
	}
}

func waitUntilReady(ctx context.Context, client *Client, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	delay := 20 * time.Millisecond
	for {
		health, err := client.Health(ctx)
		if err == nil {
			if !health.Compatible() {
				return incompatibleError(health)
			}
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("library: leader did not become ready within %s: %w", timeout, err)
		}
		jitter := time.Duration(rand.IntN(max(1, int(delay/3))))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay + jitter):
		}
		delay = min(delay*2, 500*time.Millisecond)
	}
}
