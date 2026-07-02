package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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
	fmt.Println("  q config <key>                 : Get a configuration value")
	fmt.Println("  q config <key> <value>         : Set a configuration value")
}

func runInteractiveAgent(cfg *Config, sessionName string) {
	cfg.LastSession = sessionName
	_ = SaveConfig(cfg)

	session, err := LoadSession(sessionName)
	if err != nil {
		PrintError(fmt.Sprintf("Failed to load session '%s': %v", sessionName, err))
		os.Exit(1)
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

		session.Messages = append(session.Messages, ChatMessage{Role: "user", Content: input})
		runAgentLoop(ctx, cfg, sysInfo, session)
		
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

func runAgentLoop(ctx context.Context, cfg *Config, sysInfo *SystemInfo, session *Session) {
	maxLoops := 10
	for i := 0; i < maxLoops; i++ {
		if err := CompressContextIfNeeded(ctx, cfg, &session.Messages); err != nil {
			PrintWarning(fmt.Sprintf("Context compression warning: %v", err))
		}

		PrintAgentThinking()
		msg, err := GenerateCommandMultiTurn(ctx, cfg, session.Messages)
		ClearAgentThinking()
		if err != nil {
			PrintError(fmt.Sprintf("Error calling LLM: %v", err))
			return
		}

		session.Messages = append(session.Messages, *msg)

		if len(msg.ToolCalls) > 0 {
			for _, toolCall := range msg.ToolCalls {
				if toolCall.Function.Name == "run_shell_command" {
					var args CommandArgs
					if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args); err != nil {
						PrintError(fmt.Sprintf("Failed to parse tool arguments: %v", err))
						continue
					}

					cmdStr := args.Command
					fmt.Printf("\n⚙️  %s\n", Color("Executing: "+cmdStr, ansiCyan))
					stdout, stderr, finalDir, runErr := executeCommand(sysInfo.Shell, cmdStr, sysInfo.PWD)

					if finalDir != "" {
						sysInfo.PWD = finalDir
						session.PWD = finalDir
						_ = os.Chdir(finalDir)
						_ = SaveSession(session)
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
					session.Messages = append(session.Messages, ChatMessage{
						Role:       "tool",
						Name:       "run_shell_command",
						ToolCallID: toolCall.ID,
						Content:    string(resJSON),
					})
				}
			}
			continue
		}

		if msg.Content != "" {
			fmt.Println(msg.Content)
		}
		break
	}
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
