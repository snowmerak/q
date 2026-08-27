package tools

import (
	"os"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/charmbracelet/x/ansi"
	"github.com/snowmerak/q/mcpconfig"
)

const maximumMCPStderrBytes = 16 << 10

// mcpDiagnostics is a bounded, per-connection stderr sink. Stderr may contain
// normal logs, so it is only surfaced alongside an actual MCP failure.
type mcpDiagnostics struct {
	mu        sync.Mutex
	body      []byte
	truncated bool
	secrets   []string
}

func newMCPDiagnostics(value mcpconfig.ServerConfig) *mcpDiagnostics {
	known := make(map[string]struct{})
	add := func(secret string) {
		if secret != "" {
			known[secret] = struct{}{}
		}
	}
	for _, source := range value.Env {
		add(os.Getenv(source))
	}
	for _, secret := range value.ResolvedEnv {
		add(secret)
	}
	addHeader := func(secret string) {
		add(secret)
		// Servers sometimes log just the credential, without its auth scheme.
		if parts := strings.Fields(secret); len(parts) == 2 {
			switch strings.ToLower(parts[0]) {
			case "bearer", "basic", "token":
				add(parts[1])
			}
		}
	}
	for _, source := range value.Headers {
		addHeader(os.Getenv(source))
	}
	for _, secret := range value.ResolvedHeaders {
		addHeader(secret)
	}
	// Stdio processes inherit the parent environment as well as explicit MCP
	// overrides. Include conventional credential variables without hiding every
	// ordinary PATH, locale, and runtime setting needed for troubleshooting.
	for _, variable := range os.Environ() {
		name, secret, _ := strings.Cut(variable, "=")
		name = strings.ToUpper(name)
		for _, marker := range []string{"TOKEN", "SECRET", "PASSWORD", "PASSWD", "API_KEY", "PRIVATE_KEY", "CREDENTIAL", "AUTHORIZATION"} {
			if strings.Contains(name, marker) {
				add(secret)
				break
			}
		}
	}
	d := &mcpDiagnostics{}
	for secret := range known {
		d.secrets = append(d.secrets, secret)
	}
	sort.Slice(d.secrets, func(i, j int) bool { return len(d.secrets[i]) > len(d.secrets[j]) })
	return d
}

func (d *mcpDiagnostics) Write(value []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := len(value)
	if n >= maximumMCPStderrBytes {
		d.truncated = d.truncated || len(d.body)+n > maximumMCPStderrBytes
		d.body = append(d.body[:0], value[n-maximumMCPStderrBytes:]...)
		return n, nil
	}
	if overflow := len(d.body) + n - maximumMCPStderrBytes; overflow > 0 {
		copy(d.body, d.body[overflow:])
		d.body = d.body[:len(d.body)-overflow]
		d.truncated = true
	}
	d.body = append(d.body, value...)
	return n, nil
}

func (d *mcpDiagnostics) sanitize(value string) string {
	value = strings.ToValidUTF8(ansi.Strip(value), "�")
	for _, secret := range d.secrets {
		value = strings.ReplaceAll(value, secret, "[REDACTED]")
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return -1
		}
		return r
	}, value)
}

func (d *mcpDiagnostics) stderr() string {
	d.mu.Lock()
	value, truncated := string(d.body), d.truncated
	d.mu.Unlock()
	if truncated {
		// Drop a cut-off first line; it could contain only the suffix of a
		// credential that full-value redaction would otherwise fail to match.
		_, value, _ = strings.Cut(value, "\n")
	}
	value = strings.TrimSpace(d.sanitize(value))
	if len(value) > maximumMCPStderrBytes {
		value = strings.ToValidUTF8(value[len(value)-maximumMCPStderrBytes:], "�")
		truncated = true
	}
	if truncated {
		value = "… stderr truncated\n" + value
	}
	return value
}

// Preserve the original error chain for cancellation and transport handling,
// while exposing only the sanitized diagnostic text to ACP/TUI consumers.
func (d *mcpDiagnostics) wrap(err error) error {
	if err == nil || d == nil {
		return err
	}
	message := d.sanitize(err.Error())
	if detail := d.stderr(); detail != "" {
		message += "\nMCP server stderr (recent):\n" + detail
	}
	return &mcpDiagnosticError{cause: err, message: message}
}

type mcpDiagnosticError struct {
	cause   error
	message string
}

func (e *mcpDiagnosticError) Error() string { return e.message }
func (e *mcpDiagnosticError) Unwrap() error { return e.cause }
