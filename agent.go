package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type AgentTool struct {
	Name        string
	Description string
	Parameters  any
	Handler     func(argsJSON string) (string, error)
}

type Agent struct {
	Config     *Config
	SystemInfo *SystemInfo
	Session    *Session
	Tools      map[string]AgentTool
}

// NewAgent constructs a new Agent and registers its tools.
func NewAgent(cfg *Config, sysInfo *SystemInfo, session *Session) *Agent {
	a := &Agent{
		Config:     cfg,
		SystemInfo: sysInfo,
		Session:    session,
		Tools:      make(map[string]AgentTool),
	}
	a.registerBuiltinTools()
	a.registerMCPTools()
	return a
}

// GetToolsSpec returns OpenAI compatible tools format.
func (a *Agent) GetToolsSpec() []Tool {
	var tools []Tool
	for _, at := range a.Tools {
		tools = append(tools, Tool{
			Type: "function",
			Function: ToolFunction{
				Name:        at.Name,
				Description: at.Description,
				Parameters:  at.Parameters,
			},
		})
	}
	return tools
}

func (a *Agent) Run(ctx context.Context) {
	maxLoops := 10
	for i := 0; i < maxLoops; i++ {
		if err := CompressContext(ctx, a.Config, &a.Session.Messages, false); err != nil {
			PrintWarning(fmt.Sprintf("Context compression warning: %v", err))
		}

		PrintAgentThinking()
		msg, err := GenerateCommandMultiTurn(ctx, a.Config, a.Session.Messages, a.GetToolsSpec())
		ClearAgentThinking()
		if err != nil {
			PrintError(fmt.Sprintf("Error calling LLM: %v", err))
			return
		}

		a.Session.Messages = append(a.Session.Messages, *msg)

		if len(msg.ToolCalls) > 0 {
			for _, toolCall := range msg.ToolCalls {
				name := toolCall.Function.Name
				var output string
				var runErr error

				if tool, ok := a.Tools[name]; ok {
					output, runErr = tool.Handler(toolCall.Function.Arguments)
				} else {
					runErr = fmt.Errorf("unknown tool: %s", name)
				}

				var resJSON []byte
				if runErr != nil {
					resJSON, _ = json.Marshal(map[string]any{"error": runErr.Error(), "output": output})
				} else {
					resJSON = []byte(output)
				}

				a.Session.Messages = append(a.Session.Messages, ChatMessage{
					Role:       "tool",
					Name:       name,
					ToolCallID: toolCall.ID,
					Content:    string(resJSON),
				})
			}
			continue
		}

		if msg.Content != "" {
			fmt.Println(msg.Content)
		}
		break
	}
}

func (a *Agent) registerBuiltinTools() {
	// 1. run_shell_command
	runShellParams := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "The exact CLI shell command to run on the user's terminal.",
			},
		},
		"required": []string{"command"},
	}
	a.Tools["run_shell_command"] = AgentTool{
		Name:        "run_shell_command",
		Description: "Execute a CLI shell command on the user's local terminal and return the output.",
		Parameters:  runShellParams,
		Handler: func(argsJSON string) (string, error) {
			var args struct {
				Command string `json:"command"`
			}
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
				return "", err
			}

			fmt.Printf("\n⚙️  %s\n", Color("Executing: "+args.Command, ansiCyan))
			stdout, stderr, finalDir, runErr := executeCommand(a.SystemInfo.Shell, args.Command, a.SystemInfo.PWD)
			if finalDir != "" {
				a.SystemInfo.PWD = finalDir
				a.Session.PWD = finalDir
				_ = os.Chdir(finalDir)
				_ = SaveSession(a.Session)
			}

			var errStr string
			if runErr != nil {
				errStr = runErr.Error()
			}
			res := ToolResult{
				Stdout: string(stdout),
				Stderr: string(stderr),
				Error:  errStr,
			}
			resJSON, _ := json.Marshal(res)
			return string(resJSON), nil
		},
	}

	// 2. read_file
	readFileParams := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"file_path": map[string]any{
				"type":        "string",
				"description": "The path to the file to read.",
			},
			"start_line": map[string]any{
				"type":        "integer",
				"description": "The 1-based line number to start reading from (inclusive). Omit for line 1.",
			},
			"end_line": map[string]any{
				"type":        "integer",
				"description": "The 1-based line number to stop reading at (inclusive). Omit for end of file.",
			},
			"max_lines": map[string]any{
				"type":        "integer",
				"description": "Maximum number of lines to return. Defaults to 200.",
			},
		},
		"required": []string{"file_path"},
	}
	a.Tools["read_file"] = AgentTool{
		Name:        "read_file",
		Description: "Read a file's contents with optional line range. Returns lines prefixed with 'LINE#HASH:' anchors.",
		Parameters:  readFileParams,
		Handler: func(argsJSON string) (string, error) {
			var args struct {
				FilePath  string `json:"file_path"`
				StartLine int    `json:"start_line"`
				EndLine   int    `json:"end_line"`
				MaxLines  int    `json:"max_lines"`
			}
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
				return "", err
			}
			resolvedPath := resolvePath(a.SystemInfo.PWD, args.FilePath)
			maxLines := args.MaxLines
			if maxLines <= 0 {
				maxLines = 200
			}
			content, total, err := ReadFileTool(resolvedPath, args.StartLine, args.EndLine, maxLines)
			var res map[string]any
			if err != nil {
				res = map[string]any{"error": err.Error()}
			} else {
				res = map[string]any{
					"path":        resolvedPath,
					"total_lines": total,
					"content":     content,
				}
			}
			resJSON, _ := json.Marshal(res)
			return string(resJSON), nil
		},
	}

	// 3. edit_file
	editFileParams := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"file_path": map[string]any{
				"type":        "string",
				"description": "Path to the file to modify.",
			},
			"pos": map[string]any{
				"type":        "string",
				"description": "Anchor of the FIRST line to replace (e.g. \"10#VR\") from read_file output.",
			},
			"end": map[string]any{
				"type":        "string",
				"description": "Anchor of the LAST line to replace (inclusive). Omit or set equal to pos for single-line replacement.",
			},
			"data": map[string]any{
				"type":        "string",
				"description": "Replacement text. Plain content only — NEVER include LINE#HASH prefixes.",
			},
		},
		"required": []string{"file_path", "pos", "data"},
	}
	a.Tools["edit_file"] = AgentTool{
		Name:        "edit_file",
		Description: "Replace a range of lines in a file atomically. Validates both anchors before writing.",
		Parameters:  editFileParams,
		Handler: func(argsJSON string) (string, error) {
			var args struct {
				FilePath string `json:"file_path"`
				Pos      string `json:"pos"`
				End      string `json:"end"`
				Data     string `json:"data"`
			}
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
				return "", err
			}
			resolvedPath := resolvePath(a.SystemInfo.PWD, args.FilePath)
			diffPreview, err := EditFileTool(resolvedPath, args.Pos, args.End, args.Data)
			var res map[string]any
			if err != nil {
				res = map[string]any{"error": err.Error()}
			} else {
				res = map[string]any{
					"success":      true,
					"message":      fmt.Sprintf("Successfully edited file %s", args.FilePath),
					"diff_preview": diffPreview,
				}
			}
			resJSON, _ := json.Marshal(res)
			return string(resJSON), nil
		},
	}

	// 4. insert_file
	insertFileParams := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"file_path": map[string]any{
				"type":        "string",
				"description": "The path to the file to modify.",
			},
			"pos": map[string]any{
				"type":        "string",
				"description": "The anchor of the line AFTER which the new content is inserted (e.g. \"10#VR\") from read_file output. To insert at the very beginning of the file use \"0\".",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "The text to insert. Plain string — no anchor prefixes.",
			},
		},
		"required": []string{"file_path", "pos", "content"},
	}
	a.Tools["insert_file"] = AgentTool{
		Name:        "insert_file",
		Description: "Insert one or more new lines into a file AFTER the line indicated by pos.",
		Parameters:  insertFileParams,
		Handler: func(argsJSON string) (string, error) {
			var args struct {
				FilePath string `json:"file_path"`
				Pos      string `json:"pos"`
				Content  string `json:"content"`
			}
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
				return "", err
			}
			resolvedPath := resolvePath(a.SystemInfo.PWD, args.FilePath)
			err := InsertFileTool(resolvedPath, args.Pos, args.Content)
			var res map[string]any
			if err != nil {
				res = map[string]any{"error": err.Error()}
			} else {
				res = map[string]any{
					"success": true,
					"message": fmt.Sprintf("Successfully inserted content into %s", args.FilePath),
				}
			}
			resJSON, _ := json.Marshal(res)
			return string(resJSON), nil
		},
	}

	// 5. erase_file
	eraseFileParams := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"file_path": map[string]any{
				"type":        "string",
				"description": "The path to the file to modify.",
			},
			"pos": map[string]any{
				"type":        "string",
				"description": "The anchor of the first line to delete (e.g. \"10#VR\") from read_file output.",
			},
			"end": map[string]any{
				"type":        "string",
				"description": "The anchor of the last line to delete (inclusive) from read_file output.",
			},
		},
		"required": []string{"file_path", "pos", "end"},
	}
	a.Tools["erase_file"] = AgentTool{
		Name:        "erase_file",
		Description: "Delete a range of lines from a file (inclusive).",
		Parameters:  eraseFileParams,
		Handler: func(argsJSON string) (string, error) {
			var args struct {
				FilePath string `json:"file_path"`
				Pos      string `json:"pos"`
				End      string `json:"end"`
			}
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
				return "", err
			}
			resolvedPath := resolvePath(a.SystemInfo.PWD, args.FilePath)
			err := EraseFileTool(resolvedPath, args.Pos, args.End)
			var res map[string]any
			if err != nil {
				res = map[string]any{"error": err.Error()}
			} else {
				res = map[string]any{
					"success": true,
					"message": fmt.Sprintf("Successfully erased range in %s", args.FilePath),
				}
			}
			resJSON, _ := json.Marshal(res)
			return string(resJSON), nil
		},
	}

	// 6. search_skills
	searchSkillsParams := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "The search term or tag to find matching skills.",
			},
		},
		"required": []string{"query"},
	}
	a.Tools["search_skills"] = AgentTool{
		Name:        "search_skills",
		Description: "Search for available agent skills / SOP guidelines by keywords or tags.",
		Parameters:  searchSkillsParams,
		Handler: func(argsJSON string) (string, error) {
			var args struct {
				Query string `json:"query"`
			}
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
				return "", err
			}

			skills := SearchSkills(args.Query)
			resJSON, _ := json.Marshal(skills)
			return string(resJSON), nil
		},
	}

	// 7. get_skill
	getSkillParams := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "The exact name of the skill to retrieve (case-insensitive).",
			},
		},
		"required": []string{"name"},
	}
	a.Tools["get_skill"] = AgentTool{
		Name:        "get_skill",
		Description: "Retrieve the full prompt and guidelines of a specific skill by its name.",
		Parameters:  getSkillParams,
		Handler: func(argsJSON string) (string, error) {
			var args struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
				return "", err
			}

			nameLower := strings.ToLower(args.Name)
			skill, ok := GlobalLoadedSkills[nameLower]
			if !ok {
				return "", fmt.Errorf("skill '%s' not found", args.Name)
			}

			resJSON, _ := json.Marshal(skill)
			return string(resJSON), nil
		},
	}
}

func (a *Agent) registerMCPTools() {
	GlobalMCPManager.mu.RLock()
	defer GlobalMCPManager.mu.RUnlock()

	for serverName, client := range GlobalMCPManager.clients {
		mcpTools, err := client.ListTools()
		if err != nil {
			PrintError(fmt.Sprintf("Failed to list tools for MCP server '%s': %v", serverName, err))
			continue
		}

		for _, mt := range mcpTools {
			prefixedName := fmt.Sprintf("%s__%s", serverName, mt.Name)

			var params map[string]any
			if len(mt.InputSchema) > 0 {
				_ = json.Unmarshal(mt.InputSchema, &params)
			}

			sName := serverName
			tName := mt.Name

			a.Tools[prefixedName] = AgentTool{
				Name:        prefixedName,
				Description: fmt.Sprintf("[%s] %s", sName, mt.Description),
				Parameters:  params,
				Handler: func(argsJSON string) (string, error) {
					var args any
					if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
						return "", err
					}

					GlobalMCPManager.mu.RLock()
					c, ok := GlobalMCPManager.clients[sName]
					GlobalMCPManager.mu.RUnlock()

					if !ok {
						return "", fmt.Errorf("MCP server '%s' is not running", sName)
					}

					output, err := c.CallTool(tName, args)
					var res map[string]any
					if err != nil {
						res = map[string]any{"error": err.Error(), "output": output}
					} else {
						res = map[string]any{
							"success": true,
							"output":  output,
						}
					}
					resJSON, _ := json.Marshal(res)
					return string(resJSON), nil
				},
			}
		}
	}
}
