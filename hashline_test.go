package main

import (
	"testing"
)

func TestHashLine(t *testing.T) {
	content := "fmt.Println(\"Hello, World!\")"
	lineNum := 5

	hash1 := HashLine(content, lineNum)
	if len(hash1) != 2 {
		t.Errorf("Expected 2-char hash, got: %s", hash1)
	}

	// Identical content at different line number should yield different hash
	hash2 := HashLine(content, 6)
	if hash1 == hash2 {
		t.Errorf("Expected different hashes for different lines, got same: %s", hash1)
	}

	// Normalizing CRLF should work
	hash3 := HashLine(content+"\r", lineNum)
	if hash1 != hash3 {
		t.Errorf("Expected hashes to be identical after normalizing CRLF")
	}
}

func TestParseAnchor(t *testing.T) {
	anchorStr := "10#VR"
	anchor, err := ParseAnchor(anchorStr)
	if err != nil {
		t.Fatalf("Unexpected error parsing valid anchor: %v", err)
	}
	if anchor.Line != 10 {
		t.Errorf("Expected line 10, got %d", anchor.Line)
	}
	if anchor.Hash != "VR" {
		t.Errorf("Expected hash 'VR', got '%s'", anchor.Hash)
	}

	_, err = ParseAnchor("invalid")
	if err == nil {
		t.Error("Expected error parsing invalid anchor, got nil")
	}

	_, err = ParseAnchor("10#V")
	if err == nil {
		t.Error("Expected error parsing short hash, got nil")
	}
}
