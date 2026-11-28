// Package cli implements the local command client and the session process.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type appOptions struct {
	State         string
	Timeout       time.Duration
	Session       string
	DataDir       string
	Metadata      string
	Endpoint      string
	Chrome        string
	Headless      bool
	InputMode     string
	CommandPrefix []string // Host subcommand tokens used when re-executing serve.
	headlessSet   bool
	Named         bool
}
type serveOptions struct {
	Endpoint                       string
	Chrome                         string
	Headless                       bool
	InputMode                      string
	headlessSet                    bool
	CommandTimeout, CleanupTimeout time.Duration
	commandSet, cleanupSet         bool
}

// Main writes machine-readable results to stdout and errors to stderr.
func Main(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return MainWithPrefix(ctx, args, stdin, stdout, stderr, nil)
}

// MainWithPrefix preserves an embedding host's dispatch path when starting a
// background process. Args have already had these prefix tokens removed.
func MainWithPrefix(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, prefix []string) int {
	f := flag.NewFlagSet("cdp-q-driver", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	options := appOptions{CommandPrefix: append([]string(nil), prefix...)}
	f.StringVar(&options.State, "state", "", "explicit legacy runtime file; mutually exclusive with --session/--data-dir")
	f.StringVar(&options.Session, "session", "default", "named session ID; absent sessions are started on first use")
	f.StringVar(&options.DataDir, "data-dir", "", "application data root (default: OS application data directory)")
	f.StringVar(&options.Endpoint, "endpoint", "", "CDP endpoint for first use of a named session")
	f.StringVar(&options.Chrome, "chrome", "", "Chrome/Chromium executable to launch with a dedicated session profile")
	f.BoolVar(&options.Headless, "headless", false, "launch Chrome without a visible window")
	f.StringVar(&options.InputMode, "input-mode", "", "input pacing: paced (default) or fast; fixed until session restart")
	f.DurationVar(&options.Timeout, "timeout", 30*time.Second, "whole command timeout")
	emitError := func(err error, code int) int {
		_ = json.NewEncoder(stderr).Encode(response{OK: false, Error: failure(err, false)})
		return code
	}
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, _ = io.WriteString(stdout, help)
			return 0
		}
		return emitError(invalid("%v", err), 2)
	}
	args = f.Args()
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		_, _ = io.WriteString(stdout, help)
		return 0
	}
	if options.Timeout < time.Millisecond || options.Timeout > 24*time.Hour {
		return emitError(invalid("--timeout must be between 1ms and 24h"), 2)
	}
	seen := make(map[string]bool)
	f.Visit(func(v *flag.Flag) { seen[v.Name] = true })
	options.headlessSet = seen["headless"]
	if err := configureSession(&options, seen); err != nil {
		return emitError(err, 2)
	}
	if args[0] == "run" {
		result, err := runScript(ctx, options, args[1:], stdin, stderr)
		if err != nil {
			code := 1
			var own *commandError
			if errors.As(err, &own) && own.Code == "invalid_argument" {
				code = 2
			}
			_ = json.NewEncoder(stderr).Encode(response{OK: false, Result: result, Error: failure(err, false)})
			return code
		}
		if err := json.NewEncoder(stdout).Encode(response{OK: true, Result: result}); err != nil {
			return 1
		}
		return 0
	}
	if args[0] == "start" || args[0] == "serve" {
		serve, err := parseServe(args[1:])
		if err != nil {
			return emitError(err, 2)
		}
		if args[0] == "serve" {
			serve, err = resolveServe(options, serve)
			if err == nil && options.Named {
				serve, err = autoChrome(serve)
			}
			if err != nil {
				return emitError(err, 1)
			}
			if serve.Endpoint == "" && serve.Chrome == "" {
				return emitError(&commandError{Code: "endpoint_required", Message: "serve requires --chrome PATH, --endpoint URL, or saved session configuration"}, 2)
			}
			if err := runServer(ctx, options, serve); err != nil {
				return emitError(err, 1)
			}
			return 0
		}
		commandCtx, cancel := context.WithTimeout(ctx, options.Timeout)
		defer cancel()
		var result startResult
		if options.Named {
			_, result, err = ensureSession(commandCtx, options, serve)
		} else {
			serve, err = resolveServe(options, serve)
			if err == nil && serve.Endpoint == "" {
				err = invalid("start requires --endpoint URL")
			}
			if err == nil {
				result, err = startProcess(commandCtx, options, serve)
			}
		}
		if err != nil {
			return emitError(err, 1)
		}
		if err := json.NewEncoder(stdout).Encode(response{OK: true, Result: result}); err != nil {
			return 1
		}
		return 0
	}
	r, err := parseCommand(args, stdin)
	if err != nil {
		return emitError(err, 2)
	}
	r.TimeoutMS = options.Timeout.Milliseconds()
	commandCtx, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()
	if strings.HasPrefix(r.Command, "session.") {
		if !options.Named {
			return emitError(invalid("session management cannot be combined with --state"), 2)
		}
		var result any
		switch r.Command {
		case "session.list":
			result, err = listSessions(commandCtx, options)
		case "session.info":
			result, err = sessionInfo(commandCtx, options)
		case "session.ensure":
			_, result, err = ensureSession(commandCtx, options, serveOptions{})
		}
		if err != nil {
			return emitError(err, 1)
		}
		if err := json.NewEncoder(stdout).Encode(response{OK: true, Result: result}); err != nil {
			return 1
		}
		return 0
	}
	var d discovery
	if options.Named && r.Command != "status" && r.Command != "stop" {
		d, _, err = ensureSession(commandCtx, options, serveOptions{})
	} else {
		d, err = loadDiscovery(options.State)
		if err == nil {
			err = checkSession(options, d, serveOptions{Endpoint: options.Endpoint, Chrome: options.Chrome, Headless: options.Headless, headlessSet: options.headlessSet, InputMode: options.InputMode})
		}
	}
	if err != nil {
		return emitError(err, 1)
	}
	result, err := call(commandCtx, d, r)
	if err != nil {
		// Once an RPC is dispatched its acknowledgement can be lost. No command
		// is replayed; mutating transport failures conservatively report effects.
		_ = json.NewEncoder(stderr).Encode(response{OK: false, Error: failure(err, !readOnly(r.Command))})
		return 1
	}
	if !result.OK {
		_ = json.NewEncoder(stderr).Encode(result)
		return 1
	}
	if r.Command == "stop" {
		if err := waitStopped(commandCtx, options.State, d.Instance); err != nil {
			return emitError(failure(err, true), 1)
		}
	}
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		return 1
	}
	return 0
}

func parseServe(args []string) (serveOptions, error) {
	var o serveOptions
	f := flag.NewFlagSet("serve", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.StringVar(&o.Endpoint, "endpoint", "", "explicit browser CDP endpoint")
	f.StringVar(&o.Chrome, "chrome", "", "Chrome/Chromium executable to launch")
	f.BoolVar(&o.Headless, "headless", false, "launch Chrome without a visible window")
	f.StringVar(&o.InputMode, "input-mode", "", "input pacing: paced or fast")
	f.DurationVar(&o.CommandTimeout, "command-timeout", 15*time.Second, "SDK command timeout")
	f.DurationVar(&o.CleanupTimeout, "cleanup-timeout", 5*time.Second, "SDK cleanup budget")
	if err := f.Parse(args); err != nil {
		return o, invalid("%v", err)
	}
	f.Visit(func(v *flag.Flag) {
		if v.Name == "headless" {
			o.headlessSet = true
		}
		if v.Name == "command-timeout" {
			o.commandSet = true
		}
		if v.Name == "cleanup-timeout" {
			o.cleanupSet = true
		}
	})
	if len(f.Args()) != 0 {
		return o, invalid("start/serve accept no positional arguments")
	}
	if o.CommandTimeout <= 0 || o.CommandTimeout > 24*time.Hour || o.CleanupTimeout <= 0 || o.CleanupTimeout > time.Minute {
		return o, invalid("command timeout must be in (0,24h], cleanup timeout in (0,1m]")
	}
	return o, nil
}

func call(ctx context.Context, d discovery, r request) (response, error) {
	var result response
	data, err := json.Marshal(r)
	if err != nil {
		return result, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+d.Address+"/rpc", bytes.NewReader(data))
	if err != nil {
		return result, err
	}
	req.Header.Set("Authorization", "Bearer "+d.Token)
	req.Header.Set("Content-Type", "application/json")
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{}).DialContext, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return result, err
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(res.Body, maxMessage+1))
	if err != nil {
		return result, err
	}
	if len(body) > maxMessage {
		return result, errors.New("RPC response exceeded 16 MiB; result may have effects")
	}
	var wire struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
		Error  *commandError   `json:"error"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return result, fmt.Errorf("invalid RPC response: %w", err)
	}
	if !wire.OK && wire.Error == nil {
		return result, errors.New("RPC failed without an error outcome")
	}
	if res.StatusCode != http.StatusOK && wire.OK {
		return result, fmt.Errorf("unexpected RPC HTTP status %d", res.StatusCode)
	}
	result.OK, result.Error = wire.OK, wire.Error
	if len(wire.Result) > 0 && string(wire.Result) != "null" {
		result.Result = wire.Result
	}
	return result, nil
}

func startProcess(ctx context.Context, options appOptions, serve serveOptions) (startResult, error) {
	if _, err := os.Lstat(options.State); err == nil {
		return startResult{}, &commandError{Code: "already_exists", Message: "session file already exists; use status/stop, or remove a stale file only after confirming its process has exited"}
	} else if !errors.Is(err, os.ErrNotExist) {
		return startResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(options.State), 0700); err != nil {
		return startResult{}, err
	}
	log, err := os.CreateTemp(filepath.Dir(options.State), "qdriver-*.log")
	if err != nil {
		return startResult{}, err
	}
	defer func() { _ = log.Close() }()
	executable, err := os.Executable()
	if err != nil {
		return startResult{}, err
	}
	startupTimeout := options.Timeout
	if deadline, ok := ctx.Deadline(); ok {
		startupTimeout = time.Until(deadline)
		if startupTimeout < time.Millisecond {
			return startResult{}, context.DeadlineExceeded
		}
	}
	childArgs := append(append([]string{}, options.CommandPrefix...), "--timeout", startupTimeout.String())
	if options.Named {
		childArgs = append(childArgs, "--session", options.Session, "--data-dir", options.DataDir)
	} else {
		childArgs = append(childArgs, "--state", options.State)
	}
	childArgs = append(childArgs, "serve")
	if serve.Chrome != "" {
		childArgs = append(childArgs, "--chrome", serve.Chrome, fmt.Sprintf("--headless=%t", serve.Headless))
	} else {
		childArgs = append(childArgs, "--endpoint", serve.Endpoint)
	}
	childArgs = append(childArgs, "--input-mode", serve.InputMode, "--command-timeout", serve.CommandTimeout.String(), "--cleanup-timeout", serve.CleanupTimeout.String())
	cmd := exec.Command(executable, childArgs...)
	backgroundProcess(cmd)
	cmd.Stdin = nil
	cmd.Stdout = log
	cmd.Stderr = log
	if err := cmd.Start(); err != nil {
		return startResult{}, err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if d, err := loadDiscovery(options.State); err == nil && d.PID == cmd.Process.Pid {
			probe, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
			reply, err := call(probe, d, request{Command: "status", TimeoutMS: 500})
			cancel()
			if err == nil && reply.OK {
				return startResult{Session: d.Session, Instance: d.Instance, PID: d.PID, State: options.State, Log: log.Name(), Created: true}, nil
			}
		}
		select {
		case err := <-exited:
			return startResult{}, fmt.Errorf("control process exited during startup (%v); inspect %s", err, log.Name())
		case <-ctx.Done():
			if serve.Chrome != "" {
				// Give the child time to unwind its startup deadline and close its
				// browser. If readiness raced cancellation, stop only this PID's
				// authenticated runtime rather than abandoning a running browser.
				cleanup, stop := context.WithTimeout(context.Background(), 7*time.Second)
				defer stop()
				for cleanup.Err() == nil {
					select {
					case waitErr := <-exited:
						return startResult{}, errors.Join(ctx.Err(), waitErr, removeOwnedState(options.State, cmd.Process.Pid, ""))
					default:
					}
					if d, err := loadDiscovery(options.State); err == nil && d.PID == cmd.Process.Pid {
						_, _ = call(cleanup, d, request{Command: "stop", TimeoutMS: 6000})
					}
					select {
					case <-cleanup.Done():
					case <-ticker.C:
					}
				}
			}
			_ = cmd.Process.Kill()
			var waitErr error
			select {
			case waitErr = <-exited:
			case <-time.After(5 * time.Second):
				return startResult{}, errors.Join(ctx.Err(), errors.New("startup process did not exit after termination"))
			}
			var terminated *exec.ExitError
			if errors.As(waitErr, &terminated) {
				waitErr = nil
			} // Expected after termination.
			return startResult{}, errors.Join(ctx.Err(), waitErr, removeOwnedState(options.State, cmd.Process.Pid, ""))
		case <-ticker.C:
		}
	}
}

func waitStopped(ctx context.Context, path, instance string) error {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if d, err := loadDiscovery(path); err == nil && d.Instance != instance {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
