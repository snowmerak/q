package cli_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/snowmerak/q/v2/pkg/cdp/cli"
)

func TestPublicCLIEntryPoints(t *testing.T) {
	var out, stderr bytes.Buffer
	code := cli.Main(context.Background(), []string{"help"}, strings.NewReader(""), &out, &stderr)
	if code != 0 || !strings.Contains(out.String(), "run [--node PATH]") || stderr.Len() != 0 {
		t.Fatalf("public standalone CLI: code=%d stdout=%q stderr=%q", code, out.String(), stderr.String())
	}
	out.Reset()
	code = cli.MainWithOptions(t.Context(), []string{"--invalid-flag"}, strings.NewReader(""), &out, &stderr, cli.Options{CommandPrefix: []string{"cdp"}})
	if code != 2 || out.Len() != 0 || !strings.Contains(stderr.String(), `"code":"invalid_argument"`) {
		t.Fatalf("public embedded CLI: code=%d stdout=%q stderr=%q", code, out.String(), stderr.String())
	}
}
