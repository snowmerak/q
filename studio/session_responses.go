package studio

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/snowmerak/q/app"
	"github.com/snowmerak/q/workspace"
)

const responseChangeRunKind = "response_change"

type sessionResponseEditor interface {
	ReviseResponse(context.Context, workspace.Store, app.SessionResponseChange) (string, error)
}

func (service *sessionsService) serveResponseChange(writer http.ResponseWriter, request *http.Request) {
	if _, ok := service.runner.(sessionResponseEditor); !ok {
		writeSessionError(writer, app.ErrSessionRuntimeUnavailable)
		return
	}
	var input struct {
		WorkspaceRoot string     `json:"workspace_root"`
		Action        string     `json:"action"`
		Index         *int       `json:"index"`
		Content       string     `json:"content,omitempty"`
		UpdatedAt     *time.Time `json:"updated_at"`
	}
	if err := decodeRequest(writer, request, &input, 1<<20, "decode response change"); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	if input.Index == nil || *input.Index < 0 || input.UpdatedAt == nil {
		writeAPIError(writer, http.StatusBadRequest, errors.New("a response index and session timestamp are required"))
		return
	}
	switch input.Action {
	case "delete", "regenerate":
	case "edit":
		if strings.TrimSpace(input.Content) == "" {
			writeAPIError(writer, http.StatusBadRequest, errors.New("response text is required"))
			return
		}
	default:
		writeAPIError(writer, http.StatusBadRequest, errors.New("unsupported response action"))
		return
	}
	root, err := canonicalWorkspaceDirectory(input.WorkspaceRoot)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	change := app.SessionResponseChange{Index: *input.Index, Action: input.Action, Content: input.Content, UpdatedAt: *input.UpdatedAt}
	run, err := service.runs.startRun(root, request.PathValue("session"), studioRunInput{kind: responseChangeRunKind, responseChange: &change}, request.Context())
	if err != nil {
		writeSessionError(writer, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusAccepted, run.snapshotCopy())
}

func (service *sessionRunService) executeResponseChange(ctx context.Context, store workspace.Store, change app.SessionResponseChange, emit app.SessionEventSink) error {
	editor, ok := service.runner.(sessionResponseEditor)
	if !ok {
		return app.ErrSessionRuntimeUnavailable
	}
	if err := emit(app.SessionEvent{Type: "session", SessionID: store.SessionID}); err != nil {
		return err
	}
	detail, err := editor.ReviseResponse(ctx, store, change)
	if err != nil {
		return err
	}
	return emit(app.SessionEvent{Type: "result", Outcome: "succeeded", Detail: detail})
}
