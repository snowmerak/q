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
	id := session.ProviderSessionID("grok")
	newSession := id == ""
	if newSession {
		var err error
		id, err = newUUID()
		if err != nil {
			return err
		}
	}
	args := grokArgs(session.PWD, id, prompt, !newSession)
	cmd := exec.CommandContext(ctx, "grok", args...)
	cmd.Dir = session.PWD
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run Grok session %q: %w", id, err)
	}
	if newSession {
		session.SetProviderSessionID("grok", id)
		if err := SaveSession(session); err != nil {
			return fmt.Errorf("save Grok session mapping: %w", err)
		}
	}
	return nil
}

func grokArgs(cwd, id, prompt string, resume bool) []string {
	args := []string{"--cwd", cwd, "--output-format", "plain"}
	if resume {
		args = append(args, "--resume", id)
	} else {
		args = append(args, "--session-id", id)
	}
	return append(args, "--single", prompt)
}

func runClaudeForSession(ctx context.Context, session *Session, prompt string) error {
	id := session.ProviderSessionID("claude")
	newSession := id == ""
	if newSession {
		var err error
		id, err = newUUID()
		if err != nil {
			return err
		}
	}
	cmd := exec.CommandContext(ctx, "claude", claudeArgs(id, prompt, !newSession)...)
	cmd.Dir = session.PWD
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run Claude Code session %q: %w", id, err)
	}
	if newSession {
		session.SetProviderSessionID("claude", id)
		if err := SaveSession(session); err != nil {
			return fmt.Errorf("save Claude Code session mapping: %w", err)
		}
	}
	return nil
}

func claudeArgs(id, prompt string, resume bool) []string {
	args := []string{"--output-format", "text"}
	if resume {
		args = append(args, "--resume", id)
	} else {
		args = append(args, "--session-id", id)
	}
	return append(args, "--print", prompt)
}

func runAgyForSession(ctx context.Context, session *Session, prompt string) error {
	brainDir, err := agyBrainDir()
	if err != nil {
		return err
	}
	id := session.ProviderSessionID("agy")
	newSession := id == ""
	var before map[string]time.Time
	if newSession {
		before, err = scanConversationDirs(brainDir)
		if err != nil {
			return err
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
		return fmt.Errorf("run agy: %w", err)
	}
	if newSession {
		after, err := scanConversationDirs(brainDir)
		if err != nil {
			return err
		}
		id = findNewConversationID(before, after)
		if id == "" {
			return errors.New("agy completed but its new conversation ID could not be determined")
		}
		session.SetProviderSessionID("agy", id)
		if err := SaveSession(session); err != nil {
			return fmt.Errorf("save agy conversation mapping: %w", err)
		}
	}
	text, err := readAgyAssistantText(filepath.Join(brainDir, id, ".system_generated", "logs", "transcript.jsonl"))
	if err != nil {
		return err
	}
	fmt.Print(text)
	if text != "" && !strings.HasSuffix(text, "\n") {
		fmt.Println()
	}
	return nil
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

func readAgyAssistantText(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var lines []agyTranscriptLine
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var line agyTranscriptLine
		if json.Unmarshal(scanner.Bytes(), &line) == nil {
			lines = append(lines, line)
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	lastUser := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if lines[i].Source == "USER_EXPLICIT" {
			lastUser = i
			break
		}
	}
	var out strings.Builder
	for _, line := range lines[lastUser+1:] {
		if line.Source == "MODEL" {
			out.WriteString(line.Content)
		}
	}
	return out.String(), nil
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
