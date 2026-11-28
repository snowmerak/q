package cli

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

//go:embed script_runner.cjs
var scriptRunner string

type scriptMessage struct {
	Type   string          `json:"type"`
	ID     int64           `json:"id,omitempty"`
	Args   []string        `json:"args,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *commandError   `json:"error,omitempty"`
}

// runScript owns only the Node child. Session resources deliberately survive
// completion/cancellation, exactly as they do across ordinary CLI invocations.
func runScript(caller context.Context, options appOptions, args []string, stdin io.Reader, stderr io.Writer) (result json.RawMessage, resultErr error) {
	f := flag.NewFlagSet("run", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	node := f.String("node", "node", "Node.js executable")
	if err := f.Parse(args); err != nil {
		return nil, invalid("%v", err)
	}
	if f.NArg() < 1 {
		return nil, invalid("run expects [--node PATH] SCRIPT|- [ARG ...]")
	}
	path := f.Arg(0)
	source, err := readScript(path, stdin)
	if err != nil {
		return nil, err
	}
	executable, err := exec.LookPath(*node)
	if err != nil {
		return nil, &commandError{Code: "node_not_found", Message: "run requires Node.js on PATH or run --node PATH; other CLI commands do not require Node.js"}
	}
	filename := path
	if path == "-" {
		filename = "stdin.cjs"
	}
	filename, err = filepath.Abs(filename)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(caller, options.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-e", scriptRunner)
	browserProcess(cmd, true) // Hide the console window on Windows; keep pipes.
	cmd.WaitDelay = time.Second
	cmd.Stderr = stderr
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	defer func() { _ = input.Close() }()
	output, writer := io.Pipe()
	defer func() { _ = output.Close() }()
	cmd.Stdout = writer
	if err := cmd.Start(); err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("start JavaScript runner: %w", err)
	}
	done := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		_ = writer.Close()
		done <- err
	}()
	// Always reap this child, including malformed protocol and canceled RPCs.
	effects := false
	defer func() {
		cancel()
		_ = output.Close() // Unblock the copy goroutine if protocol reading failed.
		if waitErr := <-done; resultErr == nil && waitErr != nil {
			result, resultErr = nil, failure(fmt.Errorf("JavaScript runner exited: %w", waitErr), effects)
		}
	}()
	if err := writeScriptMessage(input, map[string]any{"source": source, "filename": filename, "args": f.Args()[1:], "session": options.Session}); err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 4096), maxMessage)
	var final *scriptMessage
	var d discovery
	connected := false
	for scanner.Scan() {
		var message scriptMessage
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			return nil, failure(errors.New("invalid runner output; use console for logs, stdout is reserved"), effects)
		}
		if final != nil {
			return nil, failure(errors.New("runner sent data after its result"), effects)
		}
		switch message.Type {
		case "result":
			final = &message
		case "call":
			r, parseErr := parseScriptCommand(message.Args)
			var reply response
			if parseErr != nil {
				reply.Error = failure(parseErr, false)
			} else {
				if !connected {
					if options.Named && r.Command != "status" {
						effects = true // Startup may create a managed browser.
						d, _, err = ensureSession(ctx, options, serveOptions{})
					} else {
						d, err = loadDiscovery(options.State)
						if err == nil {
							err = checkSession(options, d, serveOptions{Endpoint: options.Endpoint, InputMode: options.InputMode, Chrome: options.Chrome, Headless: options.Headless, headlessSet: options.headlessSet})
						}
					}
					if err != nil {
						return nil, failure(err, effects)
					}
					connected = true
				}
				effects = effects || !readOnly(r.Command)
				deadline, _ := ctx.Deadline()
				r.TimeoutMS = max(1, time.Until(deadline).Milliseconds())
				reply, err = call(ctx, d, r)
				if err != nil {
					reply = response{Error: failure(err, !readOnly(r.Command))}
				}
			}
			if err := writeScriptMessage(input, map[string]any{"id": message.ID, "response": reply}); err != nil {
				if ctx.Err() != nil {
					err = ctx.Err()
				}
				return nil, failure(err, effects)
			}
		default:
			return nil, failure(errors.New("unknown runner message type"), effects)
		}
	}
	if ctx.Err() != nil {
		return nil, failure(ctx.Err(), effects)
	}
	if err := scanner.Err(); err != nil {
		return nil, failure(fmt.Errorf("read JavaScript runner: %w", err), effects)
	}
	if final == nil {
		return nil, failure(errors.New("JavaScript runner exited without a result"), effects)
	}
	if final.Error != nil {
		return final.Result, failure(final.Error, effects)
	}
	if len(final.Result) == 0 {
		return json.RawMessage("null"), nil
	}
	return final.Result, nil
}

func readScript(path string, stdin io.Reader) (string, error) {
	reader := stdin
	if path != "-" {
		file, err := os.Open(path)
		if err != nil {
			return "", err
		}
		defer func() { _ = file.Close() }()
		reader = file
	}
	data, err := io.ReadAll(io.LimitReader(reader, (1<<20)+1))
	if err != nil {
		return "", err
	}
	if len(data) > 1<<20 {
		return "", invalid("script exceeds 1 MiB")
	}
	return strings.TrimPrefix(string(data), "\ufeff"), nil
}

func writeScriptMessage(w io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) >= maxMessage {
		return errors.New("JavaScript bridge message exceeds 16 MiB")
	}
	_, err = w.Write(append(data, '\n'))
	return err
}

func parseScriptCommand(args []string) (request, error) {
	r, err := parseCommand(args, strings.NewReader(""))
	if err == nil && (r.Command == "stop" || strings.HasPrefix(r.Command, "session.")) {
		err = invalid("run uses the selected session; manage session lifecycle outside the script")
	}
	return r, err
}
