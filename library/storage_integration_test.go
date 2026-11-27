package library_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/snowmerak/q/library"
	"github.com/snowmerak/q/opensearchindex"
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/storageconfig"
	"github.com/snowmerak/q/workitems"
	"github.com/snowmerak/q/workspacememory"
)

func TestStorageRemoteIntegration(t *testing.T) {
	pgDSN := os.Getenv("Q_TEST_POSTGRES_DSN")
	osURL := os.Getenv("Q_TEST_OPENSEARCH_URL")
	if pgDSN == "" || osURL == "" {
		t.Skip("set Q_TEST_POSTGRES_DSN and Q_TEST_OPENSEARCH_URL to run remote storage e2e integration")
	}
	ctx := t.Context()
	prefix := fmt.Sprintf("qe2e-%d", time.Now().UTC().UnixNano())
	osClient, err := opensearchindex.NewClient(opensearchindex.Config{
		Endpoint:    osURL,
		IndexPrefix: prefix,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(cleanupCtx, http.MethodDelete, osURL+"/"+prefix+"-*", nil)
		if err == nil {
			resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
			if resp != nil {
				_ = resp.Body.Close()
			}
			_ = err
		}
		_ = osClient.Close()
	})

	dir := t.TempDir()
	libPort := freeStorageTestPort(t)
	wmPort := freeStorageTestPort(t)
	if err := (library.ConfigStore{Dir: dir}).Save(library.Config{
		Version: library.ConfigVersion,
		Host:    "127.0.0.1",
		Port:    libPort,
	}); err != nil {
		t.Fatal(err)
	}
	if err := (workspacememory.ConfigStore{Dir: dir}).Save(workspacememory.Config{
		Version: workspacememory.ConfigVersion,
		Host:    "127.0.0.1",
		Port:    wmPort,
	}); err != nil {
		t.Fatal(err)
	}
	if err := (storageconfig.Store{Dir: dir}).Save(storageconfig.Config{
		Mode: storageconfig.ModeRemote,
		Remote: storageconfig.RemoteConfig{
			Postgres: storageconfig.PostgresConfig{
				Enabled: true,
				DSN:     pgDSN,
			},
			OpenSearch: storageconfig.OpenSearchConfig{
				Enabled:     true,
				Endpoint:    osURL,
				IndexPrefix: prefix,
				Refresh:     "wait_for",
				Targets: storageconfig.OpenSearchTargets{
					Skills:          true,
					Knowledge:       true,
					WorkspaceMemory: true,
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	libRT, err := library.Ensure(ctx, dir)
	if err != nil {
		t.Fatalf("library.Ensure: %v", err)
	}
	defer func() { _ = libRT.Close() }()

	ws, err := libRT.Client().CreateWorkspace(ctx, workitems.WorkspaceInput{
		Name:           "e2e-remote-" + prefix,
		WorkspaceRoots: []string{t.TempDir()},
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	defer func() {
		_ = libRT.Client().ArchiveWorkspace(context.Background(), ws.ID)
	}()

	issue, err := libRT.Client().CreateItem(ctx, workitems.CreateInput{
		WorkspaceID: ws.ID,
		Kind:        workitems.Issue,
		Title:       "E2E Remote Storage Verification",
		Content:     "PostgreSQL workitems paired with OpenSearch knowledge search via storage.json.",
		Author:      "e2e-test",
	})
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}

	workHits, err := libRT.Client().SearchWork(ctx, library.WorkSearchRequest{
		WorkspaceID: ws.ID,
		Query:       "storage.json PostgreSQL",
		Mode:        "lexical",
	})
	if err != nil || len(workHits.Hits) != 1 || workHits.Hits[0].ItemID != issue.ID {
		t.Fatalf("SearchWork = %#v, err = %v", workHits, err)
	}
	skillHits, err := libRT.Client().SearchSkills(ctx, library.SkillSearchRequest{Query: "cdp"})
	if err != nil || len(skillHits.Hits) == 0 {
		t.Fatalf("SearchSkills = %#v, err = %v", skillHits, err)
	}

	wmRT, err := workspacememory.Ensure(ctx, dir)
	if err != nil {
		t.Fatalf("workspacememory.Ensure: %v", err)
	}
	defer func() { _ = wmRT.Close() }()

	wmWS, err := wmRT.Client().OpenWorkspace(ctx, filepath.Clean(t.TempDir()), sessionstore.VectorConfig{})
	if err != nil {
		t.Fatalf("OpenWorkspace: %v", err)
	}
	saved, err := wmWS.Save(sessionstore.Record{
		Kind:    sessionstore.KindMessage,
		Content: "Remote archive message persisted via storage.json",
	})
	if err != nil {
		t.Fatalf("wmWS.Save: %v", err)
	}
	wmSearch, err := wmWS.Search(ctx, sessionstore.SearchOptions{Text: "storage.json"})
	if err != nil || len(wmSearch.Hits) != 1 || wmSearch.Hits[0].Record.ID != saved.ID {
		t.Fatalf("wmWS.Search = %#v, err = %v", wmSearch, err)
	}
	if err := wmWS.Close(); err != nil {
		t.Fatal(err)
	}
}

func freeStorageTestPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}
