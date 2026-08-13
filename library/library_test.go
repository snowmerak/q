package library

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/snowmerak/q/gatewayconfig"
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
	if got != want {
		t.Fatalf("config = %#v, want %#v", got, want)
	}
}

func TestEnsureElectsOneLeaderAndAuthenticatesStatus(t *testing.T) {
	dir := t.TempDir()
	value := testConfig(t)
	secret := installTestAPIKey(t, dir)
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

func installTestAPIKey(t *testing.T, dir string) string {
	t.Helper()
	store := gatewayconfig.Store{Dir: dir}
	var master [32]byte
	master[0] = 42
	if _, err := store.EnsureMasterKey(master); err != nil {
		t.Fatal(err)
	}
	generated, err := gatewayconfig.GenerateAPIKey(master, "library test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	value, err := gatewayconfig.AddAPIKey(gatewayconfig.Default(), generated)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	return generated.Secret
}
