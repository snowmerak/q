package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func createTempFile(t *testing.T, content string) string {
	t.Helper()
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "test.txt")
	err := os.WriteFile(filePath, []byte(content), 0644)
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	return filePath
}

func TestReadFileTool(t *testing.T) {
	content := "line 1\nline 2\nline 3\nline 4\nline 5"
	filePath := createTempFile(t, content)

	// Read first 3 lines
	res, total, err := ReadFileTool(filePath, 1, 3, 10)
	if err != nil {
		t.Fatalf("Unexpected error reading file: %v", err)
	}
	if total != 5 {
		t.Errorf("Expected total lines to be 5, got %d", total)
	}

	lines := strings.Split(res, "\n")
	if len(lines) != 3 {
		t.Errorf("Expected 3 lines returned, got %d", len(lines))
	}

	// Verify format "lineNum#hash:line"
	for i, line := range lines {
		lineNum := i + 1
		expectedPrefix := fmt.Sprintf("%d#", lineNum)
		if !strings.HasPrefix(line, expectedPrefix) {
			t.Errorf("Expected line to start with '%s', got '%s'", expectedPrefix, line)
		}
	}
}

func TestEditFileTool(t *testing.T) {
	content := "line 1\nline 2\nline 3\nline 4\nline 5"
	filePath := createTempFile(t, content)

	// Get hash for line 2 and line 3
	hash2 := HashLine("line 2", 2)
	hash3 := HashLine("line 3", 3)

	posAnchor := fmt.Sprintf("2#%s", hash2)
	endAnchor := fmt.Sprintf("3#%s", hash3)

	// Replace lines 2-3 with "new line 2\nnew line 3"
	replacement := "new line 2\nnew line 3"
	diffPreview, err := EditFileTool(filePath, posAnchor, endAnchor, replacement)
	if err != nil {
		t.Fatalf("Unexpected error editing file: %v", err)
	}

	if !strings.HasPrefix(diffPreview, "Diff preview:") {
		t.Errorf("Expected diff preview starting with 'Diff preview:', got:\n%s", diffPreview)
	}
	if !strings.Contains(diffPreview, "- 2#") || !strings.Contains(diffPreview, "+ 2#") {
		t.Errorf("Expected diff preview to contain line operations, got:\n%s", diffPreview)
	}

	// Read back and verify
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("Failed to read file: %v", err)
	}

	expectedContent := "line 1\nnew line 2\nnew line 3\nline 4\nline 5"
	if string(data) != expectedContent {
		t.Errorf("Expected content:\n%q\nGot:\n%q", expectedContent, string(data))
	}

	// Verify stale hash detection
	// Modifying file so hashes mismatch
	err = os.WriteFile(filePath, []byte("line 1\nchanged 2\nchanged 3\nline 4\nline 5"), 0644)
	if err != nil {
		t.Fatalf("Failed to write to file: %v", err)
	}

	_, err = EditFileTool(filePath, posAnchor, endAnchor, replacement)
	if err == nil {
		t.Error("Expected stale context error, got nil")
	} else if !strings.Contains(err.Error(), "[E_STALE_CONTEXT]") {
		t.Errorf("Expected error to contain '[E_STALE_CONTEXT]', got: %v", err)
	}
}

func TestInsertFileTool(t *testing.T) {
	content := "line 1\nline 2\nline 3"
	filePath := createTempFile(t, content)

	hash2 := HashLine("line 2", 2)
	posAnchor := fmt.Sprintf("2#%s", hash2)

	err := InsertFileTool(filePath, posAnchor, "inserted line")
	if err != nil {
		t.Fatalf("Unexpected error inserting file: %v", err)
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("Failed to read file: %v", err)
	}

	expectedContent := "line 1\nline 2\ninserted line\nline 3"
	if string(data) != expectedContent {
		t.Errorf("Expected content:\n%q\nGot:\n%q", expectedContent, string(data))
	}

	// Insert at the beginning (pos = "0")
	err = InsertFileTool(filePath, "0", "new header")
	if err != nil {
		t.Fatalf("Unexpected error inserting at 0: %v", err)
	}

	data, err = os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("Failed to read file: %v", err)
	}

	expectedContentAtStart := "new header\nline 1\nline 2\ninserted line\nline 3"
	if string(data) != expectedContentAtStart {
		t.Errorf("Expected content:\n%q\nGot:\n%q", expectedContentAtStart, string(data))
	}
}

func TestEraseFileTool(t *testing.T) {
	content := "line 1\nline 2\nline 3\nline 4"
	filePath := createTempFile(t, content)

	hash2 := HashLine("line 2", 2)
	hash3 := HashLine("line 3", 3)

	posAnchor := fmt.Sprintf("2#%s", hash2)
	endAnchor := fmt.Sprintf("3#%s", hash3)

	err := EraseFileTool(filePath, posAnchor, endAnchor)
	if err != nil {
		t.Fatalf("Unexpected error erasing file: %v", err)
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("Failed to read file: %v", err)
	}

	expectedContent := "line 1\nline 4"
	if string(data) != expectedContent {
		t.Errorf("Expected content:\n%q\nGot:\n%q", expectedContent, string(data))
	}
}
