package task

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/snowmerak/q/v2/pkg/app/config"
	"github.com/snowmerak/q/v2/pkg/domain"
)

type MCPStatus struct {
	ID    string `json:"id"`
	Tools int    `json:"tools"`
	Error string `json:"error,omitempty"`
}

// MCPManager connects configured external stdio/HTTP MCP servers and surfaces
// their tools as dynamic `mcp_<server>_<tool>` ToolSpecs.
type MCPManager struct {
	mu       sync.RWMutex
	sessions map[string]*mcp.ClientSession
	tools    []ToolSpec
}

func NewMCPManager() *MCPManager {
	return &MCPManager{
		sessions: make(map[string]*mcp.ClientSession),
	}
}

func (m *MCPManager) Configure(ctx context.Context, servers []config.MCPServerConfig) []MCPStatus {
	if m == nil {
		return nil
	}
	nextSessions := make(map[string]*mcp.ClientSession)
	var nextTools []ToolSpec
	var statuses []MCPStatus

	for _, srv := range servers {
		if !srv.Enabled {
			continue
		}
		st := MCPStatus{ID: srv.ID}
		sess, tools, err := connectMCPServer(ctx, srv)
		if err != nil {
			st.Error = err.Error()
			statuses = append(statuses, st)
			continue
		}
		nextSessions[srv.ID] = sess
		nextTools = append(nextTools, tools...)
		st.Tools = len(tools)
		statuses = append(statuses, st)
	}

	m.mu.Lock()
	prev := m.sessions
	m.sessions = nextSessions
	m.tools = nextTools
	m.mu.Unlock()

	for _, s := range prev {
		_ = s.Close()
	}
	return statuses
}

func (m *MCPManager) Tools() []ToolSpec {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	out := append([]ToolSpec(nil), m.tools...)
	m.mu.RUnlock()
	return out
}

func (m *MCPManager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	prev := m.sessions
	m.sessions = nil
	m.tools = nil
	m.mu.Unlock()
	for _, s := range prev {
		_ = s.Close()
	}
}

func connectMCPServer(ctx context.Context, srv config.MCPServerConfig) (*mcp.ClientSession, []ToolSpec, error) {
	connCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	client := mcp.NewClient(&mcp.Implementation{Name: "q-v2", Version: "2.0.0"}, nil)
	var transport mcp.Transport
	switch srv.Transport {
	case "stdio":
		cmd := exec.Command(srv.Command, srv.Args...)
		if len(srv.Env) > 0 {
			env := os.Environ()
			for k, v := range srv.Env {
				env = append(env, k+"="+os.ExpandEnv(v))
			}
			cmd.Env = env
		}
		transport = &mcp.CommandTransport{Command: cmd}
	case "http":
		transport = &mcp.StreamableClientTransport{
			Endpoint:   srv.URL,
			HTTPClient: &http.Client{Timeout: 60 * time.Second},
		}
	default:
		return nil, nil, fmt.Errorf("unsupported MCP transport %q", srv.Transport)
	}

	sess, err := client.Connect(connCtx, transport, nil)
	if err != nil {
		return nil, nil, err
	}
	res, err := sess.ListTools(connCtx, &mcp.ListToolsParams{})
	if err != nil {
		_ = sess.Close()
		return nil, nil, err
	}

	specs := make([]ToolSpec, 0, len(res.Tools))
	for _, tool := range res.Tools {
		toolName := tool.Name
		exposed := fmt.Sprintf("mcp_%s_%s", sanitizeToolSlug(srv.ID), sanitizeToolSlug(toolName))
		paramsMap, _ := tool.InputSchema.(map[string]any)
		if paramsMap == nil {
			paramsMap = map[string]any{"type": "object"}
		}
		specs = append(specs, ToolSpec{
			Name:        exposed,
			Description: tool.Description,
			Source:      domain.ToolSourceMCP,
			Risk:        RiskExternal,
			Parameters:  paramsMap,
			Handler: func(ctx context.Context, _ CallContext, argsJSON string) (ToolOutput, error) {
				var parsed map[string]any
				if strings.TrimSpace(argsJSON) != "" {
					if err := json.Unmarshal([]byte(argsJSON), &parsed); err != nil {
						return ToolOutput{Content: "invalid MCP tool args: " + err.Error(), IsError: true}, nil
					}
				}
				callCtx, cancelCall := context.WithTimeout(ctx, 60*time.Second)
				defer cancelCall()
				res, err := sess.CallTool(callCtx, &mcp.CallToolParams{
					Name:      toolName,
					Arguments: parsed,
				})
				if err != nil {
					return ToolOutput{Content: "MCP tool call failed: " + err.Error(), IsError: true}, nil
				}
				return ToolOutput{
					Content: extractMCPResultText(res),
					IsError: res.IsError,
				}, nil
			},
		})
	}
	return sess, specs, nil
}

func extractMCPResultText(res *mcp.CallToolResult) string {
	if res == nil {
		return ""
	}
	var parts []string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok && tc.Text != "" {
			parts = append(parts, tc.Text)
		} else {
			if b, err := json.Marshal(c); err == nil {
				parts = append(parts, string(b))
			}
		}
	}
	if len(parts) == 0 && res.StructuredContent != nil {
		if b, err := json.MarshalIndent(res.StructuredContent, "", "  "); err == nil {
			parts = append(parts, string(b))
		}
	}
	return strings.Join(parts, "\n")
}

func sanitizeToolSlug(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			b.WriteRune(unicode.ToLower(r))
		} else {
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

// BrowserRunner executes a resolved browser driver command and args.
type BrowserRunner func(ctx context.Context, command string, args []string) (string, error)

// BrowserController resolves cdp-q-driver or ego-browser from config.Tools.BrowserControl.
type BrowserController struct {
	configFn func() config.BrowserControlConfig
	runner   BrowserRunner
}

func NewBrowserController(configFn func() config.BrowserControlConfig, runner BrowserRunner) *BrowserController {
	if runner == nil {
		runner = defaultBrowserRunner
	}
	return &BrowserController{
		configFn: configFn,
		runner:   runner,
	}
}

func (b *BrowserController) ResolveCommand(extraArgs []string) (command string, args []string, preferred string, err error) {
	if b == nil || b.configFn == nil {
		return "", nil, "", errors.New("browser_control configuration unavailable")
	}
	cfg := b.configFn()
	if !cfg.Enabled {
		return "", nil, "", errors.New("browser_control is disabled in config")
	}
	preferred = strings.TrimSpace(cfg.Preferred)
	if preferred == "" {
		preferred = "cdp-q-driver"
	}
	driverCfg := cfg.Drivers[preferred]
	command = strings.TrimSpace(driverCfg.Command)
	baseArgs := append([]string(nil), driverCfg.Args...)

	switch preferred {
	case "cdp-q-driver":
		if command == "" {
			exe, exeErr := os.Executable()
			if exeErr != nil {
				command = "cdp-q-driver"
			} else {
				command = exe
				if len(baseArgs) == 0 {
					baseArgs = []string{"cdp"}
				}
			}
		}
	case "ego-browser":
		if command == "" {
			command = "ego-browser"
		}
	default:
		return "", nil, preferred, fmt.Errorf("unsupported browser_control driver %q", preferred)
	}
	args = append(baseArgs, extraArgs...)
	return command, args, preferred, nil
}

func defaultBrowserRunner(ctx context.Context, command string, args []string) (string, error) {
	runCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, command, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	if err != nil {
		return buf.String(), fmt.Errorf("%w: %s", err, strings.TrimSpace(buf.String()))
	}
	return buf.String(), nil
}

func registerBrowserTool(r *Registry, ctrl *BrowserController) {
	r.register(ToolSpec{
		Name:        "browser_run",
		Description: "Run a browser automation command using the active browser_control driver (cdp-q-driver or ego-browser).",
		Source:      domain.ToolSourceBrowser,
		Risk:        RiskExternal,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"args": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Subcommand arguments passed to the preferred browser driver.",
				},
			},
			"required": []string{"args"},
		},
		Handler: func(ctx context.Context, _ CallContext, argsJSON string) (ToolOutput, error) {
			if ctrl == nil {
				return ToolOutput{Content: "browser controller unavailable", IsError: true}, nil
			}
			var args struct {
				Args []string `json:"args"`
			}
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
				return ToolOutput{Content: "invalid args: " + err.Error(), IsError: true}, nil
			}
			cmd, fullArgs, driverName, err := ctrl.ResolveCommand(args.Args)
			if err != nil {
				return ToolOutput{Content: err.Error(), IsError: true}, nil
			}
			out, runErr := ctrl.runner(ctx, cmd, fullArgs)
			if runErr != nil {
				return ToolOutput{Content: fmt.Sprintf("browser driver (%s) failed: %v\n%s", driverName, runErr, out), IsError: true}, nil
			}
			return ToolOutput{Content: out}, nil
		},
	})
}
