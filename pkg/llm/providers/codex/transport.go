package codex

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
)

type transport interface {
	ReadMessage() ([]byte, error)
	WriteMessage([]byte) error
	Close() error
}

type processTransport struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	scanner   *bufio.Scanner
	writeMu   sync.Mutex
	closeOnce sync.Once
	closeErr  error
}

func startProcessTransport(cfg config) (transport, error) {
	cmd := exec.Command(cfg.command, cfg.args...)
	cmd.Env = cfg.environment
	cmd.Stderr = cfg.stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("codex: open stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("codex: open stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("codex: start app-server: %w", err)
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	return &processTransport{cmd: cmd, stdin: stdin, scanner: scanner}, nil
}

func (t *processTransport) ReadMessage() ([]byte, error) {
	if !t.scanner.Scan() {
		if err := t.scanner.Err(); err != nil {
			return nil, err
		}
		return nil, io.EOF
	}
	return bytes.Clone(t.scanner.Bytes()), nil
}

func (t *processTransport) WriteMessage(message []byte) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if _, err := t.stdin.Write(message); err != nil {
		return err
	}
	_, err := t.stdin.Write([]byte{'\n'})
	return err
}

func (t *processTransport) Close() error {
	t.closeOnce.Do(func() {
		_ = t.stdin.Close()
		if t.cmd.Process != nil {
			_ = t.cmd.Process.Kill()
		}
		t.closeErr = t.cmd.Wait()
		var exitError *exec.ExitError
		if errors.As(t.closeErr, &exitError) {
			t.closeErr = nil
		}
	})
	return t.closeErr
}
