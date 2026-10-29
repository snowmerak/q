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

	"github.com/modelcontextprotocol/go-sdk/mcp"
	qlibrary "github.com/snowmerak/q/library"
	"github.com/snowmerak/q/workitems"
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
	command := exec.CommandContext(ctx, binary, "library", "start")
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
	// Launch the actual bridge executable against this custom Library address.
	// Its stdout must contain only MCP frames, even though the Library service
	// itself reports status on a separate process's stdout.
	publicClient := qlibrary.NewClient(value.Endpoint(), "", 0)
	project, err := publicClient.CreateWorkspace(ctx, workitems.WorkspaceInput{Name: "Bridge project", WorkspaceRoots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := mcp.NewClient(&mcp.Implementation{Name: "q-test", Version: "0"}, nil).Connect(ctx, &mcp.CommandTransport{Command: exec.CommandContext(ctx, binary, "library", "mcp", "--config-dir", configDir, "--workspace-id", project.ID, "--session-id", project.ManagerSessionID, "--manager")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := bridge.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("Library MCP tools = %+v, %v", listed, err)
	}
	names := map[string]bool{}
	for _, tool := range listed.Tools {
		names[tool.Name] = true
	}
	for _, required := range []string{"issue_create", "issue_reply", "post_get", "ticket_create", "ticket_review", "ticket_change_read", "search_work"} {
		if !names[required] {
			t.Fatalf("MCP tool missing: %s", required)
		}
	}
	for _, retired := range []string{"learn", "request_learning", "search_propositions", "get_proposition"} {
		if names[retired] {
			t.Fatalf("retired MCP tool: %s", retired)
		}
	}
	result, err := bridge.CallTool(ctx, &mcp.CallToolParams{Name: "search_work", Arguments: map[string]any{"query": "", "mode": "lexical"}})
	if err != nil || result.IsError {
		t.Fatalf("Library MCP search = %+v, %v", result, err)
	}
	result, err = bridge.CallTool(ctx, &mcp.CallToolParams{Name: "issue_create", Arguments: map[string]any{
		"title": "Verified decision", "content": "The user confirmed this integration-test decision.", "refs": []string{"user:integration-test"}, "request_id": "source-once",
	}})
	if err != nil || result.IsError {
		t.Fatalf("Library MCP issue = %+v, %v", result, err)
	}
	ack, err := json.Marshal(result.StructuredContent)
	var item workitems.Item
	if err == nil {
		err = json.Unmarshal(ack, &item)
	}
	if err != nil || item.WorkspaceID != project.ID || item.ID == "" || item.Revision != 1 {
		t.Fatalf("Library MCP acknowledgement = %s, %v", ack, err)
	}
	member, err := publicClient.PutMember(ctx, project.ID, workitems.MemberInput{AgentID: "builtin/research", DefinitionRoot: project.WorkspaceRoots[0], ExecutionRoot: project.WorkspaceRoots[0]})
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := publicClient.CreateItem(ctx, workitems.CreateInput{WorkspaceID: project.ID, Kind: workitems.Ticket, Title: "Report", Content: "Inspect", Author: "user"})
	if err != nil {
		t.Fatal(err)
	}
	ticket, err = publicClient.ControlTicket(ctx, ticket.ID, workitems.TicketAction{Action: "queue", Revision: ticket.Revision})
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := publicClient.ClaimTicket(ctx, ticket.ID, workitems.ClaimInput{Revision: ticket.Revision, MemberID: member.ID, MemberRevision: member.Revision, DefinitionDigest: "fixture", Owner: "fixture", RequestID: "claim"})
	if err != nil {
		t.Fatal(err)
	}
	attempt, err = publicClient.UpdateAttempt(ctx, attempt.ID, workitems.AttemptUpdate{Owner: attempt.Owner, Token: attempt.Token, Generation: attempt.Generation, Status: "awaiting_review", Result: []byte(`{"outcome":"succeeded"}`)})
	if err != nil {
		t.Fatal(err)
	}
	result, err = bridge.CallTool(ctx, &mcp.CallToolParams{Name: "ticket_review", Arguments: map[string]any{"id": attempt.ID, "decision": "accept", "request_id": "manager-review"}})
	if err != nil || result.IsError {
		t.Fatalf("stdio manager review: %+v %v", result, err)
	}
	ticket, err = publicClient.GetItem(ctx, ticket.ID)
	if err != nil || ticket.State != "done" {
		t.Fatalf("review result: %+v %v", ticket, err)
	}
	if err := bridge.Close(); err != nil {
		t.Fatal(err)
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
