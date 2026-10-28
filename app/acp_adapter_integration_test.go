package app

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/snowmerak/q/internal/acpadapter"
)

func TestNPMACPAdaptersAutoInstallAndInitialize(t *testing.T) {
	if os.Getenv("Q_TEST_ACP_ADAPTER_INSTALL") != "1" {
		t.Skip("set Q_TEST_ACP_ADAPTER_INSTALL=1 to install and initialize published npm adapters without sending a prompt")
	}
	t.Setenv("LOCALAPPDATA", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	manager, err := acpadapter.DefaultManager()
	if err != nil {
		t.Fatal(err)
	}
	for _, preset := range []string{"codex", "claude"} {
		t.Run(preset, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
			defer cancel()
			command, err := resolveACPAgentCommand(preset, exec.LookPath)
			if err != nil || command.installPreset != preset {
				t.Fatalf("automatic installation command = %+v, %v", command, err)
			}
			remote, err := startACPRemoteClient(ctx, command, t.TempDir(), "", "", io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			if err := remote.Close(); err != nil {
				t.Fatal(err)
			}
			status := manager.Status(preset, exec.LookPath)
			command, err = resolveACPAgentCommand(preset, exec.LookPath)
			if err != nil || !status.Installed || command.installPreset != "" {
				t.Fatalf("installed adapter was not reused: %+v, %+v, %v", status, command, err)
			}
			t.Logf("installed %s %s; initialized and created an ACP session", status.Name, status.Version)
		})
	}
}

func TestACPAutomaticInstallFailureAndCancellation(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	command := acpAgentCommand{display: "Codex", installPreset: "codex"}
	if _, err := startACPRemoteClient(t.Context(), command, t.TempDir(), "", "", io.Discard); err == nil {
		t.Fatal("started without npm")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := startACPRemoteClient(ctx, command, t.TempDir(), "", "", io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatalf("automatic installation cancellation = %v", err)
	}
}
