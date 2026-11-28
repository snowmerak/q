package qdriver_test

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"

	"github.com/snowmerak/q/v2/pkg/cdp/cli"
)

func TestMain(m *testing.M) {
	if len(os.Args) >= 2 && os.Args[1] == "cdp" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		code := cli.MainWithOptions(ctx, os.Args[2:], os.Stdin, os.Stdout, os.Stderr, cli.Options{
			CommandPrefix: []string{"cdp"},
		})
		stop()
		os.Exit(code)
	}
	os.Exit(m.Run())
}

func TestCLIEmbeddedSubcommandWorkflow(t *testing.T) {
	chrome := startChrome(t)
	binary, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	f := namedCLI(t, binary, t.TempDir(), "embedded")
	f.prefix = []string{"cdp"}
	f.ok(t, "--endpoint", chrome.endpoint, "page", "new", "--name", "main", "data:text/html,<title>Embedded CLI</title><input id=name>")
	f.ok(t, "page", "fill", "--css", "#name", "main", "embedded")
	if got := string(f.ok(t, "page", "eval", "main", "document.querySelector('input').value")); got != `"embedded"` {
		t.Fatalf("embedded input/session reuse failed: %s", got)
	}
	if _, err := exec.LookPath("node"); err == nil {
		got := string(f.ok(t, "run", scriptFile(t, `return await session.page('main').title();`)))
		if !strings.Contains(got, "Embedded CLI") {
			t.Fatalf("embedded JavaScript run failed: %s", got)
		}
	}
	f.ok(t, "stop")
	if _, err := os.Stat(f.state); !os.IsNotExist(err) {
		t.Fatalf("embedded stop retained runtime: %v", err)
	}
	// Restart from saved settings must also go through `q cdp serve`.
	f.ok(t, "session", "ensure")
	f.ok(t, "page", "new", "--name", "again")
	f.ok(t, "stop")
}
