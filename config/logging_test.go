package config

import (
	"math"
	"testing"
)

func TestLoggingDefaultsAndRoundTrip(t *testing.T) {
	defaults := (LoggingConfig{}).Effective()
	if *defaults.Stdout || !*defaults.FileEnabled || defaults.MaxFileMiB != 25 || defaults.MaxTotalBytes != 1_000_000_000 || defaults.MaxAgeDays != 7 {
		t.Fatalf("defaults=%+v", defaults)
	}
	value := Default()
	value.Provider.Model = "test-model"
	value.Logging = defaults
	value.Logging.Level = "debug"
	off := false
	value.Logging.FileEnabled = &off
	store := Store{Dir: t.TempDir()}
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Logging.Stdout == nil || *loaded.Logging.Stdout || loaded.Logging.FileEnabled == nil || *loaded.Logging.FileEnabled || loaded.Logging.Level != "debug" {
		t.Fatalf("round trip=%+v", loaded.Logging)
	}
}

func TestLoggingRejectsInvalidLimits(t *testing.T) {
	for _, value := range []LoggingConfig{{Level: "unexpected"}, {MaxFileMiB: -1}, {MaxTotalBytes: 1}, {MaxAgeDays: -1}, {MaxFileMiB: math.MaxInt64}, {MaxAgeDays: math.MaxInt64}} {
		if err := value.Validate(); err == nil {
			t.Fatalf("accepted invalid logging: %+v", value)
		}
	}
}
