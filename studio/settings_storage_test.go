package studio

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/snowmerak/q/config"
)

func TestSettingsStorageUpdateAndSecretMasking(t *testing.T) {
	dir := t.TempDir()
	service := newSettingsService(config.Store{Dir: dir})

	// Initial snapshot defaults to local mode.
	rec := httptest.NewRecorder()
	service.serveSnapshot(rec, httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET settings = %d: %s", rec.Code, rec.Body.String())
	}
	var snap settingsSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	if snap.Storage.Mode != "local" {
		t.Fatalf("default storage mode = %q, want local", snap.Storage.Mode)
	}

	// Fake OpenSearch endpoint for test endpoint check.
	osServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" && r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"cluster_name":"test"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer osServer.Close()

	// Switch to remote with inline secrets; snapshot must mask raw secrets.
	updateBody, _ := json.Marshal(storageUpdateRequest{
		Mode: "remote",
		Remote: remoteStorageUpdate{
			OpenSearch: openSearchStorageUpdate{
				Enabled:     true,
				Endpoint:    osServer.URL,
				IndexPrefix: "custom",
				Refresh:     "wait_for",
				Username:    "admin",
				Password:    "super-secret-pw",
				BearerToken: "secret-bearer-token",
				Targets: openSearchTargetSettings{
					Skills:          true,
					Knowledge:       true,
					WorkspaceMemory: true,
				},
			},
			Postgres: postgresStorageUpdate{
				Enabled: true,
				DSN:     "postgres://user:pgsecret@127.0.0.1:5432/q",
			},
		},
	})
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/settings/storage", bytes.NewReader(updateBody))
	service.serveStorageUpdate(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT storage = %d: %s", rec.Code, rec.Body.String())
	}
	bodyText := rec.Body.String()
	for _, secret := range []string{"super-secret-pw", "secret-bearer-token", "pgsecret"} {
		if strings.Contains(bodyText, secret) {
			t.Fatalf("secret %q leaked in snapshot response", secret)
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	if snap.Storage.Mode != "remote" || !snap.Storage.Remote.OpenSearch.HasInlinePassword || !snap.Storage.Remote.OpenSearch.HasInlineBearerToken || !snap.Storage.Remote.Postgres.HasInlineDSN {
		t.Fatalf("unexpected updated storage snapshot: %#v", snap.Storage)
	}

	// Updating without re-sending inline password must preserve existing secret.
	updateBody2, _ := json.Marshal(storageUpdateRequest{
		Mode: "remote",
		Remote: remoteStorageUpdate{
			OpenSearch: openSearchStorageUpdate{
				Enabled:     true,
				Endpoint:    osServer.URL,
				IndexPrefix: "custom-2",
				Refresh:     "true",
				Targets: openSearchTargetSettings{
					Skills:          false,
					Knowledge:       true,
					WorkspaceMemory: true,
				},
			},
			Postgres: postgresStorageUpdate{
				Enabled:  false,
				ClearDSN: true,
			},
		},
	})
	rec = httptest.NewRecorder()
	service.serveStorageUpdate(rec, httptest.NewRequest(http.MethodPut, "/api/v1/settings/storage", bytes.NewReader(updateBody2)))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT storage(second) = %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	if !snap.Storage.Remote.OpenSearch.HasInlinePassword || snap.Storage.Remote.Postgres.HasInlineDSN {
		t.Fatalf("secret preservation/clearing mismatch: %#v", snap.Storage)
	}

	// Test OpenSearch connectivity using draft + stored password.
	testBody, _ := json.Marshal(storageTestRequest{
		Target: "opensearch",
		Remote: remoteStorageUpdate{
			OpenSearch: openSearchStorageUpdate{
				Enabled:  true,
				Endpoint: osServer.URL,
			},
		},
	})
	rec = httptest.NewRecorder()
	service.serveStorageTest(rec, httptest.NewRequest(http.MethodPost, "/api/v1/settings/storage/test", bytes.NewReader(testBody)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST storage test = %d: %s", rec.Code, rec.Body.String())
	}
	var testResp storageTestResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &testResp); err != nil {
		t.Fatal(err)
	}
	if !testResp.OK || testResp.Target != "opensearch" {
		t.Fatalf("unexpected OpenSearch test response: %#v", testResp)
	}
}
