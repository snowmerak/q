package providerhost

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/snowmerak/llm-provider/gateway"
)

const childStartupTimeout = 30 * time.Second

type generation struct {
	endpoint   string
	apiKey     string
	configPath string
	command    *exec.Cmd
	stdin      io.WriteCloser
	done       chan struct{}
	waitMu     sync.Mutex
	waitErr    error
	stderr     *limitedBuffer
	closeOnce  sync.Once
}

type Supervisor struct {
	ctx        context.Context
	store      Store
	executable string
	argsPrefix []string
	mu         sync.RWMutex
	current    *generation
}

func NewSupervisor(ctx context.Context, store Store) (*Supervisor, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("providerhost: locate q executable: %w", err)
	}
	return &Supervisor{ctx: ctx, store: store, executable: executable}, nil
}

func (s *Supervisor) Prepare(ctx context.Context, value gateway.Config) (*generation, error) {
	configPath, err := s.store.WriteRuntimeSnapshot(value)
	if err != nil {
		return nil, err
	}
	apiKey, err := NewEphemeralAPIKey()
	if err != nil {
		_ = os.Remove(configPath)
		return nil, err
	}
	prepared := &generation{
		apiKey: apiKey, configPath: configPath,
		done: make(chan struct{}), stderr: newLimitedBuffer(32 << 10),
	}
	abort := true
	defer func() {
		if abort {
			prepared.close()
		}
	}()

	arguments := append([]string(nil), s.argsPrefix...)
	arguments = append(arguments, ChildCommand, "--config", configPath)
	command := exec.CommandContext(s.ctx, s.executable, arguments...)
	command.Env = environmentWithValue(os.Environ(), ChildAPIKeyEnv, apiKey)
	configureChildProcess(command)
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("providerhost: open child stdin: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("providerhost: open child stdout: %w", err)
	}
	command.Stderr = prepared.stderr
	prepared.command = command
	prepared.stdin = stdin
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("providerhost: start Gateway child: %w", err)
	}
	go func() {
		prepared.waitMu.Lock()
		prepared.waitErr = command.Wait()
		prepared.waitMu.Unlock()
		close(prepared.done)
	}()

	reader := bufio.NewReader(stdout)
	ready := make(chan ReadyMessage, 1)
	decodeErr := make(chan error, 1)
	go func() {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			decodeErr <- err
			return
		}
		var message ReadyMessage
		if err := json.Unmarshal(line, &message); err != nil {
			decodeErr <- err
			return
		}
		ready <- message
		_, _ = io.Copy(io.Discard, reader)
	}()

	startupContext, cancel := context.WithTimeout(ctx, childStartupTimeout)
	defer cancel()
	select {
	case message := <-ready:
		if message.Event != "ready" || !strings.HasPrefix(message.BaseURL, "http://127.0.0.1:") {
			return nil, fmt.Errorf("providerhost: invalid child readiness message: %#v", message)
		}
		prepared.endpoint = message.BaseURL + "/v1"
	case err := <-decodeErr:
		return nil, fmt.Errorf("providerhost: read child readiness: %w%s", err, prepared.stderrSuffix())
	case <-prepared.done:
		return nil, fmt.Errorf("providerhost: Gateway child exited during startup: %v%s", prepared.waitError(), prepared.stderrSuffix())
	case <-startupContext.Done():
		return nil, fmt.Errorf("providerhost: Gateway child startup timed out%s", prepared.stderrSuffix())
	}

	abort = false
	return prepared, nil
}

func environmentWithValue(environment []string, key, value string) []string {
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(name, key) {
			continue
		}
		result = append(result, entry)
	}
	return append(result, key+"="+value)
}

func (s *Supervisor) Activate(prepared *generation) {
	s.mu.Lock()
	previous := s.current
	s.current = prepared
	s.mu.Unlock()
	if previous != nil {
		previous.close()
	}
}

func (s *Supervisor) Endpoint() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.current == nil {
		return ""
	}
	return s.current.endpoint
}

func (s *Supervisor) APIKey() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.current == nil {
		return ""
	}
	return s.current.apiKey
}

func (s *Supervisor) Close() error {
	s.mu.Lock()
	current := s.current
	s.current = nil
	s.mu.Unlock()
	if current != nil {
		current.close()
	}
	return nil
}

func (g *generation) close() {
	g.closeOnce.Do(func() {
		if g.stdin != nil {
			_ = g.stdin.Close()
		}
		if g.command != nil && g.command.Process != nil {
			select {
			case <-g.done:
			case <-time.After(12 * time.Second):
				_ = g.command.Process.Kill()
				<-g.done
			}
		}
		if g.configPath != "" {
			_ = os.Remove(g.configPath)
		}
	})
}

func (g *generation) waitError() error {
	g.waitMu.Lock()
	defer g.waitMu.Unlock()
	return g.waitErr
}

func (g *generation) stderrSuffix() string {
	message := strings.TrimSpace(g.stderr.String())
	if message == "" {
		return ""
	}
	return ": " + message
}

type limitedBuffer struct {
	mu    sync.Mutex
	limit int
	data  []byte
}

func newLimitedBuffer(limit int) *limitedBuffer { return &limitedBuffer{limit: limit} }

func (b *limitedBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, value...)
	if len(b.data) > b.limit {
		b.data = append([]byte(nil), b.data[len(b.data)-b.limit:]...)
	}
	return len(value), nil
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(bytes.Clone(b.data))
}
