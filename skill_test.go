package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSkills(t *testing.T) {
	// Create a temporary directory for local skills
	tmpDir, err := os.MkdirTemp("", "q_test_skills")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a mock skill file
	skillContent := `---
name: test-skill
description: A mock skill for testing load
tags: [mock, test]
---
# Test Skill Prompt Body
This is the mock skill prompt body.`

	localSkillsDir := filepath.Join(tmpDir, ".skills")
	if err := os.MkdirAll(localSkillsDir, 0755); err != nil {
		t.Fatalf("Failed to create .skills dir: %v", err)
	}

	skillFilePath := filepath.Join(localSkillsDir, "test-skill.md")
	if err := os.WriteFile(skillFilePath, []byte(skillContent), 0644); err != nil {
		t.Fatalf("Failed to write mock skill: %v", err)
	}

	// Change current directory to temp directory to let LoadSkills load local skills
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get current wd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldWd) }()

	// Execute LoadSkills
	if err := LoadSkills(); err != nil {
		t.Fatalf("LoadSkills failed: %v", err)
	}

	// Verify the skill was loaded
	skill, ok := GlobalLoadedSkills["test-skill"]
	if !ok {
		t.Fatalf("Expected 'test-skill' to be loaded")
	}

	if skill.Description != "A mock skill for testing load" {
		t.Errorf("Unexpected description: %s", skill.Description)
	}

	if skill.Prompt != "# Test Skill Prompt Body\nThis is the mock skill prompt body." {
		t.Errorf("Unexpected prompt content: %s", skill.Prompt)
	}

	if len(skill.Tags) != 2 || skill.Tags[0] != "mock" || skill.Tags[1] != "test" {
		t.Errorf("Unexpected tags: %v", skill.Tags)
	}

	// Test SearchSkills
	results := SearchSkills("mock")
	if len(results) != 1 || results[0].Name != "test-skill" {
		t.Errorf("Expected search to find test-skill, got: %v", results)
	}
}

func TestAgentSkillsTools(t *testing.T) {
	// Pre-populate global skills
	GlobalLoadedSkills["demo"] = Skill{
		Name:        "demo",
		Description: "Demo skill",
		Tags:        []string{"demo", "test"},
		Prompt:      "This is demo prompt",
	}

	cfg := &Config{
		APIEndpoint:       "http://localhost:8080/v1",
		Model:             "test-model",
		MaxContextTokens:  4096,
		APITimeoutSeconds: 5,
	}
	sysInfo := &SystemInfo{
		OS:       "windows",
		Shell:    "powershell",
		Username: "testuser",
		PWD:      ".",
	}
	session := &Session{
		Name: "test-session",
	}

	agent := NewAgent(cfg, sysInfo, session)

	// Test search_skills tool
	searchTool, ok := agent.Tools["search_skills"]
	if !ok {
		t.Fatalf("Expected search_skills tool to be registered")
	}

	searchOutput, err := searchTool.Handler(`{"query": "demo"}`)
	if err != nil {
		t.Fatalf("search_skills handler failed: %v", err)
	}

	var searchResults []Skill
	if err := json.Unmarshal([]byte(searchOutput), &searchResults); err != nil {
		t.Fatalf("Failed to parse search results JSON: %v", err)
	}

	if len(searchResults) != 1 || searchResults[0].Name != "demo" {
		t.Errorf("Unexpected search results: %v", searchResults)
	}

	// Test get_skill tool
	getTool, ok := agent.Tools["get_skill"]
	if !ok {
		t.Fatalf("Expected get_skill tool to be registered")
	}

	getOutput, err := getTool.Handler(`{"name": "demo"}`)
	if err != nil {
		t.Fatalf("get_skill handler failed: %v", err)
	}

	var skillResult Skill
	if err := json.Unmarshal([]byte(getOutput), &skillResult); err != nil {
		t.Fatalf("Failed to parse skill JSON: %v", err)
	}

	if skillResult.Prompt != "This is demo prompt" {
		t.Errorf("Unexpected skill prompt: %s", skillResult.Prompt)
	}
}
