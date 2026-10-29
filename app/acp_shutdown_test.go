package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/q/third_party/acp-go-sdk"
)

func TestACPRemoteCleanupAfterParentInterrupt(t *testing.T) {
	for _, dispose := range []bool{false, true} {
		name := "close"
		if dispose {
			name = "dispose"
		}
		t.Run(name, func(t *testing.T) {
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			parent, interrupt := context.WithCancel(t.Context())
			defer interrupt()
			remote, err := startACPRemoteClient(parent, acpAgentCommand{
				name: executable, display: "shutdown peer",
				args: []string{"-test.run=^TestACPShutdownPeerProcess$", "--", "q-acp-shutdown-peer"},
			}, t.TempDir(), "", "", io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = remote.Close() })
			// Wait for the real child and its stdio to finish before cleanup, as
			// happens when Ctrl+C cancels exec.CommandContext first.
			interrupt()
			select {
			case processErr := <-remote.processDone:
				var exitErr *exec.ExitError
				if processErr != nil && !errors.As(processErr, &exitErr) && !errors.Is(processErr, context.Canceled) {
					t.Fatalf("unexpected process wait failure: %v", processErr)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("interrupted ACP process did not exit")
			}
			if _, err := remote.promptText(parent, "interrupted request"); !errors.Is(err, context.Canceled) {
				t.Fatalf("interrupted request wrote to closed stdio: %v", err)
			}
			if dispose {
				err = disposeACPRemote(remote)
			} else {
				err = remote.Close()
			}
			if err != nil {
				t.Fatalf("interrupted cleanup wrote to closed stdio: %v", err)
			}
			if err := remote.Close(); err != nil {
				t.Fatalf("repeated cleanup changed the result: %v", err)
			}
		})
	}
}

func TestACPRemoteDisposeDoesNotRetryFailedClose(t *testing.T) {
	closeErr := errors.New("live peer rejected session close")
	connection := &fakeACPRemoteConnection{deleteErr: errors.New("delete unavailable"), closeErr: closeErr}
	remote := &acpRemoteClient{connection: connection, sessionID: "session", capabilities: acp.AgentCapabilities{SessionCapabilities: acp.SessionCapabilities{
		Close: &acp.SessionCloseCapabilities{}, Delete: &acp.SessionDeleteCapabilities{},
	}}}
	if err := disposeACPRemote(remote); !errors.Is(err, closeErr) {
		t.Fatalf("lost live-peer close failure: %v", err)
	}
	if len(connection.deleted) != 1 || len(connection.closed) != 1 {
		t.Fatalf("disposal repeated a failed RPC: delete=%v close=%v", connection.deleted, connection.closed)
	}
}

func TestACPRemoteLiveCloseErrorIsPreserved(t *testing.T) {
	closeErr := errors.New("live peer rejected session close")
	connection := &fakeACPRemoteConnection{closeErr: closeErr}
	remote := &acpRemoteClient{connection: connection, sessionID: "session", capabilities: acp.AgentCapabilities{SessionCapabilities: acp.SessionCapabilities{Close: &acp.SessionCloseCapabilities{}}}}
	for range 2 {
		if err := remote.Close(); !errors.Is(err, closeErr) {
			t.Fatalf("lost live-peer close failure: %v", err)
		}
	}
	if len(connection.closed) != 1 {
		t.Fatalf("close was not idempotent: %v", connection.closed)
	}
}

type interruptingACPClose struct {
	*fakeACPRemoteConnection
	interrupt context.CancelFunc
}

func (c *interruptingACPClose) CloseSession(ctx context.Context, request acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	c.interrupt()
	return c.fakeACPRemoteConnection.CloseSession(ctx, request)
}

func TestACPRemoteInterruptRacingClose(t *testing.T) {
	process, interrupt := context.WithCancel(t.Context())
	defer interrupt()
	connection := &interruptingACPClose{fakeACPRemoteConnection: &fakeACPRemoteConnection{closeErr: acp.NewInternalError(map[string]any{"error": "write |1: file already closed"})}, interrupt: interrupt}
	remote := &acpRemoteClient{connection: connection, sessionID: "session", processContext: process, capabilities: acp.AgentCapabilities{SessionCapabilities: acp.SessionCapabilities{Close: &acp.SessionCloseCapabilities{}}}}
	if err := remote.Close(); err != nil {
		t.Fatalf("process interruption raced shutdown RPC: %v", err)
	}
}

func TestACPRemoteTurnCancellationKeepsTransport(t *testing.T) {
	connection := &fakeACPRemoteConnection{}
	closed := false
	remote := &acpRemoteClient{connection: connection, sessionID: "session", processContext: t.Context(), cancel: func() { closed = true }, capabilities: acp.AgentCapabilities{SessionCapabilities: acp.SessionCapabilities{Close: &acp.SessionCloseCapabilities{}}}}
	connection.prompt = func(ctx context.Context, request acp.PromptRequest) (acp.PromptResponse, error) {
		if err := ctx.Err(); err != nil {
			return acp.PromptResponse{}, err
		}
		err := remote.SessionUpdate(ctx, acp.SessionNotification{SessionId: request.SessionId, Update: acp.UpdateAgentMessageText("next turn completed")})
		return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, err
	}
	turn, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := remote.promptText(turn, "cancelled turn"); !errors.Is(err, context.Canceled) {
		t.Fatalf("turn cancellation changed: %v", err)
	}
	if closed || remote.processInterrupted() || len(connection.closed) != 0 {
		t.Fatal("turn cancellation closed the process")
	}
	if answer, err := remote.promptText(t.Context(), "next turn"); err != nil || answer != "next turn completed" {
		t.Fatalf("same session could not continue: %q %v", answer, err)
	}
	if err := remote.Close(); err != nil || !closed || len(connection.closed) != 1 {
		t.Fatalf("final cleanup: %v, closed=%t, requests=%v", err, closed, connection.closed)
	}
}

// This peer uses real process pipes without contacting a provider.
func TestACPShutdownPeerProcess(t *testing.T) {
	if len(os.Args) == 0 || os.Args[len(os.Args)-1] != "q-acp-shutdown-peer" {
		return
	}
	decoder, encoder := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	for {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := decoder.Decode(&request); err != nil {
			os.Exit(0)
		}
		var result any = map[string]any{}
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"sessionCapabilities": map[string]any{"close": map[string]any{}, "delete": map[string]any{}}}}
		case "session/new":
			result = map[string]any{"sessionId": "shutdown-session"}
		default:
			if !strings.HasPrefix(request.Method, "session/") {
				os.Exit(2)
			}
		}
		if len(request.ID) > 0 {
			if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}); err != nil {
				os.Exit(3)
			}
		}
	}
}
