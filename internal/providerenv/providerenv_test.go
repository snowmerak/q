//go:build !windows

package providerenv

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

func reset(t *testing.T) {
	t.Helper()
	previous := Replace(nil)
	t.Cleanup(func() { Replace(previous) })
}

func TestLoadSelectsReferencedNamesFromCommand(t *testing.T) {
	reset(t)
	settings := Settings{Shell: "/bin/sh", Command: `printf '{"Q_TEST_KEY":"rotated","Q_OTHER":"unused"}'`}
	values, result, err := Load(context.Background(), settings, []string{"Q_TEST_KEY", "Q_MISSING", "", "Q_TEST_KEY"})
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 || values["Q_TEST_KEY"] != "rotated" {
		t.Fatalf("values = %v", values)
	}
	if result.Source != SourceCommand || !slices.Equal(result.Loaded, []string{"Q_TEST_KEY"}) || !slices.Equal(result.Missing, []string{"Q_MISSING"}) {
		t.Fatalf("result = %+v", result)
	}
}

func TestLoadErrorsDoNotExposeOutput(t *testing.T) {
	reset(t)
	for name, command := range map[string]string{
		"not json":   `printf 'secret-value'`,
		"not string": `printf '{"Q_TEST_KEY":["secret-value"]}'`,
		"exit":       `echo secret-value >&2; echo secret-value; exit 3`,
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := Load(context.Background(), Settings{Shell: "/bin/sh", Command: command}, []string{"Q_TEST_KEY"})
			if err == nil {
				t.Fatal("expected an error")
			}
			if strings.Contains(err.Error(), "secret-value") {
				t.Fatalf("error exposes output: %v", err)
			}
		})
	}
}

func TestLoadWithoutCommandIsNotConfigured(t *testing.T) {
	if _, _, err := Load(context.Background(), Settings{}, []string{"Q_TEST_KEY"}); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("err = %v", err)
	}
}

func TestOverlayLookupEnvironmentAndNotify(t *testing.T) {
	reset(t)
	t.Setenv("Q_TEST_KEY", "inherited")
	t.Setenv("Q_TEST_KEEP", "kept")
	if got := Getenv("Q_TEST_KEY"); got != "inherited" {
		t.Fatalf("before reload = %q", got)
	}
	changed := Changed()
	Replace(map[string]string{"Q_TEST_KEY": "rotated"})
	select {
	case <-changed:
		t.Fatal("Replace notified consumers")
	default:
	}
	if got := Getenv("Q_TEST_KEY"); got != "rotated" {
		t.Fatalf("after reload = %q", got)
	}
	environment := Environment([]string{"Q_TEST_KEY=inherited", "Q_TEST_KEEP=kept"})
	if !slices.Equal(environment, []string{"Q_TEST_KEEP=kept", "Q_TEST_KEY=rotated"}) {
		t.Fatalf("environment = %v", environment)
	}
	Notify()
	select {
	case <-changed:
	default:
		t.Fatal("Notify did not close the channel")
	}
}
