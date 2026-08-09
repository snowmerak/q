package commitagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/subagent"
)

const (
	toolGitOverview   = "git_overview"
	toolGitFileDiff   = "git_file_diff"
	toolGitHunk       = "git_hunk"
	toolRecentCommits = "recent_commits"
	toolAnalyzeFiles  = "analyze_files"
	toolProposeCommit = "propose_commit"
	toolSplitCommit   = "split_commit"
)

var (
	allowedCommitTypes = map[string]struct{}{
		"build": {}, "chore": {}, "ci": {}, "docs": {}, "feat": {}, "fix": {},
		"perf": {}, "refactor": {}, "revert": {}, "style": {}, "test": {},
	}
	scopePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]*$`)
)

type Proposal struct {
	Type    string   `json:"type"`
	Scope   string   `json:"scope,omitempty"`
	Summary string   `json:"summary"`
	Body    []string `json:"body,omitempty"`
	Files   []string `json:"files,omitempty"`
}

type proposalState struct {
	Single *Proposal
	Split  []Proposal
}

type commitToolRuntime struct {
	state       repositoryState
	client      agentClient
	spec        subagent.Spec
	maxParallel int
	logger      *progressLogger
	overviewed  bool
	proposal    proposalState
}

func commitTools() []client.Tool {
	strict := true
	stringArray := func() map[string]any {
		return map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	}
	proposalProperties := map[string]any{
		"type":    map[string]any{"type": "string", "enum": sortedCommitTypes()},
		"scope":   map[string]any{"type": "string"},
		"summary": map[string]any{"type": "string"},
		"body":    stringArray(),
	}
	return []client.Tool{
		functionTool(toolGitOverview, "Inspect staged files, stat, numstat, scope candidates, changelog targets, and repository instructions. This must be the first tool called.", map[string]any{
			"type": "object", "properties": map[string]any{}, "additionalProperties": false,
		}, &strict),
		functionTool(toolGitFileDiff, "Read the complete staged diff for one non-lock file.", map[string]any{
			"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}},
			"required": []string{"path"}, "additionalProperties": false,
		}, &strict),
		functionTool(toolGitHunk, "Read one indexed staged diff hunk for a non-lock file.", map[string]any{
			"type": "object", "properties": map[string]any{
				"path": map[string]any{"type": "string"}, "index": map[string]any{"type": "integer", "minimum": 0},
			}, "required": []string{"path", "index"}, "additionalProperties": false,
		}, &strict),
		functionTool(toolRecentCommits, "Read recent commit subjects and bodies to match this repository's style.", map[string]any{
			"type": "object", "properties": map[string]any{"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 20}},
			"required": []string{"limit"}, "additionalProperties": false,
		}, &strict),
		functionTool(toolAnalyzeFiles, "Fan out isolated file-analysis workers and return structured summaries, highlights, and risks.", map[string]any{
			"type": "object", "properties": map[string]any{"paths": stringArray()},
			"required": []string{"paths"}, "additionalProperties": false,
		}, &strict),
		functionTool(toolProposeCommit, "Submit one validated commit proposal. Use this instead of replying with a commit message.", map[string]any{
			"type": "object", "properties": proposalProperties,
			"required": []string{"type", "summary"}, "additionalProperties": false,
		}, &strict),
		functionTool(toolSplitCommit, "Submit two or more validated, file-disjoint commit proposals that cover every visible staged file.", map[string]any{
			"type": "object", "properties": map[string]any{
				"commits": map[string]any{"type": "array", "minItems": 2, "maxItems": 8, "items": map[string]any{
					"type": "object", "properties": mergeSchema(proposalProperties, map[string]any{"files": stringArray()}),
					"required": []string{"type", "summary", "files"}, "additionalProperties": false,
				}},
			}, "required": []string{"commits"}, "additionalProperties": false,
		}, &strict),
	}
}

func functionTool(name, description string, parameters map[string]any, strict *bool) client.Tool {
	return client.Tool{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{
		Name: name, Description: description, Parameters: parameters, Strict: strict,
	}}
}

func mergeSchema(left, right map[string]any) map[string]any {
	result := make(map[string]any, len(left)+len(right))
	for key, value := range left {
		result[key] = value
	}
	for key, value := range right {
		result[key] = value
	}
	return result
}

func sortedCommitTypes() []string {
	result := make([]string, 0, len(allowedCommitTypes))
	for value := range allowedCommitTypes {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func (runtime *commitToolRuntime) call(ctx context.Context, call client.ToolCall) client.ToolResult {
	if call.Function.Name != toolGitOverview && !runtime.overviewed {
		return toolError(errors.New("git_overview must be called before every other commit tool"))
	}
	var output any
	var err error
	switch call.Function.Name {
	case toolGitOverview:
		output, err = runtime.gitOverview(call.Function.Arguments)
	case toolGitFileDiff:
		output, err = runtime.gitFileDiff(call.Function.Arguments)
	case toolGitHunk:
		output, err = runtime.gitHunk(call.Function.Arguments)
	case toolRecentCommits:
		output, err = runtime.recentCommits(ctx, call.Function.Arguments)
	case toolAnalyzeFiles:
		output, err = runtime.analyzeFiles(ctx, call.Function.Arguments)
	case toolProposeCommit:
		output, err = runtime.proposeCommit(call.Function.Arguments)
	case toolSplitCommit:
		output, err = runtime.splitCommit(call.Function.Arguments)
	default:
		err = fmt.Errorf("unsupported commit tool %q", call.Function.Name)
	}
	if err != nil {
		return toolError(err)
	}
	body, err := json.Marshal(output)
	if err != nil {
		return toolError(err)
	}
	return client.ToolResult{Content: string(body)}
}

func (runtime *commitToolRuntime) gitOverview(arguments string) (any, error) {
	if err := decodeArguments(arguments, &struct{}{}); err != nil {
		return nil, err
	}
	runtime.overviewed = true
	return struct {
		Files            []string      `json:"staged_files"`
		Stat             string        `json:"stat"`
		Numstat          string        `json:"numstat"`
		Scopes           []string      `json:"scope_candidates"`
		ChangelogTargets []string      `json:"changelog_targets,omitempty"`
		ContextFiles     []contextFile `json:"context_files,omitempty"`
		AutoStaged       bool          `json:"auto_staged"`
	}{runtime.state.visibleFiles, runtime.state.stat, runtime.state.numstat, runtime.state.scopeCandidates,
		runtime.state.changelogTargets, runtime.state.contextFiles, runtime.state.autoStaged}, nil
}

func (runtime *commitToolRuntime) gitFileDiff(arguments string) (any, error) {
	var input struct {
		Path string `json:"path"`
	}
	if err := decodeArguments(arguments, &input); err != nil {
		return nil, err
	}
	path, diff, err := runtime.visibleDiff(input.Path)
	if err != nil {
		return nil, err
	}
	return map[string]any{"path": path, "diff": diff}, nil
}

func (runtime *commitToolRuntime) gitHunk(arguments string) (any, error) {
	var input struct {
		Path  string `json:"path"`
		Index int    `json:"index"`
	}
	if err := decodeArguments(arguments, &input); err != nil {
		return nil, err
	}
	path, diff, err := runtime.visibleDiff(input.Path)
	if err != nil {
		return nil, err
	}
	hunks := splitHunks(diff)
	if input.Index < 0 || input.Index >= len(hunks) {
		return nil, fmt.Errorf("hunk index %d is out of range for %s (%d hunks)", input.Index, path, len(hunks))
	}
	return map[string]any{"path": path, "index": input.Index, "hunk": hunks[input.Index], "hunk_count": len(hunks)}, nil
}

func (runtime *commitToolRuntime) visibleDiff(requested string) (string, string, error) {
	path := filepathSlashClean(requested)
	for _, visible := range runtime.state.visibleFiles {
		if path == visible {
			return path, runtime.state.fileDiffs[path], nil
		}
	}
	return "", "", fmt.Errorf("%q is not a visible staged file", requested)
}

func splitHunks(diff string) []string {
	lines := strings.Split(diff, "\n")
	var header []string
	var hunks []string
	var current []string
	for _, line := range lines {
		if strings.HasPrefix(line, "@@") {
			if len(current) > 0 {
				hunks = append(hunks, strings.Join(current, "\n"))
			}
			current = append(append([]string(nil), header...), line)
			continue
		}
		if len(current) > 0 {
			current = append(current, line)
		} else if strings.HasPrefix(line, "diff --git") || strings.HasPrefix(line, "index ") || strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ") {
			header = append(header, line)
		}
	}
	if len(current) > 0 {
		hunks = append(hunks, strings.Join(current, "\n"))
	}
	if len(hunks) == 0 && strings.TrimSpace(diff) != "" {
		return []string{diff}
	}
	return hunks
}

func (runtime *commitToolRuntime) recentCommits(ctx context.Context, arguments string) (any, error) {
	var input struct {
		Limit int `json:"limit"`
	}
	if err := decodeArguments(arguments, &input); err != nil {
		return nil, err
	}
	if input.Limit < 1 || input.Limit > 20 {
		return nil, errors.New("limit must be between 1 and 20")
	}
	format := "%s%x00%b%x00"
	body, err := gitOutput(ctx, runtime.state.root, "log", "-n", strconv.Itoa(input.Limit), "--format="+format)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(body, "\x00")
	type recent struct {
		Subject string `json:"subject"`
		Body    string `json:"body,omitempty"`
	}
	commits := make([]recent, 0, input.Limit)
	for index := 0; index+1 < len(parts); index += 2 {
		subject := strings.TrimSpace(parts[index])
		if subject == "" {
			continue
		}
		commits = append(commits, recent{Subject: subject, Body: strings.TrimSpace(parts[index+1])})
	}
	return map[string]any{"commits": commits}, nil
}

type fileAnalysis struct {
	Path       string   `json:"path"`
	Summary    string   `json:"summary"`
	Highlights []string `json:"highlights,omitempty"`
	Risks      []string `json:"risks,omitempty"`
}

func (runtime *commitToolRuntime) analyzeFiles(ctx context.Context, arguments string) (any, error) {
	var input struct {
		Paths []string `json:"paths"`
	}
	if err := decodeArguments(arguments, &input); err != nil {
		return nil, err
	}
	if len(input.Paths) == 0 || len(input.Paths) > 16 {
		return nil, errors.New("paths must contain between 1 and 16 files")
	}
	runtime.logger.step("analyze", "fan-out requested for %d files", len(input.Paths))
	paths := make([]string, 0, len(input.Paths))
	seen := make(map[string]struct{}, len(input.Paths))
	for _, requested := range input.Paths {
		path, _, err := runtime.visibleDiff(requested)
		if err != nil {
			return nil, err
		}
		if _, found := seen[path]; !found {
			seen[path] = struct{}{}
			paths = append(paths, path)
		}
	}

	limit := runtime.maxParallel
	if limit < 1 {
		limit = 1
	}
	semaphore := make(chan struct{}, limit)
	results := make([]fileAnalysis, len(paths))
	errorsByPath := make([]string, len(paths))
	var group sync.WaitGroup
	for index, path := range paths {
		group.Add(1)
		go func() {
			defer group.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				errorsByPath[index] = ctx.Err().Error()
				return
			}
			analysis, err := runtime.analyzeFile(ctx, path)
			if err != nil {
				errorsByPath[index] = err.Error()
				return
			}
			results[index] = analysis
		}()
	}
	group.Wait()
	output := make([]fileAnalysis, 0, len(results))
	failures := make(map[string]string)
	for index, result := range results {
		if errorsByPath[index] != "" {
			failures[paths[index]] = errorsByPath[index]
		} else {
			output = append(output, result)
		}
	}
	return map[string]any{"analyses": output, "failures": failures}, nil
}

func (runtime *commitToolRuntime) analyzeFile(ctx context.Context, path string) (fileAnalysis, error) {
	diff := runtime.state.fileDiffs[path]
	request := client.ChatRequest{
		Messages: []client.Message{
			{Role: client.RoleSystem, Content: "Analyze one staged file diff for a commit author. Return only JSON with summary, highlights, and risks. Do not call tools."},
			{Role: client.RoleUser, Content: "Path: " + path + "\n\n" + diff},
		},
		ToolChoice: client.ToolChoiceNone,
		OutputSchema: map[string]any{
			"type": "object", "properties": map[string]any{
				"summary":    map[string]any{"type": "string"},
				"highlights": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"risks":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			}, "required": []string{"summary", "highlights", "risks"}, "additionalProperties": false,
		},
	}
	runtime.spec.Apply(&request)
	response, err := runtime.client.Chat(ctx, request)
	if err != nil {
		return fileAnalysis{}, err
	}
	content, err := assistantContent(response)
	if err != nil {
		return fileAnalysis{}, err
	}
	var result fileAnalysis
	if err := json.Unmarshal([]byte(stripJSONFence(content)), &result); err != nil {
		result.Summary = strings.TrimSpace(content)
	}
	result.Path = path
	if result.Summary == "" {
		return fileAnalysis{}, errors.New("file analyzer returned an empty summary")
	}
	return result, nil
}

func (runtime *commitToolRuntime) proposeCommit(arguments string) (any, error) {
	var proposal Proposal
	if err := decodeArguments(arguments, &proposal); err != nil {
		return nil, err
	}
	if err := validateProposal(&proposal, false); err != nil {
		return nil, err
	}
	runtime.proposal = proposalState{Single: &proposal}
	return map[string]any{"accepted": true, "message": formatCommitMessage(proposal)}, nil
}

func (runtime *commitToolRuntime) splitCommit(arguments string) (any, error) {
	var input struct {
		Commits []Proposal `json:"commits"`
	}
	if err := decodeArguments(arguments, &input); err != nil {
		return nil, err
	}
	if len(input.Commits) < 2 || len(input.Commits) > 8 {
		return nil, errors.New("split_commit requires between 2 and 8 commits")
	}
	visible := make(map[string]struct{}, len(runtime.state.visibleFiles))
	for _, path := range runtime.state.visibleFiles {
		visible[path] = struct{}{}
	}
	covered := make(map[string]struct{}, len(visible))
	for index := range input.Commits {
		proposal := &input.Commits[index]
		if err := validateProposal(proposal, true); err != nil {
			return nil, fmt.Errorf("commit %d: %w", index+1, err)
		}
		for fileIndex, requested := range proposal.Files {
			path := filepathSlashClean(requested)
			if _, found := visible[path]; !found {
				return nil, fmt.Errorf("commit %d references unknown or hidden file %q", index+1, requested)
			}
			if _, found := covered[path]; found {
				return nil, fmt.Errorf("file %q appears in more than one commit", path)
			}
			proposal.Files[fileIndex] = path
			covered[path] = struct{}{}
		}
	}
	if len(covered) != len(visible) {
		var missing []string
		for path := range visible {
			if _, found := covered[path]; !found {
				missing = append(missing, path)
			}
		}
		sort.Strings(missing)
		return nil, fmt.Errorf("split proposal does not cover staged files: %s", strings.Join(missing, ", "))
	}
	runtime.proposal = proposalState{Split: input.Commits}
	messages := make([]string, 0, len(input.Commits))
	for _, proposal := range input.Commits {
		messages = append(messages, formatCommitMessage(proposal))
	}
	return map[string]any{"accepted": true, "messages": messages}, nil
}

func validateProposal(proposal *Proposal, requireFiles bool) error {
	proposal.Type = strings.ToLower(strings.TrimSpace(proposal.Type))
	proposal.Scope = strings.ToLower(strings.TrimSpace(proposal.Scope))
	proposal.Summary = strings.TrimSpace(proposal.Summary)
	if _, allowed := allowedCommitTypes[proposal.Type]; !allowed {
		return fmt.Errorf("unsupported commit type %q", proposal.Type)
	}
	if proposal.Scope != "" && !scopePattern.MatchString(proposal.Scope) {
		return fmt.Errorf("invalid commit scope %q", proposal.Scope)
	}
	if proposal.Summary == "" || strings.ContainsAny(proposal.Summary, "\r\n") {
		return errors.New("summary must be one non-empty line")
	}
	if strings.HasSuffix(proposal.Summary, ".") {
		return errors.New("summary must not end with a period")
	}
	first, _ := utf8.DecodeRuneInString(proposal.Summary)
	if unicode.IsUpper(first) {
		return errors.New("summary must begin with lowercase text")
	}
	if len([]rune(formatSubject(*proposal))) > 72 {
		return errors.New("formatted commit subject must not exceed 72 characters")
	}
	for index, line := range proposal.Body {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "-"))
		if line == "" || strings.ContainsAny(line, "\r\n") {
			return fmt.Errorf("body item %d must be one non-empty line", index+1)
		}
		first, _ := utf8.DecodeRuneInString(line)
		if unicode.IsLetter(first) && !unicode.IsUpper(first) {
			return fmt.Errorf("body item %d must begin with uppercase text", index+1)
		}
		if !strings.HasSuffix(line, ".") {
			return fmt.Errorf("body item %d must end with a period", index+1)
		}
		proposal.Body[index] = line
	}
	if requireFiles && len(proposal.Files) == 0 {
		return errors.New("files must not be empty")
	}
	return nil
}

func formatSubject(proposal Proposal) string {
	prefix := proposal.Type
	if proposal.Scope != "" {
		prefix += "(" + proposal.Scope + ")"
	}
	return prefix + ": " + proposal.Summary
}

func formatCommitMessage(proposal Proposal) string {
	message := formatSubject(proposal)
	if len(proposal.Body) == 0 {
		return message
	}
	var body strings.Builder
	body.WriteString(message)
	body.WriteString("\n\n")
	for index, line := range proposal.Body {
		if index > 0 {
			body.WriteByte('\n')
		}
		body.WriteString("- ")
		body.WriteString(line)
	}
	return body.String()
}

func mechanicalFallback(state repositoryState) Proposal {
	typeName := "chore"
	allDocs, allTests := len(state.visibleFiles) > 0, len(state.visibleFiles) > 0
	for _, path := range state.visibleFiles {
		lower := strings.ToLower(path)
		allDocs = allDocs && (strings.HasSuffix(lower, ".md") || strings.HasPrefix(lower, "docs/"))
		allTests = allTests && (strings.Contains(lower, "_test.") || strings.Contains(lower, "/test") || strings.HasPrefix(lower, "test"))
	}
	if allDocs {
		typeName = "docs"
	} else if allTests {
		typeName = "test"
	}
	scope := ""
	if len(state.scopeCandidates) > 0 && state.scopeCandidates[0] != "repo" {
		scope = state.scopeCandidates[0]
	}
	return Proposal{Type: typeName, Scope: scope, Summary: "updated staged changes"}
}

func decodeArguments(arguments string, output any) error {
	decoder := json.NewDecoder(strings.NewReader(arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("decode arguments: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return errors.New("decode arguments: multiple JSON values")
	} else if !errors.Is(err, io.EOF) {
		return fmt.Errorf("decode arguments: %w", err)
	}
	return nil
}

func toolError(err error) client.ToolResult {
	return client.ToolResult{Content: "Tool error: " + err.Error(), IsError: true}
}

func filepathSlashClean(path string) string {
	return strings.TrimPrefix(strings.ReplaceAll(strings.TrimSpace(path), "\\", "/"), "./")
}

func stripJSONFence(value string) string {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "```") {
		return value
	}
	firstNewline := strings.IndexByte(value, '\n')
	lastFence := strings.LastIndex(value, "```")
	if firstNewline < 0 || lastFence <= firstNewline {
		return value
	}
	return strings.TrimSpace(value[firstNewline+1 : lastFence])
}
