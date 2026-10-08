package council_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/q/app"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/council"
	"github.com/snowmerak/q/third_party/acp-go-sdk"
)

type councilACPAgent struct {
	acp.Agent
	connection *acp.AgentSideConnection
	logDir     string
}

type councilACPCall struct {
	Session string `json:"session"`
	Prompt  string `json:"prompt"`
}

func (a *councilACPAgent) Initialize(context.Context, acp.InitializeRequest) (acp.InitializeResponse, error) {
	return acp.InitializeResponse{ProtocolVersion: acp.ProtocolVersionNumber, AuthMethods: []acp.AuthMethod{}}, nil
}

func (a *councilACPAgent) NewSession(context.Context, acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	return acp.NewSessionResponse{SessionId: acp.SessionId(fmt.Sprintf("session-%d", os.Getpid()))}, nil
}

func (a *councilACPAgent) Cancel(context.Context, acp.CancelNotification) error { return nil }

func (a *councilACPAgent) Prompt(ctx context.Context, request acp.PromptRequest) (acp.PromptResponse, error) {
	text := request.Prompt[0].Text.Text
	body, err := json.Marshal(councilACPCall{Session: string(request.SessionId), Prompt: text})
	if err != nil {
		return acp.PromptResponse{}, err
	}
	file, err := os.OpenFile(filepath.Join(a.logDir, fmt.Sprintf("%d.jsonl", os.Getpid())), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return acp.PromptResponse{}, err
	}
	_, err = file.Write(append(body, '\n'))
	file.Close()
	if err != nil {
		return acp.PromptResponse{}, err
	}
	if strings.Contains(text, "cancel-test") {
		<-ctx.Done()
		return acp.PromptResponse{}, ctx.Err()
	}
	if strings.Contains(text, "continuing a multi-round LLM council") {
		file, err := os.OpenFile(filepath.Join(a.logDir, "failed-once"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			file.Close()
			return acp.PromptResponse{}, errors.New("temporary ACP review failure")
		}
	}
	for _, kind := range []acp.ToolKind{acp.ToolKindRead, acp.ToolKindEdit, acp.ToolKindExecute} {
		permission, err := a.connection.RequestPermission(ctx, acp.RequestPermissionRequest{
			SessionId: request.SessionId, ToolCall: acp.ToolCallUpdate{ToolCallId: "permission", Kind: &kind},
			Options: []acp.PermissionOption{{OptionId: "allow", Kind: acp.PermissionOptionKindAllowOnce}},
		})
		if err != nil {
			return acp.PromptResponse{}, err
		}
		if (kind == acp.ToolKindRead) != (permission.Outcome.Selected != nil) {
			return acp.PromptResponse{}, errors.New("unexpected council permission policy")
		}
	}
	answer := "ACP independent opinion"
	if strings.Contains(text, "All council rounds:") {
		answer = "ACP conclusion"
	} else if strings.Contains(text, "Peer answers:") || strings.Contains(text, "continuing a multi-round") {
		answer = "ACP review"
	}
	err = a.connection.SessionUpdate(ctx, acp.SessionNotification{SessionId: request.SessionId, Update: acp.SessionUpdate{
		AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{Content: acp.TextBlock(answer)},
	}})
	return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, err
}

// A real stdio process exercises framing, initialization, permissions, streamed
// answers and process cleanup without requiring a vendor account or executable.
func TestCouncilACPProcess(t *testing.T) {
	logDir := os.Getenv("Q_COUNCIL_ACP_TEST_DIR")
	if logDir == "" {
		return
	}
	agent := &councilACPAgent{logDir: logDir}
	agent.connection = acp.NewAgentSideConnection(agent, os.Stdout, os.Stdin)
	<-agent.connection.Done()
	os.Exit(0)
}

func councilACPConnections(t *testing.T) (map[string]config.AgentConnectionConfig, string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	logDir := t.TempDir()
	return map[string]config.AgentConnectionConfig{"research": {
		Command: executable, Args: []string{"-test.run=^TestCouncilACPProcess$"}, Env: map[string]string{"Q_COUNCIL_ACP_TEST_DIR": logDir},
	}}, logDir
}

func TestMixedCouncilACPResumeUsesSavedRoundsAndACPChair(t *testing.T) {
	connections, logDir := councilACPConnections(t)
	value := testCouncil(council.Independent, "", "")
	value.Members[1] = council.Seat{Agent: "research"}
	value.Chair = council.Seat{Agent: "research"}
	value.Rounds = 3
	turn, err := council.NewTurn(value, "Assess design")
	if err != nil {
		t.Fatal(err)
	}
	model := &fakeModel{}
	root := t.TempDir()
	runner := app.NewParticipantRunner(t.Context(), sessionRunner{model}, connections)
	partial, err := council.Run(t.Context(), runner, value, nil, turn, nil, root, 2, func(council.Turn) error { return nil })
	if err == nil || partial.CurrentRound != 3 || partial.Rounds[1].Reviews[1].Text != "ACP review" {
		t.Fatalf("partial = %+v, %v", partial, err)
	}
	before := len(model.requests)
	runner = app.NewParticipantRunner(t.Context(), sessionRunner{model}, connections)
	result, err := council.Resume(t.Context(), runner, value, nil, partial, nil, root, 2, func(council.Turn) error { return nil })
	if err != nil || result.Status != "completed" || result.Final != "ACP conclusion" || len(model.requests) != before {
		t.Fatalf("resume = %+v, %v; native requests %d -> %d", result, err, before, len(model.requests))
	}
	files, err := filepath.Glob(filepath.Join(logDir, "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var calls []councilACPCall
	for _, file := range files {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
			var call councilACPCall
			if err := json.Unmarshal([]byte(line), &call); err != nil {
				t.Fatal(err)
			}
			calls = append(calls, call)
		}
	}
	if len(files) != 3 || len(calls) != 5 {
		t.Fatalf("expected original member, resumed member and separate chair: files=%v calls=%+v", files, calls)
	}
	restored := false
	for _, call := range calls {
		if strings.Contains(call.Prompt, "Completed conversation restored by Q") {
			restored = strings.Contains(call.Prompt, "ACP independent opinion") && strings.Contains(call.Prompt, "ACP review")
		}
	}
	if !restored {
		t.Fatal("ACP resume did not restore completed exchanges")
	}
	for _, request := range model.requests {
		if strings.Contains(councilUserPrompt(request), "research") {
			t.Fatal("ACP connection identity leaked to a native reviewer")
		}
	}
	if markdown := council.ExportMarkdown(value, []council.Turn{result}); !strings.Contains(markdown, "acp/research") || !strings.Contains(markdown, "ACP conclusion") {
		t.Fatalf("missing ACP export identity: %s", markdown)
	}
}

func TestCouncilACPCancellationStopsProcess(t *testing.T) {
	connections, _ := councilACPConnections(t)
	value := testCouncil(council.Independent, "", "")
	value.Members = []council.Seat{{Agent: "research"}, {Agent: "research"}}
	value.Rounds = 1
	turn, err := council.NewTurn(value, "cancel-test")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	start := time.Now()
	_, err = council.Run(ctx, app.NewParticipantRunner(ctx, nil, connections), value, nil, turn, nil, t.TempDir(), 2, func(council.Turn) error { return nil })
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 6*time.Second {
		t.Fatalf("ACP cancellation = %v, elapsed %s", err, time.Since(start))
	}
}

func TestACPSeatValidation(t *testing.T) {
	for _, seat := range []council.Seat{{Agent: "research", Model: "provider/one"}, {Agent: "research", ReasoningEffort: "high"}, {Agent: "bad/id"}, {}} {
		value := testCouncil(council.Independent, "", "")
		value.Members[0] = seat
		if err := council.Validate(value); err == nil {
			t.Fatalf("accepted seat %+v", seat)
		}
	}
}

func TestCouncilDistinctACPSeatsCanUseSameConnection(t *testing.T) {
	connections, _ := councilACPConnections(t)
	value := testCouncil(council.Independent, "", "")
	value.Members = []council.Seat{{Agent: "research"}, {Agent: "research"}}
	turn, err := council.NewTurn(value, "Assess design")
	if err != nil {
		t.Fatal(err)
	}
	runner := app.NewParticipantRunner(t.Context(), sessionRunner{&fakeModel{}}, connections)
	result, err := council.Run(t.Context(), runner, value, nil, turn, nil, t.TempDir(), 2, func(council.Turn) error { return nil })
	if err != nil || result.Status != "completed" || result.MemberSessions[0] == result.MemberSessions[1] {
		t.Fatalf("distinct ACP seats = %+v, %v", result, err)
	}
	for _, review := range result.Reviews {
		if review.Text != "ACP review" {
			t.Fatalf("ACP seat failed peer review: %+v", review)
		}
	}
}

type acpIdentityModel struct{ fakeModel }

func (m *acpIdentityModel) Chat(ctx context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
	response, err := m.fakeModel.Chat(ctx, request)
	if request.Model == "" && strings.Contains(request.Messages[0].Content, "independent") {
		response.Choices[0].Message.Content = "a factual claim from acp/a"
	}
	return response, err
}

func TestACPAnonymizationPreservesWordsMatchingShortConnectionIDs(t *testing.T) {
	value := testCouncil(council.Independent, "", "")
	value.Members[1] = council.Seat{Agent: "a"}
	turn, err := council.NewTurn(value, "Assess design")
	if err != nil {
		t.Fatal(err)
	}
	model := &acpIdentityModel{}
	_, err = council.Run(t.Context(), sessionRunner{model}, value, nil, turn, nil, t.TempDir(), 2, func(council.Turn) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range model.requests {
		if request.Model == "provider/one" && strings.Contains(request.Messages[0].Content, "anonymous peer reviewer") {
			prompt := councilUserPrompt(request)
			if !strings.Contains(prompt, "a factual claim") || strings.Contains(prompt, "acp/a") {
				t.Fatalf("anonymization damaged the answer or leaked identity: %s", prompt)
			}
			return
		}
	}
	t.Fatal("native reviewer was not invoked")
}
