package studio

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/snowmerak/q/config"
)

func TestLoggingSettingsTransferRoundTrip(t *testing.T) {
	source := transferTestStore(t)
	value, err := source.Load()
	if err != nil {
		t.Fatal(err)
	}
	on, off := true, false
	value.Logging = config.LoggingConfig{Level: "debug", Stdout: &on, FileEnabled: &off, MaxFileMiB: 12, MaxTotalBytes: 100_000_000, MaxAgeDays: 3}
	if err := source.Save(value); err != nil {
		t.Fatal(err)
	}
	handler, err := newHandler(source)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/settings/transfer", nil))
	if response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	var bundle settingsBundle
	if err := json.Unmarshal(response.Body.Bytes(), &bundle); err != nil {
		t.Fatal(err)
	}
	logging, ok := bundle.Sections["runtime"]["logging"]
	if !ok {
		t.Fatal("logging missing from export")
	}
	target := transferTestStore(t)
	handler, err = newHandler(target)
	if err != nil {
		t.Fatal(err)
	}
	selected := settingsBundle{Format: bundle.Format, Version: bundle.Version, Scope: bundle.Scope, Sections: map[string]map[string]json.RawMessage{"runtime": {"logging": logging}}}
	response = transferRequest(t, handler, "/api/v1/settings/transfer/import", selected)
	if response.Code != http.StatusOK {
		t.Fatalf("import=%d %s", response.Code, response.Body.String())
	}
	loaded, err := target.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.Logging, value.Logging) {
		t.Fatalf("imported=%+v wanted=%+v", loaded.Logging, value.Logging)
	}
}
