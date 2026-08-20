package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLearningConfigRoundTripAndClear(t *testing.T) {
	store := Store{Root: t.TempDir()}
	missing, err := store.LoadLearningConfig()
	if err != nil || missing.Version != LearningConfigVersion || missing.Disabled {
		t.Fatalf("missing learning config = %#v, %v", missing, err)
	}

	if err := store.SaveLearningConfig(LearningConfig{Disabled: true}); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadLearningConfig()
	if err != nil || !got.Disabled || got.Version != LearningConfigVersion {
		t.Fatalf("loaded learning config = %#v, %v", got, err)
	}
	if err := store.ClearLearningConfig(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.LearningPath()); !os.IsNotExist(err) {
		t.Fatalf("learning config after clear: %v", err)
	}
}

func TestLoadLearningConfigRejectsInvalidJSON(t *testing.T) {
	store := Store{Root: t.TempDir()}
	if err := os.MkdirAll(store.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"unknown field":   `{"version":1,"disabled":true,"extra":true}`,
		"wrong version":   `{"version":2,"disabled":true}`,
		"multiple values": `{"version":1,"disabled":true} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(store.Dir(), LearningFileName), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.LoadLearningConfig(); err == nil {
				t.Fatal("LoadLearningConfig accepted invalid input")
			}
		})
	}
}
