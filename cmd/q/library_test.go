package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	qlibrary "github.com/snowmerak/q/library"
)

func TestLibraryCommandServesHealth(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping q binary integration test in short mode")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	configDir := filepath.Join(home, ".q")
	value := qlibrary.Config{
		Version: qlibrary.ConfigVersion, Host: "127.0.0.1", Port: port,
		APIKeyEnv: qlibrary.DefaultAPIKeyEnv,
	}
	if err := (qlibrary.ConfigStore{Dir: configDir}).Save(value); err != nil {
		t.Fatal(err)
	}
	binaryName := "q-test"
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}
	binary := filepath.Join(t.TempDir(), binaryName)
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = "."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build q: %v\n%s", err, output)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "library")
	command.Env = environmentWithHome(os.Environ(), home)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	defer func() {
		if !stopped && command.Process != nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()
	lineResult := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		lineResult <- line
	}()

	endpoint := value.Endpoint() + "/health"
	var health qlibrary.Health
	deadline := time.Now().Add(10 * time.Second)
	for {
		response, requestErr := http.Get(endpoint)
		if requestErr == nil {
			decodeErr := json.NewDecoder(response.Body).Decode(&health)
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK && decodeErr == nil && health.Compatible() {
				break
			}
		}
		if time.Now().After(deadline) {
			_ = command.Process.Kill()
			_ = command.Wait()
			stopped = true
			t.Fatalf("q library did not become ready: %s", stderr.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	if health.StoreID == "" || health.Generation == "" {
		t.Fatalf("health = %#v", health)
	}
	var output string
	select {
	case output = <-lineResult:
	case <-time.After(2 * time.Second):
		t.Fatal("q library did not report its endpoint")
	}

	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("killed q library unexpectedly exited successfully")
	}
	stopped = true
	if !strings.Contains(output, "q library listening on "+value.Endpoint()) {
		t.Fatalf("stdout = %q", output)
	}
}

func environmentWithHome(environment []string, home string) []string {
	result := make([]string, 0, len(environment)+2)
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(name, "HOME") || strings.EqualFold(name, "USERPROFILE") {
			continue
		}
		result = append(result, entry)
	}
	return append(result, "HOME="+home, "USERPROFILE="+home)
}
