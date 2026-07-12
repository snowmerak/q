package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/tabwriter"
)

func main() {
	cfg, err := LoadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading configuration: %v\n", err)
		os.Exit(1)
	}

	if len(os.Args) < 2 {
		sessionName := cfg.LastSession
		if sessionName == "" {
			sessionName = "default"
		}
		runInteractiveAgent(cfg, sessionName)
		return
	}

	cmd := strings.ToLower(os.Args[1])
	switch cmd {
	case "codex":
		if len(os.Args) < 3 {
			PrintError("Missing prompt. Usage: q codex <prompt>")
			return
		}
		if err := runCodexOnce(context.Background(), cfg, strings.Join(os.Args[2:], " ")); err != nil {
			PrintError(err.Error())
		}
		return
	case "grok", "agy":
		if len(os.Args) < 3 {
			PrintError(fmt.Sprintf("Missing prompt. Usage: q %s <prompt>", cmd))
			return
		}
		session, err := loadLastSession(cfg)
		if err != nil {
			PrintError(err.Error())
			return
		}
		prompt := strings.Join(os.Args[2:], " ")
		if cmd == "grok" {
			err = runGrokForSession(context.Background(), session, prompt)
		} else {
			err = runAgyForSession(context.Background(), session, prompt)
		}
		if err != nil {
			PrintError(err.Error())
		}
		return

	case "mcp":
		if len(os.Args) < 3 {
			printMcpUsage()
			return
		}

		mcpCmd := strings.ToLower(os.Args[2])
		switch mcpCmd {
		case "ls", "list":
			handleMcpList(cfg)
		case "add":
			handleMcpAdd(cfg)
		case "rm", "remove":
			handleMcpRm(cfg)
		default:
			printMcpUsage()
		}
		return

	case "ls":
		if err := ListSessions(); err != nil {
			PrintError(fmt.Sprintf("Failed to list sessions: %v", err))
		}
		return

	case "rm":
		if len(os.Args) < 3 {
			PrintError("Missing session name. Usage: q rm <session_name>")
			return
		}
		if err := DeleteSession(os.Args[2]); err != nil {
			PrintError(err.Error())
		}
		return

	case "mv":
		if len(os.Args) < 4 {
			PrintError("Missing arguments. Usage: q mv <old_name> <new_name>")
			return
		}
		if err := RenameSession(os.Args[2], os.Args[3]); err != nil {
			PrintError(err.Error())
		}
		return

	case "cp":
		if len(os.Args) < 4 {
			PrintError("Missing arguments. Usage: q cp <src_name> <dst_name>")
			return
		}
		if err := CopySession(os.Args[2], os.Args[3]); err != nil {
			PrintError(err.Error())
		}
		return

	case "config":
		if len(os.Args) < 3 {
			data, err := json.MarshalIndent(cfg, "", "  ")
			if err != nil {
				PrintError(fmt.Sprintf("Failed to marshal config: %v", err))
				return
			}
			fmt.Println(string(data))
			return
		}

		key := os.Args[2]
		if strings.ToLower(key) == "edit" && len(os.Args) < 5 {
			filePath, err := getConfigFilePath()
			if err != nil {
				PrintError(fmt.Sprintf("Failed to get config path: %v", err))
				return
			}
			PrintInfo(fmt.Sprintf("Opening config file at: %s", filePath))
			editorOverride := ""
			if len(os.Args) == 4 {
				editorOverride = os.Args[3]
			}
			if err := openEditor(filePath, editorOverride); err != nil {
				PrintError(fmt.Sprintf("Failed to open editor: %v", err))
			}
			return
		}

		if len(os.Args) < 4 {
			val, err := cfg.GetConfigValue(key)
			if err != nil {
				PrintError(err.Error())
				return
			}
			fmt.Println(val)
			return
		}

		val := os.Args[3]
		if err := cfg.SetConfigValue(key, val); err != nil {
			PrintError(err.Error())
			return
		}
		if err := SaveConfig(cfg); err != nil {
			PrintError(fmt.Sprintf("Failed to save config: %v", err))
			return
		}
		PrintSuccess(fmt.Sprintf("Config updated: %s = %s", key, val))
		return
	}

	sessionName := os.Args[1]
	runInteractiveAgent(cfg, sessionName)
}

func printUsage() {
	fmt.Println("Usage:")
	fmt.Println("  q                              : Enter last active session (or default)")
	fmt.Println("  q <session_name>               : Enter or create a specific session")
	fmt.Println("  q ls                           : List all saved sessions")
	fmt.Println("  q rm <session_name>            : Delete a session")
	fmt.Println("  q mv <old_name> <new_name>     : Rename a session")
	fmt.Println("  q cp <src_name> <dst_name>     : Copy a session")
	fmt.Println("  q config                       : Print all configurations in JSON")
	fmt.Println("  q config edit [editor]         : Open the configuration file in specified or text editor")
	fmt.Println("  q config <key>                 : Get a configuration value")
	fmt.Println("  q config <key> <value>         : Set a configuration value")
	fmt.Println("  q mcp ls                       : List all configured MCP servers and status")
	fmt.Println("  q mcp add <name> [-e K=V]... <cmd> [args...] : Add a new MCP server configuration")
	fmt.Println("  q mcp rm <name>                : Remove an MCP server configuration")
	fmt.Println("  q codex <prompt>               : Run one turn through Codex app-server")
	fmt.Println("  q grok <prompt>                : Run one turn in this q session's Grok session")
	fmt.Println("  q agy <prompt>                 : Run one turn in this q session's agy conversation")
	fmt.Println("Interactive Session Commands:")
	fmt.Println("  /skills                        : List all loaded skills")
	fmt.Println("  /codex <prompt>                : Run Codex in this q session's native thread")
	fmt.Println("  /grok <prompt>                 : Run Grok in this q session's native session")
	fmt.Println("  /agy <prompt>                  : Run agy in this q session's native conversation")
	fmt.Println("  /<skill_name> [args]           : Run a specific skill workflow")
}

func runCodexOnce(ctx context.Context, cfg *Config, prompt string) error {
	session, err := loadLastSession(cfg)
	if err != nil {
		return err
	}
	return runCodexForSession(ctx, session, prompt)
}

func loadLastSession(cfg *Config) (*Session, error) {
	sessionName := cfg.LastSession
	if sessionName == "" {
		sessionName = "default"
	}
	session, err := LoadSession(sessionName)
	if err != nil {
		return nil, fmt.Errorf("load q session %q: %w", sessionName, err)
	}
	return session, nil
}

func runCodexForSession(ctx context.Context, session *Session, prompt string) error {
	server, err := StartCodexAppServer(ctx, "", os.Stderr)
	if err != nil {
		return err
	}
	defer server.Close()
	if err := server.Initialize(ctx); err != nil {
		return err
	}
	cwd := session.PWD
	if cwd == "" {
		cwd, err = os.Getwd()
		if err != nil {
			return err
		}
	}

	threadID := session.ProviderSessionID("codex")
	if threadID == "" {
		threadID, err = server.StartThread(ctx, cwd)
		if err != nil {
			return err
		}
		session.SetProviderSessionID("codex", threadID)
		if err := SaveSession(session); err != nil {
			return fmt.Errorf("save Codex thread mapping: %w", err)
		}
	} else if err := server.ResumeThread(ctx, threadID, cwd); err != nil {
		return fmt.Errorf("resume Codex thread %q for q session %q: %w", threadID, session.Name, err)
	}
	turnID, err := server.StartTurn(ctx, threadID, prompt)
	if err != nil {
		return err
	}

	for event := range server.Events() {
		switch event.Method {
		case "item/agentMessage/delta":
			var p struct {
				Delta  string `json:"delta"`
				TurnID string `json:"turnId"`
			}
			if json.Unmarshal(event.Params, &p) == nil && p.TurnID == turnID {
				fmt.Print(p.Delta)
			}
		case "turn/completed":
			var p struct {
				Turn struct {
					ID     string          `json:"id"`
					Status string          `json:"status"`
					Error  json.RawMessage `json:"error"`
				} `json:"turn"`
			}
			if json.Unmarshal(event.Params, &p) == nil && p.Turn.ID == turnID {
				fmt.Println()
				if p.Turn.Status == "failed" {
					return fmt.Errorf("codex turn failed: %s", compactJSON(p.Turn.Error))
				}
				if p.Turn.Status == "interrupted" {
					return errors.New("codex turn was interrupted")
				}
				return nil
			}
		}
	}
	return errors.New("codex app-server stopped before the turn completed")
}

func compactJSON(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return "unknown error"
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return string(raw)
	}
	data, err := json.Marshal(value)
	if err != nil {
		return string(raw)
	}
	return string(data)
}

func runInteractiveAgent(cfg *Config, sessionName string) {
	InitMCPManager(cfg)
	defer StopMCPManager()
	_ = LoadSkills()

	cfg.LastSession = sessionName
	_ = SaveConfig(cfg)

	execWD, err := os.Getwd()
	if err != nil {
		execWD = "."
	}

	session, err := LoadSession(sessionName)
	if err != nil {
		PrintError(fmt.Sprintf("Failed to load session '%s': %v", sessionName, err))
		os.Exit(1)
	}

	if len(session.Messages) == 0 {
		session.PWD = execWD
	}

	if session.PWD != "" {
		if _, err := os.Stat(session.PWD); err == nil {
			_ = os.Chdir(session.PWD)
		}
	}

	sysInfo := GetSystemInfo()
	if wd, err := os.Getwd(); err == nil {
		sysInfo.PWD = wd
		session.PWD = wd
	}
	ctx := context.Background()

	PrintInfo(fmt.Sprintf("Entering session '%s'...", sessionName))
	PrintInfo("Type 'exit' or 'quit' to close.")

	if len(session.Messages) == 0 {
		initialMsgs := CreateInitialMessages(sysInfo, "Start session")
		session.Messages = initialMsgs[:1]
		_ = SaveSession(session)
	}

	reader := bufio.NewReader(os.Stdin)
	for {
		if wd, err := os.Getwd(); err == nil {
			sysInfo.PWD = wd
			session.PWD = wd
		}

		dirPart := Color(filepath.Base(sysInfo.PWD), ansiCyan)
		sessionPart := Color("("+sessionName+")", ansiMagenta)
		promptPart := Color("⚡ q › ", ansiYellow)
		fmt.Printf("\n📂 %s %s %s", dirPart, sessionPart, promptPart)

		input, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		input = strings.TrimSpace(input)
		if input == "" {
			continue
		}
		if input == "exit" || input == "quit" {
			break
		}

		if strings.HasPrefix(input, "/") {
			cmdParts := strings.Fields(input)
			slashCmd := cmdParts[0]

			if slashCmd == "/skills" {
				if len(GlobalLoadedSkills) == 0 {
					PrintWarning("No skills loaded.")
				} else {
					fmt.Println(Bold("\nAvailable Skills:"))
					for _, skill := range GlobalLoadedSkills {
						fmt.Printf("  %s: %s\n", Color(skill.Name, ansiCyan), skill.Description)
						if len(skill.Tags) > 0 {
							fmt.Printf("    Tags: %s\n", Dim(strings.Join(skill.Tags, ", ")))
						}
					}
					fmt.Println()
				}
				continue
			}

			if slashCmd == "/codex" {
				prompt := strings.TrimSpace(strings.TrimPrefix(input, slashCmd))
				if prompt == "" {
					PrintError("Missing prompt. Usage: /codex <prompt>")
				} else if err := runCodexForSession(ctx, session, prompt); err != nil {
					PrintError(err.Error())
				}
				continue
			}

			if slashCmd == "/grok" || slashCmd == "/agy" {
				prompt := strings.TrimSpace(strings.TrimPrefix(input, slashCmd))
				if prompt == "" {
					PrintError(fmt.Sprintf("Missing prompt. Usage: %s <prompt>", slashCmd))
				} else {
					var err error
					if slashCmd == "/grok" {
						err = runGrokForSession(ctx, session, prompt)
					} else {
						err = runAgyForSession(ctx, session, prompt)
					}
					if err != nil {
						PrintError(err.Error())
					}
				}
				continue
			}

			skillName := strings.TrimPrefix(slashCmd, "/")
			if skill, ok := GlobalLoadedSkills[strings.ToLower(skillName)]; ok {
				extraPrompt := ""
				if len(cmdParts) > 1 {
					extraPrompt = strings.Join(cmdParts[1:], " ")
				}

				combinedPrompt := skill.Prompt
				if extraPrompt != "" {
					combinedPrompt = fmt.Sprintf("%s\n\nAdditional instructions:\n%s", combinedPrompt, extraPrompt)
				}

				PrintInfo(fmt.Sprintf("Running skill '%s'...", skill.Name))

				session.Messages = append(session.Messages, ChatMessage{Role: "user", Content: combinedPrompt})
				agent := NewAgent(cfg, sysInfo, session)
				agent.Run(ctx)

				session.PWD = sysInfo.PWD
				_ = SaveSession(session)
				continue
			}

			PrintError(fmt.Sprintf("Unknown command: %s. Type /skills to see available skills.", slashCmd))
			continue
		}

		if input == "clear" || input == "cls" {
			if len(session.Messages) > 1 {
				session.Messages = session.Messages[:1]
				_ = SaveSession(session)
			}
			PrintSuccess("Chat history cleared.")
			continue
		}
		if input == "compact" {
			if err := CompressContext(ctx, cfg, &session.Messages, true); err != nil {
				PrintError(fmt.Sprintf("Failed to compact history: %v", err))
			} else {
				_ = SaveSession(session)
			}
			continue
		}

		session.Messages = append(session.Messages, ChatMessage{Role: "user", Content: input})
		agent := NewAgent(cfg, sysInfo, session)
		agent.Run(ctx)

		session.PWD = sysInfo.PWD
		_ = SaveSession(session)
	}
}

type ToolResult struct {
	Stdout string `json:"stdout,omitempty"`
	Stderr string `json:"stderr,omitempty"`
	Error  string `json:"error,omitempty"`
}

type CommandArgs struct {
	Command string `json:"command"`
}

func executeCommand(shell string, cmdStr string, currentDir string) ([]byte, []byte, string, error) {
	var execCmd *exec.Cmd
	shellLower := strings.ToLower(shell)

	var chainedCmd string
	if runtime.GOOS == "windows" {
		if shellLower == "powershell" || shellLower == "pwsh" {
			chainedCmd = fmt.Sprintf("%s ; Write-Output \"Q_PWD_MARKER\" ; (Get-Location).Path", cmdStr)
			execCmd = exec.Command("powershell", "-NoProfile", "-Command", chainedCmd)
		} else {
			chainedCmd = fmt.Sprintf("%s & echo Q_PWD_MARKER & cd", cmdStr)
			execCmd = exec.Command("cmd", "/c", chainedCmd)
		}
	} else {
		chainedCmd = fmt.Sprintf("%s ; echo \"Q_PWD_MARKER\" ; pwd", cmdStr)
		execCmd = exec.Command(shell, "-c", chainedCmd)
	}

	execCmd.Dir = currentDir

	var stdoutBuf, stderrBuf bytes.Buffer
	execCmd.Stdout = &stdoutBuf
	execCmd.Stderr = io.MultiWriter(os.Stderr, &stderrBuf)
	execCmd.Stdin = os.Stdin

	err := execCmd.Run()

	stdoutBytes := stdoutBuf.Bytes()
	marker := []byte("Q_PWD_MARKER")
	idx := bytes.LastIndex(stdoutBytes, marker)

	var finalDir string
	var userOutput []byte

	if idx != -1 {
		userOutput = stdoutBytes[:idx]
		userOutput = bytes.TrimRight(userOutput, "\r\n")

		markerLen := len(marker)
		afterMarker := stdoutBytes[idx+markerLen:]
		finalDir = strings.TrimSpace(string(afterMarker))
	} else {
		userOutput = stdoutBytes
	}

	if len(userOutput) > 0 {
		_, _ = os.Stdout.Write(userOutput)
		fmt.Println()
	}

	return userOutput, stderrBuf.Bytes(), finalDir, err
}

func resolvePath(baseDir, targetPath string) string {
	if filepath.IsAbs(targetPath) {
		return targetPath
	}
	return filepath.Join(baseDir, targetPath)
}

func openEditor(filePath string, editorOverride string) error {
	editor := editorOverride
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		if runtime.GOOS == "windows" {
			editor = "notepad"
		} else {
			editor = "nano"
		}
	}

	cmd := exec.Command(editor, filePath)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func printMcpUsage() {
	fmt.Println("Usage:")
	fmt.Println("  q mcp ls                       : List all configured MCP servers and their status")
	fmt.Println("  q mcp add <name> [-e K=V]... <cmd> [args...] : Add a new MCP server configuration")
	fmt.Println("  q mcp rm <name>                : Remove an MCP server configuration")
}

func handleMcpList(cfg *Config) {
	if len(cfg.MCPServers) == 0 {
		PrintWarning("No MCP servers configured.")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", Bold("SERVER NAME"), Bold("COMMAND"), Bold("STATUS"), Bold("TOOLS COUNT"), Bold("TOOLS"))
	fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", "-----------", "-------", "------", "-----------", "-----")

	for name, mcpCfg := range cfg.MCPServers {
		cmdStr := mcpCfg.Command + " " + strings.Join(mcpCfg.Args, " ")
		client := NewMCPClient(name, mcpCfg)

		status := Color("Failed", ansiRed)
		toolsStr := "-"
		toolsCount := 0

		if err := client.Start(); err == nil {
			status = Color("Running", ansiGreen)
			tools, err := client.ListTools()
			if err == nil {
				toolsCount = len(tools)
				names := make([]string, len(tools))
				for i, t := range tools {
					names[i] = t.Name
				}
				toolsStr = strings.Join(names, ", ")
			}
			_ = client.Stop()
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\n", Color(name, ansiCyan), Dim(cmdStr), status, toolsCount, toolsStr)
	}
	_ = w.Flush()
}

func handleMcpAdd(cfg *Config) {
	if len(os.Args) < 5 {
		PrintError("Missing arguments. Usage: q mcp add <name> [-e KEY=VAL]... <command> [args...]")
		return
	}

	name := os.Args[3]
	env := make(map[string]string)

	idx := 4
	for idx < len(os.Args) {
		arg := os.Args[idx]
		if arg == "-e" || arg == "--env" {
			if idx+1 >= len(os.Args) {
				PrintError("Missing value for environment variable flag.")
				return
			}
			val := os.Args[idx+1]
			parts := strings.SplitN(val, "=", 2)
			if len(parts) != 2 {
				PrintError(fmt.Sprintf("Invalid environment variable format: %s. Expected KEY=VAL", val))
				return
			}
			env[parts[0]] = parts[1]
			idx += 2
		} else {
			break
		}
	}

	if idx >= len(os.Args) {
		PrintError("Missing command. Usage: q mcp add <name> [-e KEY=VAL]... <command> [args...]")
		return
	}

	command := os.Args[idx]
	args := os.Args[idx+1:]

	if cfg.MCPServers == nil {
		cfg.MCPServers = make(map[string]MCPServerConfig)
	}

	cfg.MCPServers[name] = MCPServerConfig{
		Command: command,
		Args:    args,
		Env:     env,
	}

	if err := SaveConfig(cfg); err != nil {
		PrintError(fmt.Sprintf("Failed to save config: %v", err))
		return
	}

	PrintSuccess(fmt.Sprintf("MCP server '%s' added successfully.", name))
}

func handleMcpRm(cfg *Config) {
	if len(os.Args) < 4 {
		PrintError("Missing server name. Usage: q mcp rm <name>")
		return
	}

	name := os.Args[3]
	if cfg.MCPServers == nil {
		PrintError(fmt.Sprintf("MCP server '%s' does not exist", name))
		return
	}

	if _, ok := cfg.MCPServers[name]; !ok {
		PrintError(fmt.Sprintf("MCP server '%s' does not exist", name))
		return
	}

	delete(cfg.MCPServers, name)

	if err := SaveConfig(cfg); err != nil {
		PrintError(fmt.Sprintf("Failed to save config: %v", err))
		return
	}

	PrintSuccess(fmt.Sprintf("MCP server '%s' removed successfully.", name))
}
