package config

import (
	"testing"
	"time"
)

func TestRequestRetryConfiguration(t *testing.T) {
	value := Default()
	value.Provider.Model = "test"
	defaults := value.Provider.EffectiveRequestRetry()
	if defaults.MaxAttempts != 4 || defaults.InitialBackoff != 250*time.Millisecond || defaults.MaxBackoff != 2*time.Second {
		t.Fatalf("defaults = %+v", defaults)
	}
	value.Provider.RequestRetry = RequestRetryConfig{MaxAttempts: 1, InitialBackoff: time.Second, MaxBackoff: 3 * time.Second}
	store := Store{Dir: t.TempDir()}
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil || loaded.Provider.RequestRetry != value.Provider.RequestRetry {
		t.Fatalf("saved retry = %+v, %v", loaded.Provider.RequestRetry, err)
	}
	for _, invalid := range []RequestRetryConfig{{MaxAttempts: -1}, {MaxAttempts: 11}, {InitialBackoff: -time.Second}, {InitialBackoff: 3 * time.Second, MaxBackoff: time.Second}} {
		value.Provider.RequestRetry = invalid
		if err := value.Validate(); err == nil {
			t.Fatalf("accepted %+v", invalid)
		}
	}
}
