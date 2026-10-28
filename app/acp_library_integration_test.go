package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/snowmerak/q/library"
)

func TestNPMACPLibraryLookupIntegration(t *testing.T) {
	binary := os.Getenv("Q_TEST_ACP_LIBRARY_EXECUTABLE")
	if binary == "" {
		t.Skip("set Q_TEST_ACP_LIBRARY_EXECUTABLE to a built q executable to send real adapter prompts against a synthetic Library")
	}
	for _, preset := range []string{"codex", "claude"} {
		t.Run(preset, func(t *testing.T) {
			command, err := resolveACPAgentCommand(preset, exec.LookPath)
			if err != nil {
				t.Fatal(err)
			}
			if command.installPreset != "" {
				t.Skip("adapter is not installed")
			}
			var calls atomic.Int32
			const marker = "Q_LIBRARY_LOOKUP_7d6b3"
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Method != http.MethodPost || request.URL.Path != "/v1/skills/search" {
					http.NotFound(writer, request)
					return
				}
				calls.Add(1)
				writer.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(writer).Encode(library.SkillSearchResponse{Total: 1, Hits: []library.SkillSearchHit{{ID: "global/integration", Title: "Integration skill", Scope: "global", Description: marker}}})
			}))
			defer server.Close()
			address, _ := url.Parse(server.URL)
			port, _ := strconv.Atoi(address.Port())
			dir := t.TempDir()
			if err := (library.ConfigStore{Dir: dir}).Save(library.Config{Host: "127.0.0.1", Port: port}); err != nil {
				t.Fatal(err)
			}
			command.mcpServers, err = acpLibraryServers(dir)
			if err != nil {
				t.Fatal(err)
			}
			command.mcpServers[0].Stdio.Command = binary
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
			defer cancel()
			remote, err := startACPRemoteClient(ctx, command, t.TempDir(), "", "", io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := disposeACPRemote(remote); err != nil {
					t.Error(err)
				}
			}()
			// Claude requests permission even for read-only MCP tools. This
			// synthetic server exposes no user data or mutation, so the test can
			// approve the lookup without an interactive Q session.
			remote.permissions = acpPermissionAutomatic
			for _, effort := range remote.modelSettings().ReasoningEfforts {
				if effort.ID == "low" {
					if err := remote.setReasoningEffort(ctx, "low"); err != nil {
						t.Fatal(err)
					}
					break
				}
			}
			answer, err := remote.promptText(ctx, "Use the q-library MCP search_skills tool with query 'library integration'. Reply with only the description of its first result. Do not read files, execute commands, or call other tools.")
			if err != nil {
				t.Fatal(err)
			}
			if calls.Load() == 0 || !strings.Contains(answer, marker) {
				t.Fatalf("Library calls=%d, answer=%q", calls.Load(), answer)
			}
			t.Logf("%s invoked the built Q Library bridge and received its synthetic search result", preset)
		})
	}
}
