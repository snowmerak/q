package studioprocess

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"

	"github.com/snowmerak/q/worklock"
)

func accessFixture(t *testing.T, secure bool) (Endpoint, *httptest.Server) {
	t.Helper()
	server := httptest.NewUnstartedServer(nil)
	e := Endpoint{Protocol: Protocol, URL: "http://" + server.Listener.Addr().String(), Token: Secret(), Instance: Secret(), Secure: secure}
	server.Config.Handler = Access(e, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "studio") }))
	server.Start()
	t.Cleanup(server.Close)
	return e, server
}

func TestManagedBrowserAdmissionAndCSRF(t *testing.T) {
	e, server := accessFixture(t, true)
	if err := e.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	response, err := server.Client().Get(e.URL + "/api/v1/settings")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous status: %d", response.StatusCode)
	}
	launch, err := e.BrowserURL(t.Context(), "/sessions?test=1")
	if err != nil {
		t.Fatal(err)
	}
	jar, _ := cookiejar.New(nil)
	client := server.Client()
	client.Jar = jar
	response, err = client.Get(launch)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Request.URL.RequestURI() != "/sessions?test=1" {
		t.Fatalf("browser admission: %d %s", response.StatusCode, response.Request.URL)
	}
	response, err = client.Get(launch)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatal("browser admission was replayed")
	}
	for _, origin := range []string{"", "https://attacker.invalid", e.URL} {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, e.URL+"/api/v1/chats", nil)
		req.Header.Set("Origin", origin)
		response, err = client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		want := http.StatusForbidden
		if origin == e.URL {
			want = http.StatusOK
		}
		if response.StatusCode != want {
			t.Errorf("origin %q: %d want %d", origin, response.StatusCode, want)
		}
	}
	response, err = client.Get(e.URL + ControlPath + "status")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatal("browser cookie authorized engine control")
	}
}

func TestWebCompatibilityAndLocalRedirects(t *testing.T) {
	e, server := accessFixture(t, false)
	response, err := server.Client().Get(e.URL + "/api/v1/status")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatal("ordinary web requires desktop credentials")
	}
	for _, path := range []string{"//attacker.invalid/", "https://attacker.invalid/", "/\\attacker.invalid"} {
		e.Secure = true
		if _, err = e.BrowserURL(t.Context(), path); err == nil {
			t.Errorf("accepted redirect %q", path)
		}
	}
}

func TestDiscoveryRequiresLiveVerifiedOwner(t *testing.T) {
	directory := t.TempDir()
	lock, err := Acquire(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	if _, err := Acquire(directory); !errors.Is(err, worklock.ErrLocked) {
		t.Fatalf("duplicate owner: %v", err)
	}
	e, _ := accessFixture(t, true)
	if err = Publish(directory, e); err != nil {
		t.Fatal(err)
	}
	actual, err := Discover(t.Context(), directory)
	if err != nil {
		t.Fatal(err)
	}
	if actual.Instance != e.Instance || actual.Owned {
		t.Fatalf("discovered %#v", actual)
	}
	Remove(directory)
	if err = lock.Close(); err != nil {
		t.Fatal(err)
	}
	lock, err = Acquire(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = Discover(ctx, directory); err == nil {
		t.Fatal("discovered missing runtime")
	}
}

func TestControlNeverReturnsBearer(t *testing.T) {
	e, _ := accessFixture(t, true)
	response, err := e.Request(t.Context(), http.MethodGet, ControlPath+"status")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	var status map[string]any
	if err = json.NewDecoder(response.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if _, ok := status["token"]; ok {
		t.Fatal("status leaks credential")
	}
}
