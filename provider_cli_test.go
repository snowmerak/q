package main

import (
	"reflect"
	"regexp"
	"testing"
	"time"
)

func TestGrokArgs(t *testing.T) {
	wantNew := []string{"--cwd", "C:/repo", "--output-format", "plain", "--session-id", "id", "--single", "hello"}
	if got := grokArgs("C:/repo", "id", "hello", false); !reflect.DeepEqual(got, wantNew) {
		t.Fatalf("new args = %#v", got)
	}
	wantResume := []string{"--cwd", "C:/repo", "--output-format", "plain", "--resume", "id", "--single", "hello"}
	if got := grokArgs("C:/repo", "id", "hello", true); !reflect.DeepEqual(got, wantResume) {
		t.Fatalf("resume args = %#v", got)
	}
}

func TestNewUUID(t *testing.T) {
	id, err := newUUID()
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(id) {
		t.Fatalf("invalid UUID %q", id)
	}
}

func TestFindNewConversationID(t *testing.T) {
	now := time.Now()
	before := map[string]time.Time{"old": now}
	after := map[string]time.Time{"old": now, "first": now.Add(time.Second), "latest": now.Add(2 * time.Second)}
	if got := findNewConversationID(before, after); got != "latest" {
		t.Fatalf("got %q", got)
	}
}
