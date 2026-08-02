package providerhost

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/llm-provider/providers/anthropic"
	provideropenai "github.com/snowmerak/llm-provider/providers/openai"
)

const childStartupTimeout = 30 * time.Second

type generation struct {
	endpoint   string
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
	prepared := &generation{configPath: configPath, done: make(chan struct{}), stderr: newLimitedBuffer(32 << 10)}
	abort := true
	defer func() {
		if abort {
			prepared.close()
		}
	}()

	arguments := append([]string(nil), s.argsPrefix...)
	arguments = append(arguments, ChildCommand, "--config", configPath)
	command := exec.CommandContext(s.ctx, s.executable, arguments...)
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

	if err := validateModels(startupContext, prepared.endpoint, value); err != nil {
		return nil, err
	}
	abort = false
	return prepared, nil
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

func validateModels(ctx context.Context, endpoint string, value gateway.Config) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/models", nil)
	if err != nil {
		return err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("providerhost: probe Gateway models: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("providerhost: Gateway model probe returned %s", response.Status)
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return fmt.Errorf("providerhost: decode Gateway model probe: %w", err)
	}
	for _, provider := range value.Providers {
		if !provider.Enabled {
			continue
		}
		prefix := provider.Prefix
		if prefix == "" {
			prefix = provider.ID
		}
		found := false
		for _, model := range payload.Data {
			if strings.HasPrefix(model.ID, prefix+"/") {
				found = true
				break
			}
		}
		if !found {
			if discoveryErr := diagnoseProviderModels(ctx, provider); discoveryErr != nil {
				return fmt.Errorf("providerhost: provider %q model discovery failed: %s", provider.ID, redactProviderError(discoveryErr, provider))
			}
			return fmt.Errorf("providerhost: provider %q returned no models", provider.ID)
		}
	}
	return nil
}

func diagnoseProviderModels(ctx context.Context, config gateway.ProviderConfig) error {
	apiKey := config.APIKey
	if config.APIKeyEnv != "" {
		apiKey = os.Getenv(config.APIKeyEnv)
	}
	switch config.Type {
	case "anthropic", "claude":
		options := []anthropic.Option{anthropic.WithAPIKey(apiKey)}
		if config.BaseURL != "" {
			options = append(options, anthropic.WithBaseURL(config.BaseURL))
		}
		for name, value := range config.Headers {
			options = append(options, anthropic.WithHeader(name, value))
		}
		_, err := anthropic.New(options...).ListModels(ctx)
		return err
	case "openai-compatible", "openrouter", "xai", "grok":
		baseURL := config.BaseURL
		if baseURL == "" {
			switch config.Type {
			case "openrouter":
				baseURL = "https://openrouter.ai/api/v1"
			case "xai", "grok":
				baseURL = "https://api.x.ai/v1"
			}
		}
		options := []provideropenai.Option{
			provideropenai.WithBaseURL(baseURL), provideropenai.WithAPIKey(apiKey),
		}
		for name, value := range config.Headers {
			options = append(options, provideropenai.WithHeader(name, value))
		}
		_, err := provideropenai.New(options...).ListModels(ctx)
		return err
	default:
		return nil
	}
}

func redactProviderError(err error, config gateway.ProviderConfig) string {
	message := err.Error()
	secrets := []string{config.APIKey}
	if config.APIKeyEnv != "" {
		secrets = append(secrets, os.Getenv(config.APIKeyEnv))
	}
	for _, secret := range secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	const maximumErrorLength = 1024
	if len(message) > maximumErrorLength {
		message = message[:maximumErrorLength] + "…"
	}
	return message
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
