package workspace

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/snowmerak/q/internal/sessionmessage"
)

// sessionFile is Q's durable conversation format. Transport adapters convert
// its messages to Chat Completions or Responses only when making a request.
type sessionFile struct {
	Version            int                      `json:"version"`
	Type               SessionType              `json:"type"`
	ID                 string                   `json:"id,omitempty"`
	RunID              string                   `json:"run_id,omitempty"`
	LoopMode           string                   `json:"loop_mode,omitempty"`
	Title              string                   `json:"title,omitempty"`
	UpdatedAt          *time.Time               `json:"updated_at,omitempty"`
	Transcript         []sessionmessage.Message `json:"transcript,omitempty"`
	HistoryRevision    string                   `json:"history_revision,omitempty"`
	ResponseUsage      []ResponseUsage          `json:"response_usage,omitempty"`
	TranscriptThoughts []TranscriptThought      `json:"transcript_thoughts,omitempty"`
	Context            []sessionmessage.Message `json:"context,omitempty"`
	ResponseReplay     []ResponseReplayItem     `json:"response_replay,omitempty"`
	ResponseAffinity   *ResponseAffinity        `json:"response_affinity,omitempty"`
	Learning           json.RawMessage          `json:"learning,omitempty"`
	ActiveTask         *ActiveTask              `json:"active_task,omitempty"`
	Operation          *SessionOperation        `json:"operation,omitempty"`
}

func (s Session) MarshalJSON() ([]byte, error) {
	if err := s.Type.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(sessionFile{
		Type:    s.Type.Effective(),
		Version: CurrentVersion, ID: s.ID, RunID: s.RunID, Title: s.Title, UpdatedAt: s.UpdatedAt,
		Transcript: sessionmessage.Encode(s.Transcript), Context: sessionmessage.Encode(s.Context), HistoryRevision: s.HistoryRevision,
		ResponseUsage:      s.ResponseUsage,
		TranscriptThoughts: s.TranscriptThoughts,
		ResponseReplay:     s.ResponseReplay, ResponseAffinity: s.ResponseAffinity,
		ActiveTask: s.ActiveTask, Operation: s.Operation,
	})
}

func (s *Session) UnmarshalJSON(data []byte) error {
	var header struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return err
	}
	switch header.Version {
	case 1:
		// Version 1 stored Chat wire messages. Decode them once and write the
		// portable format on the next ordinary session save.
		type legacySession Session
		var legacy legacySession
		if err := decodeSessionStrict(data, &legacy); err != nil {
			return err
		}
		*s = Session(legacy)
		s.Learning = nil
		s.Type = s.Type.Effective()
		if err := s.Type.Validate(); err != nil {
			return err
		}
		s.Version = CurrentVersion
		return nil
	case 2, CurrentVersion:
		var saved sessionFile
		if err := decodeSessionStrict(data, &saved); err != nil {
			return err
		}
		if err := saved.Type.Validate(); err != nil {
			return err
		}
		if err := sessionmessage.Validate(saved.Transcript); err != nil {
			return err
		}
		if err := sessionmessage.Validate(saved.Context); err != nil {
			return err
		}
		*s = Session{
			Type:    saved.Type.Effective(),
			Version: CurrentVersion, ID: saved.ID, RunID: saved.RunID, Title: saved.Title, UpdatedAt: saved.UpdatedAt,
			Transcript: sessionmessage.Decode(saved.Transcript), Context: sessionmessage.Decode(saved.Context), HistoryRevision: saved.HistoryRevision,
			ResponseUsage:      saved.ResponseUsage,
			TranscriptThoughts: saved.TranscriptThoughts,
			ResponseReplay:     saved.ResponseReplay, ResponseAffinity: saved.ResponseAffinity,
			ActiveTask: saved.ActiveTask, Operation: saved.Operation,
		}
		return nil
	default:
		return fmt.Errorf("workspace: unsupported session version %d", header.Version)
	}
}

func decodeSessionStrict(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(value)
}
