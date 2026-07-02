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
	if len(os.Args) < 2 {
		runInteractiveAgent()
		return
	}

	executeMode := false
	var promptArgs []string
	for _, arg := range os.Args[1:] {
		if arg == "e" || arg == "-e" {
			executeMode = true
		} else {
			promptArgs = append(promptArgs, arg)
		}
	}

	if len(promptArgs) == 0 {
		printUsage()
		os.Exit(1)
	}

	prompt := strings.Join(promptArgs, " ")

	cfg, err := LoadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading configuration: %v\n", err)
		os.Exit(1)
	}

	sysInfo := GetSystemInfo()
	ctx := context.Background()

	messages := CreateInitialMessages(sysInfo, prompt)
	runAgentLoop(ctx, cfg, sysInfo, &messages, executeMode)
}

func printUsage() {
	fmt.Println("Usage:")
	fmt.Println("  q <prompt>                     : Show generated CLI command without executing")
	fmt.Println("  q e <prompt> | q -e <prompt>   : Execute agentically with tool calling")
	fmt.Println("  q (no arguments)               : Enter stateful interactive agent REPL")
}

func runInteractiveAgent() {
	cfg, err := LoadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading configuration: %v\n", err)
		os.Exit(1)
	}

	sysInfo := GetSystemInfo()
	ctx := context.Background()

	PrintInfo("Welcome to q Interactive Agent Shell.")
	PrintInfo("Type 'exit' or 'quit' to close.")

	messages := CreateInitialMessages(sysInfo, "Start session")
	messages = messages[:1]

	reader := bufio.NewReader(os.Stdin)
	for {
		if wd, err := os.Getwd(); err == nil {
			sysInfo.PWD = wd
		}
		
		dirPart := Color(filepath.Base(sysInfo.PWD), ansiCyan)
		promptPart := Color("⚡ q › ", ansiYellow)
		fmt.Printf("\n📂 %s %s", dirPart, promptPart)
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

		messages = append(messages, ChatMessage{Role: "user", Content: input})
		runAgentLoop(ctx, cfg, sysInfo, &messages, true)
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

func runAgentLoop(ctx context.Context, cfg *Config, sysInfo *SystemInfo, messages *[]ChatMessage, executeMode bool) {
	maxLoops := 10
	for i := 0; i < maxLoops; i++ {
		PrintAgentThinking()
		msg, err := GenerateCommandMultiTurn(ctx, cfg, *messages)
		ClearAgentThinking()
		if err != nil {
			PrintError(fmt.Sprintf("Error calling LLM: %v", err))
			return
		}

		*messages = append(*messages, *msg)

		if len(msg.ToolCalls) > 0 {
			for _, toolCall := range msg.ToolCalls {
				if toolCall.Function.Name == "run_shell_command" {
					var args CommandArgs
					if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args); err != nil {
						PrintError(fmt.Sprintf("Failed to parse tool arguments: %v", err))
						continue
					}

					cmdStr := args.Command
					if !executeMode {
						fmt.Printf("Suggested command: %s\n", Color(cmdStr, ansiGreen))
						return
					}

					fmt.Printf("\n⚙️  %s\n", Color("Executing: "+cmdStr, ansiCyan))
					stdout, stderr, finalDir, runErr := executeCommand(sysInfo.Shell, cmdStr, sysInfo.PWD)

					// Sync PWD if updated
					if finalDir != "" {
						sysInfo.PWD = finalDir
						_ = os.Chdir(finalDir) // update Go runtime PWD too
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
					*messages = append(*messages, ChatMessage{
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

	// Chain marker commands to capture PWD after execution
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
		// Trim the newline that usually precedes the marker
		userOutput = bytes.TrimRight(userOutput, "\r\n")

		markerLen := len(marker)
		afterMarker := stdoutBytes[idx+markerLen:]
		finalDir = strings.TrimSpace(string(afterMarker))
	} else {
		userOutput = stdoutBytes
	}

	// Print clean user output to stdout
	if len(userOutput) > 0 {
		_, _ = os.Stdout.Write(userOutput)
		fmt.Println() // Ensure trailing newline
	}

	return userOutput, stderrBuf.Bytes(), finalDir, err
}
