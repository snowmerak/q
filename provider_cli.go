package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func runGrokForSession(ctx context.Context, session *Session, prompt string) error {
	text, err := callGrokForSession(ctx, session, prompt, "")
	if err != nil {
		return err
	}
	printProviderText(text)
	return nil
}

func callGrokForSession(ctx context.Context, session *Session, prompt, schema string) (string, error) {
	id := session.ProviderSessionID("grok")
	newSession := id == ""
	if newSession {
		var err error
		id, err = newUUID()
		if err != nil {
			return "", err
		}
	}
	args := grokArgs(session.PWD, id, prompt, !newSession)
	streaming := schema == ""
	if schema != "" {
		args = replaceOutputFormat(args, "json")
		args = append([]string{"--json-schema", schema}, args...)
	}
	cmd := exec.CommandContext(ctx, "grok", args...)
	cmd.Dir = session.PWD
	var stdout strings.Builder
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, &stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("run Grok session %q: %w", id, err)
	}
	if newSession {
		session.SetProviderSessionID("grok", id)
		if err := SaveSession(session); err != nil {
			return "", fmt.Errorf("save Grok session mapping: %w", err)
		}
	}
	if streaming {
		text, err := parseGrokStreamingOutput(stdout.String())
		if err != nil {
			return "", fmt.Errorf("Grok session %q did not complete: %w", id, err)
		}
		return text, nil
	}
	return stdout.String(), nil
}

// grokArgs builds a one-shot headless invocation.
// -p/--single: run with a single prompt on stdin/out (non-interactive); agent may
// still use tools during that turn. Without -p, the TUI path can prompt for
// auto-approve and other interactive setup.
func grokArgs(cwd, id, prompt string, resume bool) []string {
	args := []string{
		"--cwd", cwd,
		"--output-format", "streaming-json",
		"--permission-mode", "bypassPermissions",
		"--always-approve",
	}
	if resume {
		args = append(args, "--resume", id)
	} else {
		args = append(args, "--session-id", id)
	}
	return append(args, "-p", prompt)
}

type grokStreamEvent struct {
	Type       string `json:"type"`
	Data       string `json:"data"`
	StopReason string `json:"stopReason"`
}

// parseGrokStreamingOutput rejects logical failures that Grok reports with a
// successful process exit code. In particular, Grok 0.2.93 can emit an end
// event with stopReason=Cancelled after its first tool request and exit 0.
func parseGrokStreamingOutput(output string) (string, error) {
	var text strings.Builder
	foundEnd := false
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event grokStreamEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return "", fmt.Errorf("decode streaming event: %w", err)
		}
		switch event.Type {
		case "text":
			text.WriteString(event.Data)
		case "end":
			foundEnd = true
			if strings.EqualFold(event.StopReason, "cancelled") || strings.EqualFold(event.StopReason, "canceled") {
				return "", fmt.Errorf("agent stopped with reason %q", event.StopReason)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read streaming events: %w", err)
	}
	if !foundEnd {
		return "", errors.New("stream ended without an end event")
	}
	return text.String(), nil
}

func runClaudeForSession(ctx context.Context, session *Session, prompt string) error {
	text, err := callClaudeForSession(ctx, session, prompt, "")
	if err != nil {
		return err
	}
	printProviderText(text)
	return nil
}

func callClaudeForSession(ctx context.Context, session *Session, prompt, schema string) (string, error) {
	id := session.ProviderSessionID("claude")
	newSession := id == ""
	if newSession {
		var err error
		id, err = newUUID()
		if err != nil {
			return "", err
		}
	}
	args := claudeArgs(id, prompt, !newSession)
	if schema != "" {
		args = replaceOutputFormat(args, "json")
		args = append([]string{"--json-schema", schema}, args...)
	}
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = session.PWD
	var stdout strings.Builder
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, &stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("run Claude Code session %q: %w", id, err)
	}
	if newSession {
		session.SetProviderSessionID("claude", id)
		if err := SaveSession(session); err != nil {
			return "", fmt.Errorf("save Claude Code session mapping: %w", err)
		}
	}
	return stdout.String(), nil
}

func replaceOutputFormat(args []string, value string) []string {
	out := append([]string(nil), args...)
	for i := 0; i+1 < len(out); i++ {
		if out[i] == "--output-format" {
			out[i+1] = value
			return out
		}
	}
	return append([]string{"--output-format", value}, out...)
}

func printProviderText(text string) {
	fmt.Print(text)
	if text != "" && !strings.HasSuffix(text, "\n") {
		fmt.Println()
	}
}

func claudeArgs(id, prompt string, resume bool) []string {
	args := []string{"--output-format", "text", "--permission-mode", "acceptEdits"}
	if resume {
		args = append(args, "--resume", id)
	} else {
		args = append(args, "--session-id", id)
	}
	return append(args, "--print", prompt)
}

func runAgyForSession(ctx context.Context, session *Session, prompt string) error {
	text, err := callAgyForSession(ctx, session, prompt)
	if err != nil {
		return err
	}
	printProviderText(text)
	return nil
}

func callAgyForSession(ctx context.Context, session *Session, prompt string) (string, error) {
	brainDir, err := agyBrainDir()
	if err != nil {
		return "", err
	}
	id := session.ProviderSessionID("agy")
	newSession := id == ""
	var before map[string]time.Time
	var previousText string
	if newSession {
		before, err = scanConversationDirs(brainDir)
		if err != nil {
			return "", err
		}
	} else {
		previousText, err = readAgyModelText(agyTranscriptPath(brainDir, id))
		if err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("read agy transcript before turn: %w", err)
		}
	}
	args := []string{"-p", prompt}
	if !newSession {
		args = append([]string{"--conversation=" + id}, args...)
	}
	cmd := exec.CommandContext(ctx, "agy", args...)
	cmd.Dir = session.PWD
	cmd.Stdin = os.Stdin
	cmd.Stdout = io.Discard
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("run agy: %w", err)
	}
	if newSession {
		after, err := scanConversationDirs(brainDir)
		if err != nil {
			return "", err
		}
		id = findNewConversationID(before, after)
		if id == "" {
			return "", errors.New("agy completed but its new conversation ID could not be determined")
		}
		session.SetProviderSessionID("agy", id)
		if err := SaveSession(session); err != nil {
			return "", fmt.Errorf("save agy conversation mapping: %w", err)
		}
	}
	currentText, err := readAgyModelText(agyTranscriptPath(brainDir, id))
	if err != nil {
		return "", err
	}
	text := truncateAgyTranscriptPrefix(previousText, currentText)
	return text, nil
}

func agyTranscriptPath(brainDir, id string) string {
	return filepath.Join(brainDir, id, ".system_generated", "logs", "transcript.jsonl")
}

func agyBrainDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".gemini", "antigravity-cli", "brain"), nil
}

func scanConversationDirs(dir string) (map[string]time.Time, error) {
	result := make(map[string]time.Time)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err == nil {
			result[entry.Name()] = info.ModTime()
		}
	}
	return result, nil
}

func findNewConversationID(before, after map[string]time.Time) string {
	var id string
	var newest time.Time
	for candidate, modified := range after {
		if _, existed := before[candidate]; existed {
			continue
		}
		if id == "" || modified.After(newest) {
			id, newest = candidate, modified
		}
	}
	return id
}

type agyTranscriptLine struct {
	Source  string `json:"source"`
	Content string `json:"content"`
}

func readAgyModelText(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var out strings.Builder
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var line agyTranscriptLine
		if json.Unmarshal(scanner.Bytes(), &line) == nil && line.Source == "MODEL" {
			out.WriteString(line.Content)
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return out.String(), nil
}

func truncateAgyTranscriptPrefix(previous, current string) string {
	return strings.TrimPrefix(current, previous)
}

func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	s := hex.EncodeToString(b[:])
	return s[0:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:32], nil
}
