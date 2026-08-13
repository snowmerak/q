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

const skillSyncPageSize = 1000

// SyncRecords makes the current discovered skills addressable through the
// workspace Session Store's Bleve index. Skill bodies remain on disk.
func (r *Registry) SyncRecords(ctx context.Context, store RecordStore) error {
	return r.SyncRecordsForScopes(ctx, store)
}

// SyncRecordsForScopes reconciles only the requested scopes. An empty scope
// list preserves SyncRecords' all-scope behavior. Unchanged digests are not
// saved or reindexed.
func (r *Registry) SyncRecordsForScopes(ctx context.Context, store RecordStore, scopes ...string) error {
	if r == nil || store == nil {
		return errors.New("agent skills: record store is unavailable")
	}
	wanted := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope != "" {
			wanted[scope] = struct{}{}
		}
	}
	existing, err := existingSkillRecords(ctx, store, scopes...)
	if err != nil {
		return err
	}
	old := make(map[string]sessionstore.Record, len(existing))
	for _, record := range existing {
		old[record.ID] = record
	}
	for _, skill := range r.Skills() {
		if len(wanted) > 0 {
			if _, ok := wanted[skill.Scope]; !ok {
				continue
			}
		}
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

// existingSkillRecords reads the complete derived skill projection before
// reconciliation starts mutating it. Session Store intentionally caps one
// search request at 1000 hits, so cleanup must walk the full result set rather
// than treating that request limit as a catalog limit.
func existingSkillRecords(ctx context.Context, store RecordStore, scopes ...string) ([]sessionstore.Record, error) {
	var records []sessionstore.Record
	for offset := 0; ; {
		page, err := store.Search(ctx, sessionstore.SearchOptions{
			Filters: sessionstore.Filters{Kinds: []string{sessionstore.KindSkill}, Scopes: scopes},
			Sort:    sessionstore.SortOldest,
			Limit:   skillSyncPageSize,
			Offset:  offset,
		})
		if err != nil {
			return nil, err
		}
		for _, hit := range page.Hits {
			records = append(records, hit.Record)
		}
		offset += len(page.Hits)
		if len(page.Hits) == 0 || uint64(offset) >= page.Total {
			return records, nil
		}
	}
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
