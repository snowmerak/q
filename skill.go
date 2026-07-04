package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Skill struct {
	Name        string   `yaml:"name" json:"name"`
	Description string   `yaml:"description" json:"description"`
	Tags        []string `yaml:"tags" json:"tags"`
	Prompt      string   `yaml:"prompt" json:"prompt"`
	FilePath    string   `yaml:"-" json:"-"`
}

// GlobalLoadedSkills caches all loaded skills.
var GlobalLoadedSkills = make(map[string]Skill)

func LoadSkills() error {
	GlobalLoadedSkills = make(map[string]Skill)

	// 1. Load global skills: ~/.config/q/skills/
	homeDir, err := os.UserHomeDir()
	if err == nil {
		globalSkillsDir := filepath.Join(homeDir, ".config", "q", "skills")
		_ = loadSkillsFromDir(globalSkillsDir)
	}

	// 2. Load local skills: .skills/
	pwd, err := os.Getwd()
	if err == nil {
		localSkillsDir := filepath.Join(pwd, ".skills")
		_ = loadSkillsFromDir(localSkillsDir)
	}

	return nil
}

func loadSkillsFromDir(dirPath string) error {
	if _, err := os.Stat(dirPath); os.IsNotExist(err) {
		return nil
	}

	files, err := os.ReadDir(dirPath)
	if err != nil {
		return err
	}

	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(strings.ToLower(file.Name()), ".md") {
			continue
		}

		filePath := filepath.Join(dirPath, file.Name())
		skill, err := parseSkillFile(filePath)
		if err != nil {
			PrintWarning(fmt.Sprintf("Failed to parse skill file %s: %v", file.Name(), err))
			continue
		}

		if skill.Name == "" {
			skill.Name = strings.TrimSuffix(file.Name(), filepath.Ext(file.Name()))
		}
		// Convert name to lowercase for easy lookup/commands
		skill.Name = strings.ToLower(skill.Name)

		GlobalLoadedSkills[skill.Name] = skill
	}

	return nil
}

func parseSkillFile(filePath string) (Skill, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return Skill{}, err
	}

	content := string(data)
	if !strings.HasPrefix(content, "---") {
		name := strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
		return Skill{
			Name:        name,
			Description: "Custom skill loaded from file",
			Prompt:      content,
			FilePath:    filePath,
		}, nil
	}

	parts := strings.SplitN(content, "---", 3)
	if len(parts) < 3 {
		return Skill{}, fmt.Errorf("invalid markdown frontmatter format")
	}

	yamlBlock := parts[1]
	bodyBlock := parts[2]

	var skill Skill
	if err := yaml.Unmarshal([]byte(yamlBlock), &skill); err != nil {
		return Skill{}, err
	}

	if skill.Prompt == "" {
		skill.Prompt = strings.TrimSpace(bodyBlock)
	}

	skill.FilePath = filePath
	return skill, nil
}

func SearchSkills(query string) []Skill {
	var results []Skill
	qLower := strings.ToLower(query)

	for _, skill := range GlobalLoadedSkills {
		match := false
		if strings.Contains(strings.ToLower(skill.Name), qLower) ||
			strings.Contains(strings.ToLower(skill.Description), qLower) ||
			strings.Contains(strings.ToLower(skill.Prompt), qLower) {
			match = true
		} else {
			for _, tag := range skill.Tags {
				if strings.Contains(strings.ToLower(tag), qLower) {
					match = true
					break
				}
			}
		}

		if match {
			results = append(results, skill)
		}
	}

	return results
}
