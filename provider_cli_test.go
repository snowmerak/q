package main

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"
	"time"
)

func TestGrokArgs(t *testing.T) {
	wantNew := []string{"--cwd", "C:/repo", "--output-format", "plain", "--permission-mode", "acceptEdits", "--session-id", "id", "--single", "hello"}
	if got := grokArgs("C:/repo", "id", "hello", false); !reflect.DeepEqual(got, wantNew) {
		t.Fatalf("new args = %#v", got)
	}
	wantResume := []string{"--cwd", "C:/repo", "--output-format", "plain", "--permission-mode", "acceptEdits", "--resume", "id", "--single", "hello"}
	if got := grokArgs("C:/repo", "id", "hello", true); !reflect.DeepEqual(got, wantResume) {
		t.Fatalf("resume args = %#v", got)
	}
}

func TestClaudeArgs(t *testing.T) {
	wantNew := []string{"--output-format", "text", "--permission-mode", "acceptEdits", "--session-id", "id", "--print", "hello"}
	if got := claudeArgs("id", "hello", false); !reflect.DeepEqual(got, wantNew) {
		t.Fatalf("new args = %#v", got)
	}
	wantResume := []string{"--output-format", "text", "--permission-mode", "acceptEdits", "--resume", "id", "--print", "hello"}
	if got := claudeArgs("id", "hello", true); !reflect.DeepEqual(got, wantResume) {
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

func TestReadAgyModelTextAndTruncatePreviousPrefix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	before := "previous answer"
	initial := `{"source":"USER_EXPLICIT","content":"first"}` + "\n" +
		`{"source":"MODEL","content":"previous answer"}` + "\n" +
		`{"source":"TOOL","content":"ignored"}` + "\n"
	if err := os.WriteFile(path, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}
	gotBefore, err := readAgyModelText(path)
	if err != nil {
		t.Fatal(err)
	}
	if gotBefore != before {
		t.Fatalf("before = %q", gotBefore)
	}

	appended := initial + `{"source":"USER_EXPLICIT","content":"second"}` + "\n" +
		`{"source":"MODEL","content":"current answer"}` + "\n"
	if err := os.WriteFile(path, []byte(appended), 0600); err != nil {
		t.Fatal(err)
	}
	gotAfter, err := readAgyModelText(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := truncateAgyTranscriptPrefix(gotBefore, gotAfter); got != "current answer" {
		t.Fatalf("new response = %q", got)
	}
}

func TestTruncateAgyTranscriptPrefixKeepsCurrentOnMismatch(t *testing.T) {
	if got := truncateAgyTranscriptPrefix("old", "new"); got != "new" {
		t.Fatalf("got %q", got)
	}
}
