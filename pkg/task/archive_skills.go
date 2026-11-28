package task

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/snowmerak/q/v2/pkg/domain"
	"github.com/snowmerak/q/v2/pkg/memory"
	"github.com/snowmerak/q/v2/pkg/storage"
	"gopkg.in/yaml.v3"
)

type SkillSummary struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"`
}

type SkillStore struct {
	dirs []string
}

func NewSkillStore(dirs ...string) *SkillStore {
	clean := make([]string, 0, len(dirs))
	for _, d := range dirs {
		d = strings.TrimSpace(d)
		if d != "" {
			clean = append(clean, d)
		}
	}
	return &SkillStore{dirs: clean}
}

func (s *SkillStore) ListSkills() []SkillSummary {
	if s == nil {
		return []SkillSummary{}
	}
	seen := make(map[string]struct{})
	var out []SkillSummary
	for _, root := range s.dirs {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			skillPath := filepath.Join(root, e.Name(), "SKILL.md")
			raw, err := os.ReadFile(skillPath)
			if err != nil {
				continue
			}
			name, desc := parseSkillFrontmatter(string(raw), e.Name())
			if _, dup := seen[name]; dup {
				continue
			}
			seen[name] = struct{}{}
			out = append(out, SkillSummary{
				Name:        name,
				Description: desc,
				Path:        skillPath,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	if out == nil {
		out = []SkillSummary{}
	}
	return out
}

func (s *SkillStore) GetSkill(name string) (SkillSummary, string, error) {
	name = strings.TrimSpace(name)
	for _, sk := range s.ListSkills() {
		if strings.EqualFold(sk.Name, name) {
			raw, err := os.ReadFile(sk.Path)
			if err != nil {
				return SkillSummary{}, "", err
			}
			return sk, string(raw), nil
		}
	}
	return SkillSummary{}, "", fmt.Errorf("skill %q not found", name)
}

func parseSkillFrontmatter(raw, fallbackName string) (name, desc string) {
	name = fallbackName
	norm := strings.ReplaceAll(raw, "\r\n", "\n")
	if !strings.HasPrefix(norm, "---\n") {
		return name, desc
	}
	rest := norm[4:]
	idx := strings.Index(rest, "\n---")
	if idx == -1 {
		return name, desc
	}
	var fm struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if yaml.Unmarshal([]byte(rest[:idx]), &fm) == nil {
		if strings.TrimSpace(fm.Name) != "" {
			name = strings.TrimSpace(fm.Name)
		}
		desc = strings.TrimSpace(fm.Description)
	}
	return name, desc
}

func resolveArchiveRoomTitle(ctx context.Context, rdb storage.RDBStore, cache map[string]string, roomID string) string {
	if roomID == "" {
		return ""
	}
	if title, ok := cache[roomID]; ok {
		return title
	}
	title := "Another Room"
	if rm, err := rdb.Rooms().Get(ctx, roomID); err == nil && strings.TrimSpace(rm.Title) != "" {
		title = strings.TrimSpace(rm.Title)
	}
	cache[roomID] = title
	return title
}

func registerArchiveAndSkillTools(
	r *Registry,
	rdb storage.RDBStore,
	search storage.SearchIndex,
	embedderFn func() memory.QueryEmbedder,
	skills *SkillStore,
) {
	r.register(ToolSpec{
		Name:        "search_archive",
		Description: "Search past dialogue/memories across rooms this character observed (strictly observer-isolated) and indexed knowledge documents.",
		Source:      domain.ToolSourceBuiltin,
		Risk:        RiskRead,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":        map[string]any{"type": "string"},
				"entity_types": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"scopes":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"limit":        map[string]any{"type": "integer"},
			},
			"required": []string{"query"},
		},
		Handler: func(ctx context.Context, call CallContext, argsJSON string) (ToolOutput, error) {
			if search == nil || rdb == nil {
				return ToolOutput{Content: "archive search unavailable", IsError: true}, nil
			}
			var args struct {
				Query       string   `json:"query"`
				EntityTypes []string `json:"entity_types"`
				Scopes      []string `json:"scopes"`
				Limit       int      `json:"limit"`
			}
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
				return ToolOutput{Content: "invalid args: " + err.Error(), IsError: true}, nil
			}
			if args.Limit <= 0 || args.Limit > 20 {
				args.Limit = 10
			}
			if len(args.EntityTypes) == 0 {
				args.EntityTypes = []string{"memory", "message", "knowledge_chunk"}
			}

			var observerTypes []string
			includeKnowledge := false
			for _, et := range args.EntityTypes {
				switch et {
				case "memory", "message":
					observerTypes = append(observerTypes, et)
				case "knowledge_chunk":
					includeKnowledge = true
				}
			}

			mode := "lexical"
			var vec []float32
			if embedderFn != nil && strings.TrimSpace(args.Query) != "" {
				if emb := embedderFn(); emb != nil {
					if v, err := emb.EmbedQuery(ctx, args.Query); err == nil && len(v) > 0 {
						vec = v
						mode = "hybrid"
					}
				}
			}

			var hits []storage.SearchHit
			if len(observerTypes) > 0 {
				obsHits, err := search.Search(ctx, storage.SearchQuery{
					Text:        args.Query,
					Vector:      vec,
					Mode:        mode,
					EntityTypes: observerTypes,
					Scopes:      args.Scopes,
					ObserverID:  call.ActorID,
					Limit:       args.Limit,
				})
				if err != nil {
					return ToolOutput{Content: err.Error(), IsError: true}, nil
				}
				hits = append(hits, obsHits...)
			}
			if includeKnowledge {
				kLimit := args.Limit
				if len(args.Scopes) == 0 && kLimit < 24 {
					kLimit = max(args.Limit*3, 16)
				}
				kHits, err := search.Search(ctx, storage.SearchQuery{
					Text:        args.Query,
					Vector:      vec,
					Mode:        mode,
					EntityTypes: []string{"knowledge_chunk"},
					Scopes:      args.Scopes,
					Limit:       kLimit,
				})
				if err != nil {
					return ToolOutput{Content: err.Error(), IsError: true}, nil
				}
				hits = append(hits, kHits...)
			}

			type viewHit struct {
				ID         string  `json:"id"`
				EntityType string  `json:"entity_type"`
				RoomID     string  `json:"room_id,omitempty"`
				RoomTitle  string  `json:"room_title,omitempty"`
				Score      float64 `json:"score"`
				Preview    string  `json:"preview"`
			}
			roomTitles := make(map[string]string)
			results := make([]viewHit, 0, len(hits))
			for _, h := range hits {
				var preview, roomID, roomTitle string
				switch h.EntityType {
				case "memory":
					m, err := rdb.Memories().GetMemory(ctx, h.ID)
					if err != nil || m.Status != domain.MemoryStatusActive || (m.OwnerID != "" && m.OwnerID != call.ActorID) || !slices.Contains(m.ObservedBy, call.ActorID) {
						continue
					}
					roomID = m.RoomID
					if m.RoomID != "" && m.RoomID != call.RoomID {
						roomTitle = resolveArchiveRoomTitle(ctx, rdb, roomTitles, m.RoomID)
						preview = fmt.Sprintf("[Room: %s] %s", roomTitle, m.Content)
					} else {
						preview = m.Content
					}
				case "message":
					m, err := rdb.Messages().Get(ctx, h.ID)
					if err != nil || m.Role == domain.MessageRoleSystem || !m.ObservedByActor(call.ActorID) {
						continue
					}
					roomID = m.RoomID
					if m.RoomID != "" && m.RoomID != call.RoomID {
						roomTitle = resolveArchiveRoomTitle(ctx, rdb, roomTitles, m.RoomID)
						preview = fmt.Sprintf("[Room: %s | %s] %s", roomTitle, m.AuthorName, m.Content)
					} else {
						preview = fmt.Sprintf("[%s] %s", m.AuthorName, m.Content)
					}
				case "knowledge_chunk":
					ch, err := rdb.Knowledge().GetChunk(ctx, h.ID)
					if err != nil {
						continue
					}
					doc, err := rdb.Knowledge().GetDoc(ctx, ch.DocID)
					if err != nil || doc.Status != domain.KnowledgeStatusActive || ch.DocRevision != doc.Revision {
						continue
					}
					if !memory.KnowledgeScopeVisible(doc.Scope, call.RoomID, call.ActorID) {
						continue
					}
					preview = fmt.Sprintf("[%s] %s", doc.Title, ch.Text)
				}
				results = append(results, viewHit{
					ID:         h.ID,
					EntityType: h.EntityType,
					RoomID:     roomID,
					RoomTitle:  roomTitle,
					Score:      h.Score,
					Preview:    preview,
				})
			}
			sort.Slice(results, func(i, j int) bool {
				if results[i].Score == results[j].Score {
					return results[i].ID < results[j].ID
				}
				return results[i].Score > results[j].Score
			})
			if len(results) > args.Limit {
				results = results[:args.Limit]
			}
			out, _ := json.MarshalIndent(results, "", "  ")
			return ToolOutput{Content: string(out)}, nil
		},
	})

	r.register(ToolSpec{
		Name:        "get_archive_record",
		Description: "Fetch a full message, memory, or knowledge chunk record by entity_type and id (respecting observer isolation).",
		Source:      domain.ToolSourceBuiltin,
		Risk:        RiskRead,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"entity_type": map[string]any{"type": "string", "enum": []string{"memory", "message", "knowledge_chunk"}},
				"id":          map[string]any{"type": "string"},
			},
			"required": []string{"entity_type", "id"},
		},
		Handler: func(ctx context.Context, call CallContext, argsJSON string) (ToolOutput, error) {
			if rdb == nil {
				return ToolOutput{Content: "archive store unavailable", IsError: true}, nil
			}
			var args struct {
				EntityType string `json:"entity_type"`
				ID         string `json:"id"`
			}
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
				return ToolOutput{Content: "invalid args: " + err.Error(), IsError: true}, nil
			}
			switch args.EntityType {
			case "memory":
				m, err := rdb.Memories().GetMemory(ctx, args.ID)
				if err != nil {
					return ToolOutput{Content: err.Error(), IsError: true}, nil
				}
				if m.Status != domain.MemoryStatusActive || (m.OwnerID != "" && m.OwnerID != call.ActorID) || !slices.Contains(m.ObservedBy, call.ActorID) {
					return ToolOutput{Content: "record not visible to actor", IsError: true}, nil
				}
				out, _ := json.MarshalIndent(m, "", "  ")
				return ToolOutput{Content: string(out)}, nil
			case "message":
				m, err := rdb.Messages().Get(ctx, args.ID)
				if err != nil {
					return ToolOutput{Content: err.Error(), IsError: true}, nil
				}
				if m.Role == domain.MessageRoleSystem || !m.ObservedByActor(call.ActorID) {
					return ToolOutput{Content: "record not visible to actor", IsError: true}, nil
				}
				out, _ := json.MarshalIndent(m, "", "  ")
				return ToolOutput{Content: string(out)}, nil
			case "knowledge_chunk":
				ch, err := rdb.Knowledge().GetChunk(ctx, args.ID)
				if err != nil {
					return ToolOutput{Content: err.Error(), IsError: true}, nil
				}
				doc, err := rdb.Knowledge().GetDoc(ctx, ch.DocID)
				if err != nil || doc.Status != domain.KnowledgeStatusActive || ch.DocRevision != doc.Revision {
					return ToolOutput{Content: "knowledge doc inactive or not found", IsError: true}, nil
				}
				if !memory.KnowledgeScopeVisible(doc.Scope, call.RoomID, call.ActorID) {
					return ToolOutput{Content: "knowledge doc not visible in current scope", IsError: true}, nil
				}
				out, _ := json.MarshalIndent(map[string]any{"doc": doc, "chunk": ch}, "", "  ")
				return ToolOutput{Content: string(out)}, nil
			default:
				return ToolOutput{Content: "unsupported entity_type: " + args.EntityType, IsError: true}, nil
			}
		},
	})

	r.register(ToolSpec{
		Name:        "search_skills",
		Description: "Search available Agent Skills by keyword.",
		Source:      domain.ToolSourceBuiltin,
		Risk:        RiskRead,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string"},
			},
		},
		Handler: func(_ context.Context, _ CallContext, argsJSON string) (ToolOutput, error) {
			var args struct {
				Query string `json:"query"`
			}
			_ = json.Unmarshal([]byte(argsJSON), &args)
			needle := strings.ToLower(strings.TrimSpace(args.Query))
			all := skills.ListSkills()
			matched := make([]SkillSummary, 0, len(all))
			for _, sk := range all {
				if needle == "" || strings.Contains(strings.ToLower(sk.Name), needle) || strings.Contains(strings.ToLower(sk.Description), needle) {
					matched = append(matched, sk)
				}
			}
			out, _ := json.MarshalIndent(matched, "", "  ")
			return ToolOutput{Content: string(out)}, nil
		},
	})

	r.register(ToolSpec{
		Name:        "get_skill",
		Description: "Read the full SKILL.md instructions for an Agent Skill by name.",
		Source:      domain.ToolSourceBuiltin,
		Risk:        RiskRead,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string"},
			},
			"required": []string{"name"},
		},
		Handler: func(_ context.Context, _ CallContext, argsJSON string) (ToolOutput, error) {
			if skills == nil {
				return ToolOutput{Content: "skills store unavailable", IsError: true}, nil
			}
			var args struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
				return ToolOutput{Content: "invalid args: " + err.Error(), IsError: true}, nil
			}
			_, content, err := skills.GetSkill(args.Name)
			if err != nil {
				return ToolOutput{Content: err.Error(), IsError: true}, nil
			}
			return ToolOutput{Content: content}, nil
		},
	})
}
