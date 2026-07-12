package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const maxWorkflowDownloadBytes = 1 << 20

type WorkflowDefinitionFile struct{ Name, Path, Scope string }

func globalWorkflowDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "q", "workflows")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	return dir, nil
}

func projectWorkflowDir() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(cwd, ".q", "workflows"), nil
}

func workflowFileName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || filepath.Base(name) != name || name == "." || name == ".." {
		return "", fmt.Errorf("invalid workflow name %q", name)
	}
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		return name + ".yaml", nil
	}
	if ext != ".yaml" && ext != ".yml" {
		return "", fmt.Errorf("workflow name must use .yaml or .yml")
	}
	return name, nil
}

func ResolveWorkflow(nameOrPath string) (string, error) {
	if strings.ContainsAny(nameOrPath, `/\`) || filepath.IsAbs(nameOrPath) {
		if _, err := os.Stat(nameOrPath); err != nil {
			return "", err
		}
		return filepath.Abs(nameOrPath)
	}
	fileName, err := workflowFileName(nameOrPath)
	if err != nil {
		return "", err
	}
	fileNames := []string{fileName}
	if filepath.Ext(nameOrPath) == "" {
		fileNames = []string{nameOrPath + ".yaml", nameOrPath + ".yml"}
	}
	project, err := projectWorkflowDir()
	if err != nil {
		return "", err
	}
	global, err := globalWorkflowDir()
	if err != nil {
		return "", err
	}
	for _, dir := range []string{project, global} {
		for _, candidate := range fileNames {
			path := filepath.Join(dir, candidate)
			if _, err := os.Stat(path); err == nil {
				return path, nil
			}
		}
	}
	return "", fmt.Errorf("workflow %q was not found in project or global workflow directories", nameOrPath)
}

func ListWorkflowDefinitions() ([]WorkflowDefinitionFile, error) {
	project, err := projectWorkflowDir()
	if err != nil {
		return nil, err
	}
	global, err := globalWorkflowDir()
	if err != nil {
		return nil, err
	}
	var out []WorkflowDefinitionFile
	for _, source := range []struct{ dir, scope string }{{project, "project"}, {global, "global"}} {
		entries, err := os.ReadDir(source.dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			ext := strings.ToLower(filepath.Ext(entry.Name()))
			if !entry.IsDir() && (ext == ".yaml" || ext == ".yml") {
				out = append(out, WorkflowDefinitionFile{strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())), filepath.Join(source.dir, entry.Name()), source.scope})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name == out[j].Name {
			return out[i].Scope > out[j].Scope
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func InitWorkflowDefinition(name string, global bool) (string, error) {
	fileName, err := workflowFileName(name)
	if err != nil {
		return "", err
	}
	dir, err := projectWorkflowDir()
	if global {
		dir, err = globalWorkflowDir()
	}
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, fileName)
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("workflow already exists: %s", path)
	}
	content := defaultWorkflowGraphYAML(strings.TrimSuffix(fileName, filepath.Ext(fileName)))
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return "", err
	}
	return path, nil
}

func DownloadWorkflowDefinition(name, rawURL string) (string, error) {
	fileName, err := workflowFileName(name)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", fmt.Errorf("workflow URL must be an absolute HTTPS URL")
	}
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" {
			return fmt.Errorf("refusing redirect to non-HTTPS URL")
		}
		if len(via) >= 5 {
			return fmt.Errorf("too many redirects")
		}
		return nil
	}}
	return downloadWorkflowDefinitionWithClient(fileName, rawURL, client)
}

func downloadWorkflowDefinitionWithClient(fileName, rawURL string, client *http.Client) (string, error) {
	resp, err := client.Get(rawURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxWorkflowDownloadBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxWorkflowDownloadBytes {
		return "", fmt.Errorf("workflow exceeds %d bytes", maxWorkflowDownloadBytes)
	}
	if _, err := parseWorkflow(data); err != nil {
		return "", fmt.Errorf("invalid downloaded workflow: %w", err)
	}
	dir, err := globalWorkflowDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, fileName)
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("workflow already exists: %s", path)
	}
	temp, err := os.CreateTemp(dir, ".workflow-*.tmp")
	if err != nil {
		return "", err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err := io.Copy(temp, bytes.NewReader(data)); err != nil {
		temp.Close()
		return "", err
	}
	if err := temp.Close(); err != nil {
		return "", err
	}
	if err := os.Link(tempPath, path); err != nil {
		return "", err
	}
	return path, nil
}

func RemoveWorkflowDefinition(name string, global bool) (string, error) {
	fileName, err := workflowFileName(name)
	if err != nil {
		return "", err
	}
	dir, err := projectWorkflowDir()
	if global {
		dir, err = globalWorkflowDir()
	}
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, fileName)
	if err := os.Remove(path); err != nil {
		return "", err
	}
	return path, nil
}

func defaultWorkflowGraphYAML(name string) string {
	return `version: 1
name: ` + name + `
entry: plan
limits:
  max_visits: 24
  max_visits_per_step:
    plan: 5
    work: 5
    verify: 5
defaults:
  repair_attempts: 2
steps:
  plan:
    type: provider
    provider: claude
    prompt: |
      PLAN for: {{ input }}
      Visit {{ visit }}. Prior work: {{ steps.work }}
      Prior verify: {{ steps.verify }}
      Structured plan only.
    output:
      kind: structured
      schema: plan
  work:
    type: provider
    provider: grok
    prompt: |
      WORK for: {{ input }}
      Plan: {{ steps.plan }}
      Implement plan actions. Summarize changes.
  verify:
    type: provider
    provider: claude
    prompt: |
      VERIFY for: {{ input }}
      Plan: {{ steps.plan }}
      Work: {{ steps.work }}
    output:
      kind: structured
      schema: verdict
edges:
  - { from: plan, to: end, when: "steps.plan.done == true" }
  - { from: plan, to: work, when: "steps.plan.done == false" }
  - { from: work, to: verify, when: always }
  - { from: verify, to: end, when: "steps.verify.pass == true" }
  - { from: verify, to: plan, when: "steps.verify.pass == false" }
sinks:
  end: { type: success }
`
}

func WorkflowDefinitionPath(name string, global bool) (string, error) {
	fileName, err := workflowFileName(name)
	if err != nil {
		return "", err
	}
	dir, err := projectWorkflowDir()
	if global {
		dir, err = globalWorkflowDir()
	}
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, fileName)
	if _, err := os.Stat(path); err != nil {
		return "", err
	}
	return path, nil
}
