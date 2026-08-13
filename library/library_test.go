package library

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	llmclient "github.com/snowmerak/q/client"
	"github.com/snowmerak/q/gatewayconfig"
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/worklock"
)

func TestConfigSupportsWildcardBindAndLocalProbe(t *testing.T) {
	value := Config{Version: ConfigVersion, Host: "0.0.0.0", Port: 17891}.Effective()
	if err := value.Validate(); err != nil {
		t.Fatal(err)
	}
	if value.ListenAddress() != "0.0.0.0:17891" {
		t.Fatalf("listen address = %q", value.ListenAddress())
	}
	if value.Endpoint() != "http://127.0.0.1:17891/v1" {
		t.Fatalf("endpoint = %q", value.Endpoint())
	}
}

func TestEnsureServesWildcardBindThroughLocalProbe(t *testing.T) {
	dir := t.TempDir()
	value := testConfig(t)
	value.Host = "0.0.0.0"
	runtime, err := EnsureWithOptions(context.Background(), testOptions(dir, value))
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if !runtime.IsLeader() {
		t.Fatal("wildcard runtime did not become leader")
	}
	health, err := runtime.Client().Health(context.Background())
	if err != nil || !health.Compatible() {
		t.Fatalf("wildcard health = %#v, err = %v", health, err)
	}
}

func TestConfigStoreRoundTrip(t *testing.T) {
	store := ConfigStore{Dir: t.TempDir()}
	want := Config{
		Version: ConfigVersion, Host: "0.0.0.0", Port: 19001,
		ProbeHost: "127.0.0.1", APIKeyEnv: "TEST_LIBRARY_KEY",
	}
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadOrDefault()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("config = %#v, want %#v", got, want)
	}
}

func TestEnsureElectsOneLeaderAndAuthenticatesStatus(t *testing.T) {
	dir := t.TempDir()
	value := testConfig(t)
	value, secret := installTestAPIKey(t, dir, value)
	t.Setenv(DefaultAPIKeyEnv, secret)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	first, err := EnsureWithOptions(ctx, testOptions(dir, value))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if !first.IsLeader() {
		t.Fatal("first runtime did not become leader")
	}
	health, err := first.Client().Health(ctx)
	if err != nil || !health.Compatible() || health.StoreID == "" || health.Generation == "" {
		t.Fatalf("health = %#v, err = %v", health, err)
	}
	status, err := first.Client().Status(ctx)
	if err != nil || status.Generation != health.Generation {
		t.Fatalf("authenticated status = %#v, err = %v", status, err)
	}
	if _, err := NewClient(value.Endpoint(), "", time.Second).Status(ctx); err == nil {
		t.Fatal("status accepted a missing API key")
	}

	second, err := EnsureWithOptions(ctx, testOptions(dir, value))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if second.IsLeader() {
		t.Fatal("second runtime also became leader")
	}
	secondHealth, err := second.Client().Health(ctx)
	if err != nil || secondHealth.Generation != health.Generation {
		t.Fatalf("second health = %#v, err = %v", secondHealth, err)
	}
}

func TestLibraryAuthenticationDoesNotUseGatewaySettings(t *testing.T) {
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

	value, librarySecret := installTestAPIKey(t, dir, testConfig(t))
	t.Setenv(DefaultAPIKeyEnv, librarySecret)
	runtime, err := EnsureWithOptions(context.Background(), testOptions(dir, value))
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if _, err := runtime.Client().Status(context.Background()); err != nil {
		t.Fatalf("Library key was rejected: %v", err)
	}
	if _, err := NewClient(value.Endpoint(), gatewayKey.Secret, time.Second).Status(context.Background()); err == nil {
		t.Fatal("Gateway key was accepted by the Library")
	}

	libraryStore := ConfigStore{Dir: dir}
	if libraryStore.MasterKeyPath() == gatewayStore.MasterKeyPath() {
		t.Fatal("Library and Gateway master-key paths are shared")
	}
	if _, err := os.Stat(libraryStore.MasterKeyPath()); err != nil {
		t.Fatalf("Library master key was not created: %v", err)
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
	value, secret := installTestAPIKey(t, dir, testConfig(t))
	t.Setenv(DefaultAPIKeyEnv, secret)
	runtime, err := EnsureWithOptions(context.Background(), testOptions(dir, value))
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	client := runtime.Client()
	if _, err := NewClient(value.Endpoint(), "", time.Second).SearchSkills(context.Background(), SkillSearchRequest{Query: "Initial"}); err == nil {
		t.Fatal("global skill search accepted a missing Library API key")
	}

	initial, err := client.SearchSkills(context.Background(), SkillSearchRequest{Query: "Initial global skill token"})
	if err != nil {
		t.Fatal(err)
	}
	if len(initial.Hits) != 1 || initial.Hits[0].Scope != "global" {
		t.Fatalf("initial global skill search = %#v", initial)
	}
	resource, err := client.GetSkill(context.Background(), initial.Hits[0].ID, "")
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

func TestGlobalPropositionAPIReadsStoredRecordsWithRecency(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC)
	confidence := 0.9
	payload, err := json.Marshal(PropositionPayload{
		Queries: []string{"how should services emit logs"}, Confidence: &confidence,
		ExtractorModel: "test-model", ExtractorVersion: "v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	seedLibraryRecords(t, dir,
		sessionstore.Record{
			ID: "prop-old", Kind: sessionstore.KindProposition, Scope: "global",
			Content: "Services emit structured JSON logs.", SearchText: "how should services emit logs",
			CreatedAt: now.Add(-90 * 24 * time.Hour), UpdatedAt: now.Add(-90 * 24 * time.Hour),
			Refs: []string{"thread://old"}, Tags: []string{"logging"}, Payload: payload,
		},
		sessionstore.Record{
			ID: "prop-new", Kind: sessionstore.KindProposition, Scope: "global",
			Content: "Services emit structured JSON logs.", SearchText: "how should services emit logs",
			CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
			Refs: []string{"thread://new"}, Tags: []string{"logging"}, Payload: payload,
		},
		sessionstore.Record{
			ID: "not-a-proposition", Kind: sessionstore.KindSkill, Scope: "global",
			Content: "Services emit structured JSON logs.", SearchText: "how should services emit logs",
			CreatedAt: now, UpdatedAt: now,
		},
	)

	value, secret := installTestAPIKey(t, dir, testConfig(t))
	t.Setenv(DefaultAPIKeyEnv, secret)
	runtime, err := EnsureWithOptions(context.Background(), testOptions(dir, value))
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if _, err := NewClient(value.Endpoint(), "", time.Second).SearchPropositions(
		context.Background(), PropositionSearchRequest{Query: "how should services emit logs"},
	); err == nil {
		t.Fatal("proposition search accepted a missing Library API key")
	}

	result, err := runtime.Client().SearchPropositions(context.Background(), PropositionSearchRequest{
		Query: "how should services emit logs", Tags: []string{"logging"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 2 || len(result.Hits) != 2 || result.Hits[0].ID != "prop-new" || result.Hits[0].Score <= result.Hits[1].Score {
		t.Fatalf("proposition search = %#v", result)
	}
	if result.RecencyWeight != defaultPropositionRecencyWeight || result.RecencyHalfLifeHours != defaultPropositionRecencyHalfLifeHour {
		t.Fatalf("proposition recency = %#v", result)
	}
	proposition, err := runtime.Client().GetProposition(context.Background(), "prop-new")
	if err != nil {
		t.Fatal(err)
	}
	if proposition.Content != "Services emit structured JSON logs." || len(proposition.Payload.Queries) != 1 || proposition.Payload.Confidence == nil || *proposition.Payload.Confidence != confidence {
		t.Fatalf("proposition = %#v", proposition)
	}
	if _, err := runtime.Client().GetProposition(context.Background(), "not-a-proposition"); err == nil {
		t.Fatal("get proposition returned a record of another kind")
	}
}

func TestGlobalPropositionAPIRegistersIdempotently(t *testing.T) {
	dir := t.TempDir()
	value, secret := installTestAPIKey(t, dir, testConfig(t))
	t.Setenv(DefaultAPIKeyEnv, secret)
	runtime, err := EnsureWithOptions(context.Background(), testOptions(dir, value))
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	input := PropositionRegisterRequest{
		Content:    "The Thinker registers one proposition per tool call.",
		Queries:    []string{"how does thinker register propositions", "proposition tool granularity"},
		Confidence: 0.95, Tags: []string{"thinker"}, Refs: []string{"run:test"},
		ExtractorModel: "thinker-model", ExtractorVersion: "thinker-v1",
	}
	if _, err := NewClient(value.Endpoint(), "", time.Second).RegisterProposition(context.Background(), "job/0", input); err == nil {
		t.Fatal("proposition registration accepted a missing Library API key")
	}
	created, err := runtime.Client().RegisterProposition(context.Background(), "job/0", input)
	if err != nil || !created.Created || created.ID == "" {
		t.Fatalf("created = %#v, err = %v", created, err)
	}
	retried, err := runtime.Client().RegisterProposition(context.Background(), "job/0", input)
	if err != nil || retried.Created || retried.ID != created.ID {
		t.Fatalf("retried = %#v, err = %v", retried, err)
	}
	changed := input
	changed.Content = "Different content must conflict."
	if _, err := runtime.Client().RegisterProposition(context.Background(), "job/0", changed); err == nil || !strings.Contains(err.Error(), "HTTP 409") {
		t.Fatalf("idempotency conflict = %v", err)
	}
	sensitive := input
	sensitive.Content = "api_key=qk_example.abcdefghijklmnopqrstuvwxyz"
	if _, err := runtime.Client().RegisterProposition(context.Background(), "job/1", sensitive); err == nil || !strings.Contains(err.Error(), "sensitive data") {
		t.Fatalf("sensitive proposition = %v", err)
	}
	result, err := runtime.Client().SearchPropositions(context.Background(), PropositionSearchRequest{Query: "proposition tool granularity"})
	if err != nil || result.Total != 1 || result.Hits[0].ID != created.ID {
		t.Fatalf("registered search = %#v, err = %v", result, err)
	}
	got, err := runtime.Client().GetProposition(context.Background(), created.ID)
	if err != nil || got.Payload.ExtractorModel != "thinker-model" || len(got.Payload.Queries) != 2 {
		t.Fatalf("registered proposition = %#v, err = %v", got, err)
	}
}

func TestGlobalPropositionAPIPersistsSearchesAndDeletesVectorProjections(t *testing.T) {
	dir := t.TempDir()
	value, secret := installTestAPIKey(t, dir, testConfig(t))
	t.Setenv(DefaultAPIKeyEnv, secret)
	options := testOptions(dir, value)
	options.Vector = sessionstore.VectorConfig{Model: "embed-test", Dimensions: 3}
	runtime, err := EnsureWithOptions(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	embedder := &testPropositionEmbedder{}
	if err := runtime.Client().ConfigureEmbedding(embedder, "embed-test", 3); err != nil {
		t.Fatal(err)
	}

	firstInput := PropositionRegisterRequest{
		Content:    "The Library persists one vector projection per proposition text and retrieval query.",
		Queries:    []string{"how are proposition vectors stored", "semantic memory projection"},
		Confidence: 0.9, ExtractorModel: "thinker-model", ExtractorVersion: "thinker-v1",
	}
	first, err := runtime.Client().RegisterProposition(context.Background(), "vectors/0", firstInput)
	if err != nil {
		t.Fatal(err)
	}
	retried, err := runtime.Client().RegisterProposition(context.Background(), "vectors/0", firstInput)
	if err != nil || retried.Created || retried.ID != first.ID {
		t.Fatalf("vector registration retry = %#v, %v", retried, err)
	}
	second, err := runtime.Client().RegisterProposition(context.Background(), "vectors/1", PropositionRegisterRequest{
		Content: "A separate proposition.", Confidence: 0.8,
		ExtractorModel: "thinker-model", ExtractorVersion: "thinker-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtime.Client().SearchPropositions(context.Background(), PropositionSearchRequest{
		Query: "unseen semantic lookup", RecencyWeight: floatPointer(0), Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 2 || len(result.Hits) != 2 || result.Hits[0].ID != first.ID {
		t.Fatalf("vector proposition search = %#v", result)
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	runtime, err = EnsureWithOptions(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if err := runtime.Client().ConfigureEmbedding(embedder, "embed-test", 3); err != nil {
		t.Fatal(err)
	}
	persisted, err := runtime.Client().SearchPropositions(context.Background(), PropositionSearchRequest{
		Query: "unseen semantic lookup", RecencyWeight: floatPointer(0), Limit: 10,
	})
	if err != nil || persisted.Total != 2 || persisted.Hits[0].ID != first.ID {
		t.Fatalf("persisted vector proposition search = %#v, %v", persisted, err)
	}
	removed, err := runtime.Client().DeleteProposition(context.Background(), first.ID)
	if err != nil || !removed.Deleted {
		t.Fatalf("delete proposition = %#v, %v", removed, err)
	}
	removedAgain, err := runtime.Client().DeleteProposition(context.Background(), first.ID)
	if err != nil || removedAgain.Deleted {
		t.Fatalf("idempotent delete = %#v, %v", removedAgain, err)
	}
	after, err := runtime.Client().SearchPropositions(context.Background(), PropositionSearchRequest{
		Query: "unseen semantic lookup", RecencyWeight: floatPointer(0), Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if after.Total != 1 || len(after.Hits) != 1 || after.Hits[0].ID != second.ID {
		t.Fatalf("vector search after delete = %#v", after)
	}
	if len(embedder.inputs) != 6 || len(embedder.inputs[0]) != 3 {
		t.Fatalf("embedding batches = %#v", embedder.inputs)
	}
}

func floatPointer(value float64) *float64 { return &value }

type testPropositionEmbedder struct{ inputs [][]string }

func (e *testPropositionEmbedder) Embed(_ context.Context, request llmclient.EmbeddingRequest) (*llmclient.EmbeddingResponse, error) {
	inputs, ok := request.Input.([]string)
	if !ok {
		return nil, fmt.Errorf("embedding input = %T", request.Input)
	}
	e.inputs = append(e.inputs, append([]string(nil), inputs...))
	response := &llmclient.EmbeddingResponse{Model: request.Model, Data: make([]llmclient.Embedding, len(inputs))}
	for index, input := range inputs {
		vector := []float64{1, 0, 0}
		if strings.Contains(input, "separate") {
			vector = []float64{0, 0, 1}
		} else {
			vector[1] = float64(len(e.inputs)) / 1000
		}
		response.Data[index] = llmclient.Embedding{Index: index, Embedding: vector}
	}
	return response, nil
}

func TestEnsureConcurrentElectionAndTakeover(t *testing.T) {
	dir := t.TempDir()
	value := testConfig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
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
	defer replacement.Close()
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
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
		defer got.runtime.Close()
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
	go server.Serve(listener)
	defer server.Close()
	value := Config{
		Version: ConfigVersion, Host: "127.0.0.1",
		Port: listener.Addr().(*net.TCPAddr).Port,
	}.Effective()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	runtime, err := EnsureWithOptions(ctx, testOptions(t.TempDir(), value))
	if runtime != nil {
		_ = runtime.Close()
		t.Fatal("runtime started on an occupied port")
	}
	if err == nil || !IsAddressInUse(errors.Unwrap(err)) {
		t.Fatalf("collision error = %v", err)
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
	return Config{Version: ConfigVersion, Host: "127.0.0.1", Port: port, APIKeyEnv: DefaultAPIKeyEnv}
}

func testOptions(dir string, value Config) EnsureOptions {
	return EnsureOptions{
		Dir: dir, Config: value,
		ProbeTimeout: 300 * time.Millisecond, StartupTimeout: 3 * time.Second,
	}
}

func installTestAPIKey(t *testing.T, dir string, value Config) (Config, string) {
	t.Helper()
	updated, generated, err := (ConfigStore{Dir: dir}).CreateAPIKey(value, "library test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return updated, generated.Secret
}

func seedLibraryRecords(t *testing.T, dir string, records ...sessionstore.Record) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	lock, err := worklock.AcquireFile(dir, LockFileName, "seed Library test records")
	if err != nil {
		t.Fatal(err)
	}
	store, err := sessionstore.OpenWithOptions(dir, sessionstore.OpenOptions{
		WorkspaceLock: lock, Directory: "library",
	})
	if err != nil {
		_ = lock.Close()
		t.Fatal(err)
	}
	for _, record := range records {
		if _, err := store.Save(record); err != nil {
			_ = store.Close()
			_ = lock.Close()
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		_ = lock.Close()
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
}
