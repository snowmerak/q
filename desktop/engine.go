package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const engineProtocol = 1
const controlPath = "/__q_studio/"

// Wire schema shared with q's private __studio_desktop protocol. Keeping the
// shell's dependencies separate preserves go install of the headless commands.
type engineEndpoint struct {
	Protocol int    `json:"protocol"`
	URL      string `json:"url"`
	Token    string `json:"token"`
	Instance string `json:"instance"`
	Secure   bool   `json:"secure"`
	Owned    bool   `json:"owned"`
}

func (e engineEndpoint) validate() error {
	u, err := url.Parse(e.URL)
	if err != nil || e.Protocol != engineProtocol || u.Scheme != "http" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || !net.ParseIP(u.Hostname()).IsLoopback() || u.Port() == "" || len(e.Token) < 32 || e.Instance == "" {
		return errors.New("incompatible or non-local Q engine")
	}
	return nil
}

type engineProcess struct {
	endpoint engineEndpoint
	command  *exec.Cmd
	stdin    io.WriteCloser
	done     chan struct{}
	err      error // published by closing done
	job      io.Closer
}

func bundledEngine() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	name := "q-engine"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(filepath.Dir(executable), name)
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return "", fmt.Errorf("bundled Q engine missing at %s; reinstall Q Studio", path)
	}
	return path, nil
}

func startEngine(ctx context.Context, path string, diagnostics io.Writer) (*engineProcess, error) {
	command := exec.Command(path, "__studio_desktop")
	configureEngine(command)
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	command.Stderr = diagnostics
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		return nil, err
	}
	process := &engineProcess{command: command, stdin: stdin, done: make(chan struct{})}
	process.job, err = containEngine(command)
	go func() { process.err = command.Wait(); close(process.done) }()
	if err != nil {
		_ = process.close()
		return nil, err
	}
	if _, err := io.WriteString(stdin, "{\"protocol\":1}\n"); err != nil {
		_ = process.close()
		return nil, err
	}
	ready := make(chan error, 1)
	go func() {
		reader := bufio.NewReaderSize(stdout, 8192)
		line, err := reader.ReadSlice('\n')
		if err == nil {
			err = json.Unmarshal(line, &process.endpoint)
		}
		if err == nil {
			err = process.endpoint.validate()
		}
		ready <- err
		_, _ = io.Copy(io.Discard, reader)
	}()
	timer := time.NewTimer(45 * time.Second)
	defer timer.Stop()
	select {
	case err := <-ready:
		if err == nil {
			return process, nil
		}
		_ = process.close()
		return nil, fmt.Errorf("q engine readiness: %w", err)
	case <-ctx.Done():
		_ = process.close()
		return nil, ctx.Err()
	case <-timer.C:
		_ = process.close()
		return nil, errors.New("q engine startup timed out; see desktop.log")
	}
}

func (p *engineProcess) close() error {
	_ = p.stdin.Close()
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	select {
	case <-p.done:
	case <-timer.C:
		if p.job != nil {
			_ = p.job.Close()
			p.job = nil
		}
		_ = p.command.Process.Kill()
		<-p.done
	}
	if p.job != nil {
		_ = p.job.Close()
		p.job = nil
	}
	return p.err
}

// engineHost serializes ownership changes and allows in-flight proxy requests
// to finish against their original generation during a reconnect.
type engineHost struct {
	mu          sync.RWMutex
	process     *engineProcess
	path        string
	diagnostics io.Writer
	logger      *slog.Logger
	proxy       *httputil.ReverseProxy
}

type proxyRequestContextKey struct{}

type engineProxyRequest struct {
	endpoint engineEndpoint
	started  time.Time
}

func newEngineHost(path string, diagnostics io.Writer, logger *slog.Logger) *engineHost {
	host := &engineHost{path: path, diagnostics: diagnostics, logger: logger}
	host.proxy = &httputil.ReverseProxy{
		Rewrite: func(request *httputil.ProxyRequest) {
			endpoint := request.In.Context().Value(proxyRequestContextKey{}).(engineProxyRequest).endpoint
			target, _ := url.Parse(endpoint.URL)
			request.SetURL(target)
			request.Out.Host = target.Host
			request.Out.Header.Set("Authorization", "Bearer "+endpoint.Token)
			request.Out.Header.Del("Cookie")
			request.Out.Header.Del("Origin")
		},
		Transport: &http.Transport{Proxy: nil, ResponseHeaderTimeout: 40 * time.Second},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if !errors.Is(err, context.Canceled) {
				request := r.Context().Value(proxyRequestContextKey{}).(engineProxyRequest)
				host.logProxyFailure(r, request.started, proxyFailureReason(err), err.Error())
			}
			http.Error(w, "Q engine reconnecting. Reopen Q Studio in a moment.", http.StatusServiceUnavailable)
		},
	}
	return host
}

func (h *engineHost) connect(ctx context.Context) error {
	p, err := startEngine(ctx, h.path, h.diagnostics)
	if err != nil {
		return err
	}
	h.mu.Lock()
	h.process = p
	h.mu.Unlock()
	return nil
}

func (h *engineHost) endpoint() (engineEndpoint, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.process == nil {
		return engineEndpoint{}, false
	}
	return h.process.endpoint, true
}

func (h *engineHost) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, controlPath) {
		http.NotFound(w, r)
		return
	}
	started := time.Now()
	endpoint, ok := h.endpoint()
	if !ok {
		h.logProxyFailure(r, started, "engine_unavailable", "no Q engine endpoint is connected")
		http.Error(w, "Q engine reconnecting", http.StatusServiceUnavailable)
		return
	}
	request := engineProxyRequest{endpoint: endpoint, started: started}
	h.proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), proxyRequestContextKey{}, request)))
}

func proxyFailureReason(err error) string {
	if networkErr, ok := errors.AsType[net.Error](err); ok && networkErr.Timeout() {
		return "timeout"
	}
	if operation, ok := errors.AsType[*net.OpError](err); ok && operation.Op == "dial" {
		return "connection_failed"
	}
	return "transport_error"
}

func (h *engineHost) logProxyFailure(r *http.Request, started time.Time, reason, detail string) {
	h.logger.ErrorContext(r.Context(), "desktop.engine.proxy_failed",
		"method", boundedProxyLogText(r.Method), "path", boundedProxyLogText(r.URL.EscapedPath()),
		"status", http.StatusServiceUnavailable, "duration_ms", time.Since(started).Milliseconds(),
		"reason_code", reason, "error", boundedProxyLogText(detail))
}

func boundedProxyLogText(value string) string {
	const maxBytes = 2048
	if len(value) > maxBytes {
		return value[:maxBytes] + "…"
	}
	return value
}

func (h *engineHost) browserURL(ctx context.Context) (string, error) {
	e, ok := h.endpoint()
	if !ok {
		return "", errors.New("q engine is reconnecting")
	}
	if !e.Secure {
		return e.URL, nil
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, e.URL+controlPath+"launch?path=%2F", nil)
	req.Header.Set("Authorization", "Bearer "+e.Token)
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	response, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = response.Body.Close() }()
	var result struct {
		Path string `json:"path"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&result) != nil || !strings.HasPrefix(result.Path, controlPath+"enter/") {
		return "", errors.New("could not open browser session")
	}
	return e.URL + result.Path, nil
}

func (h *engineHost) close() {
	h.mu.Lock()
	p := h.process
	h.process = nil
	h.mu.Unlock()
	if p != nil {
		_ = p.close()
	}
	h.proxy.Transport.(*http.Transport).CloseIdleConnections()
}

// Reconnect the engine, never replay an accepted agent turn. Borrowed engines
// are checked too: their relay process intentionally stays alive until quit.
func (h *engineHost) monitor(ctx context.Context, changed func()) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		e, ok := h.endpoint()
		if !ok {
			if h.connect(ctx) == nil {
				changed()
			}
			continue
		}
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		req, _ := http.NewRequestWithContext(probeCtx, http.MethodGet, e.URL+controlPath+"status", nil)
		req.Header.Set("Authorization", "Bearer "+e.Token)
		client := &http.Client{Transport: &http.Transport{Proxy: nil}}
		resp, err := client.Do(req)
		alive := err == nil && resp.StatusCode == http.StatusOK
		if resp != nil {
			var status struct {
				Protocol int    `json:"protocol"`
				Instance string `json:"instance"`
			}
			alive = alive && json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&status) == nil && status.Protocol == engineProtocol && status.Instance == e.Instance
			_ = resp.Body.Close()
		}
		client.CloseIdleConnections()
		cancel()
		if alive {
			continue
		}
		h.close()
		changed()
		for delay := time.Second; ctx.Err() == nil; delay = min(delay*2, 30*time.Second) {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			if h.connect(ctx) == nil {
				changed()
				break
			}
		}
	}
}
