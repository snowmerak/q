package agentskills

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/snowmerak/q/sessionstore"
)

type RecordStore interface {
	Search(context.Context, sessionstore.SearchOptions) (sessionstore.SearchResult, error)
	Save(sessionstore.Record) (sessionstore.Record, error)
	Delete(string) error
}

type recordPayload struct {
	Source        Source            `json:"source"`
	Digest        string            `json:"digest"`
	License       string            `json:"license,omitempty"`
	Compatibility string            `json:"compatibility,omitempty"`
	AllowedTools  string            `json:"allowed_tools,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

// SyncRecords makes the current discovered skills addressable through the
// workspace Session Store's Bleve index. Skill bodies remain on disk.
func (r *Registry) SyncRecords(ctx context.Context, store RecordStore) error {
	if r == nil || store == nil {
		return errors.New("agent skills: record store is unavailable")
	}
	existing, err := store.Search(ctx, sessionstore.SearchOptions{
		Filters: sessionstore.Filters{Kinds: []string{sessionstore.KindSkill}}, Limit: 1000,
	})
	if err != nil {
		return err
	}
	old := make(map[string]sessionstore.Record, len(existing.Hits))
	for _, hit := range existing.Hits {
		old[hit.Record.ID] = hit.Record
	}
	for _, skill := range r.Skills() {
		payload := recordPayload{
			Source: skill.Source, Digest: skill.Digest, License: skill.License,
			Compatibility: skill.Compatibility, AllowedTools: skill.AllowedTools, Metadata: skill.Metadata,
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		record := sessionstore.Record{
			ID: skill.ID, Kind: sessionstore.KindSkill, Role: "skill", Status: sessionstore.StatusSucceeded,
			Scope: skill.Scope, Location: skill.Directory, Summary: skill.Name,
			Content: skill.Description, SearchText: skill.Description + " " + strings.Join(skill.Tags, " "),
			Tags: append([]string(nil), skill.Tags...), Payload: encoded,
		}
		if previous, ok := old[skill.ID]; ok && sameRecord(previous, record, skill.Digest) {
			delete(old, skill.ID)
			continue
		}
		if _, err := store.Save(record); err != nil {
			return err
		}
		delete(old, skill.ID)
	}
	for id := range old {
		if err := store.Delete(id); err != nil && !errors.Is(err, sessionstore.ErrNotFound) {
			return err
		}
	}
	return nil
}

func sameRecord(previous, current sessionstore.Record, digest string) bool {
	var payload recordPayload
	if json.Unmarshal(previous.Payload, &payload) != nil || payload.Digest != digest {
		return false
	}
	return previous.Kind == current.Kind && previous.Scope == current.Scope &&
		previous.Location == current.Location && previous.Summary == current.Summary &&
		previous.Content == current.Content && previous.SearchText == current.SearchText &&
		slices.Equal(previous.Tags, current.Tags)
}
