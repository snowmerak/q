// Package domain defines pure core entities, value objects, and state enums
// for q v2 without external storage or transport dependencies.
package domain

import "slices"

type ApprovalMode string

const (
	ApprovalBypass        ApprovalMode = "bypass"
	ApprovalAutoDetection ApprovalMode = "auto-detection"
	ApprovalRequest       ApprovalMode = "request"
)

func (m ApprovalMode) Valid() bool {
	return m == ApprovalBypass || m == ApprovalAutoDetection || m == ApprovalRequest
}

type RoomMode string

const (
	RoomModeDirect RoomMode = "direct"
	RoomModeGroup  RoomMode = "group"
)

func (m RoomMode) Valid() bool {
	return m == RoomModeDirect || m == RoomModeGroup
}

type TurnStatus string

const (
	TurnStatusRunning          TurnStatus = "running"
	TurnStatusAwaitingApproval TurnStatus = "awaiting_approval"
	TurnStatusAwaitingUser     TurnStatus = "awaiting_user"
	TurnStatusCompleted        TurnStatus = "completed"
	TurnStatusCancelled        TurnStatus = "cancelled"
	TurnStatusFailed           TurnStatus = "failed"
)

type MessageRole string

const (
	MessageRoleUser      MessageRole = "user"
	MessageRoleCharacter MessageRole = "character"
	MessageRoleSystem    MessageRole = "system"
)

type ToolSource string

const (
	ToolSourceBuiltin  ToolSource = "builtin"
	ToolSourceMCP      ToolSource = "mcp"
	ToolSourceBrowser  ToolSource = "browser"
	ToolSourceSubagent ToolSource = "subagent"
)

type ToolStatus string

const (
	ToolStatusPendingApproval ToolStatus = "pending_approval"
	ToolStatusApproved        ToolStatus = "approved"
	ToolStatusRejected        ToolStatus = "rejected"
	ToolStatusRunning         ToolStatus = "running"
	ToolStatusCompleted       ToolStatus = "completed"
	ToolStatusFailed          ToolStatus = "failed"
	ToolStatusCancelled       ToolStatus = "cancelled"
)

type QuestionStatus string

const (
	QuestionStatusPending   QuestionStatus = "pending"
	QuestionStatusAnswered  QuestionStatus = "answered"
	QuestionStatusCancelled QuestionStatus = "cancelled"
)

type MemoryKind string

const (
	MemoryKindEpisodic     MemoryKind = "episodic"
	MemoryKindFact         MemoryKind = "fact"
	MemoryKindValueHistory MemoryKind = "value_history"
)

type MemoryStatus string

const (
	MemoryStatusActive     MemoryStatus = "active"
	MemoryStatusSuperseded MemoryStatus = "superseded"
	MemoryStatusDeleted    MemoryStatus = "deleted"
)

type ValueJobStatus string

const (
	ValueJobQueued    ValueJobStatus = "queued"
	ValueJobRunning   ValueJobStatus = "running"
	ValueJobCompleted ValueJobStatus = "completed"
	ValueJobFailed    ValueJobStatus = "failed"
)

type KnowledgeStatus string

const (
	KnowledgeStatusActive  KnowledgeStatus = "active"
	KnowledgeStatusDeleted KnowledgeStatus = "deleted"
)

type OutboxOp string

const (
	OutboxOpUpsert OutboxOp = "upsert"
	OutboxOpDelete OutboxOp = "delete"
)

type OutboxStatus string

const (
	OutboxStatusPending        OutboxStatus = "pending"
	OutboxStatusEmbeddingRetry OutboxStatus = "embedding_retry"
	OutboxStatusDone           OutboxStatus = "done"
	OutboxStatusDead           OutboxStatus = "dead"
)

type ArtifactKind string

const (
	ArtifactKindLoom  ArtifactKind = "loom"
	ArtifactKindMedia ArtifactKind = "media"
)

type Persona struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Description      string `json:"description,omitempty"`
	AvatarArtifactID string `json:"avatar_artifact_id,omitempty"`
	IsDefault        bool   `json:"is_default"`
	Version          int64  `json:"version"`
	CreatedAt        int64  `json:"created_at"`
	UpdatedAt        int64  `json:"updated_at"`
	DeletedAt        *int64 `json:"deleted_at,omitempty"`
}

type ActorAffect struct {
	Valence    int  `json:"valence"`
	Activation int  `json:"activation"`
	Stressed   bool `json:"stressed"`
}

type Relationship struct {
	Valence    int  `json:"valence"`
	Activation int  `json:"activation"`
	Trust      int  `json:"trust"`
	Stressed   bool `json:"stressed"`
}

type InitialState struct {
	Self          *ActorAffect            `json:"self,omitempty"`
	Relationships map[string]Relationship `json:"relationships,omitempty"`
}

type ModelPrefs struct {
	ChatModel             string `json:"chat_model,omitempty"`
	MemoryModel           string `json:"memory_model,omitempty"`
	RecallPlannerModel    string `json:"recall_planner_model,omitempty"`
	DecisionModel         string `json:"decision_model,omitempty"`
	ReasoningEffort       string `json:"reasoning_effort,omitempty"`
	MemoryReasoningEffort string `json:"memory_reasoning_effort,omitempty"`
	ContextWindowOverride int    `json:"context_window_override,omitempty"`
}

type Character struct {
	ID                     string        `json:"id"`
	Name                   string        `json:"name"`
	AvatarArtifactID       string        `json:"avatar_artifact_id,omitempty"`
	Personality            string        `json:"personality,omitempty"`
	Background             string        `json:"background,omitempty"`
	Appearance             string        `json:"appearance,omitempty"`
	InitialValues          []string      `json:"initial_values,omitempty"`
	InitialState           *InitialState `json:"initial_state,omitempty"`
	ModelPrefs             *ModelPrefs   `json:"model_prefs,omitempty"`
	ApprovalPolicyOverride *ApprovalMode `json:"approval_policy_override,omitempty"`
	Version                int64         `json:"version"`
	CreatedAt              int64         `json:"created_at"`
	UpdatedAt              int64         `json:"updated_at"`
	DeletedAt              *int64        `json:"deleted_at,omitempty"`
}

type CharacterValue struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type PromptSnapshotState struct {
	Self          ActorAffect             `json:"self"`
	Relationships map[string]Relationship `json:"relationships,omitempty"`
	Values        []CharacterValue        `json:"values,omitempty"`
	WorkDir       string                  `json:"work_dir,omitempty"`
}

type PromptInjection struct {
	AfterSeq int64  `json:"after_seq"`
	Content  string `json:"content"`
}

type PromptCacheState struct {
	EpochSeq    int64                `json:"epoch_seq"`
	Baseline    *PromptSnapshotState `json:"baseline,omitempty"`
	LastEmitted *PromptSnapshotState `json:"last_emitted,omitempty"`
	Injections  []PromptInjection    `json:"injections,omitempty"`
}

type ActorState struct {
	Self                   ActorAffect             `json:"self"`
	StateUpdatedAt         int64                   `json:"state_updated_at,omitempty"`
	Relationships          map[string]Relationship `json:"relationships,omitempty"`
	RelationshipsUpdatedAt map[string]int64        `json:"relationships_updated_at,omitempty"`
	CompressedMemo         string                  `json:"compressed_memo,omitempty"`
	CompactedThroughSeq    int64                   `json:"compacted_through_seq,omitempty"`
	Values                 []CharacterValue        `json:"values,omitempty"`
	ValuesRevision         int64                   `json:"values_revision"`
	PromptCache            *PromptCacheState       `json:"prompt_cache,omitempty"`
}

type Room struct {
	ID                   string        `json:"id"`
	Title                string        `json:"title"`
	Mode                 RoomMode      `json:"mode"`
	PersonaID            string        `json:"persona_id"`
	PersonaSnapshot      Persona       `json:"persona_snapshot"`
	ApprovalModeOverride *ApprovalMode `json:"approval_mode_override,omitempty"`
	ModelOverrides       *ModelPrefs   `json:"model_overrides,omitempty"`
	MaxTurnsPerInput     int           `json:"max_turns_per_input"`
	WorkDir              string        `json:"work_dir,omitempty"`
	ContextVersion       int64         `json:"context_version"`
	Version              int64         `json:"version"`
	CreatedAt            int64         `json:"created_at"`
	UpdatedAt            int64         `json:"updated_at"`
	DeletedAt            *int64        `json:"deleted_at,omitempty"`
}

type RoomMember struct {
	RoomID            string     `json:"room_id"`
	CharacterID       string     `json:"character_id"`
	CharacterSnapshot Character  `json:"character_snapshot"`
	BaseSpeed         int        `json:"base_speed"`
	ActorState        ActorState `json:"actor_state"`
	Active            bool       `json:"active"`
	JoinedAt          int64      `json:"joined_at"`
}

type Turn struct {
	ID             string     `json:"id"`
	RoomID         string     `json:"room_id"`
	RequestID      string     `json:"request_id"`
	InputHash      string     `json:"input_hash"`
	Status         TurnStatus `json:"status"`
	CheckpointJSON string     `json:"checkpoint_json,omitempty"`
	ErrorCode      string     `json:"error_code,omitempty"`
	ErrorMessage   string     `json:"error_message,omitempty"`
	CreatedAt      int64      `json:"created_at"`
	UpdatedAt      int64      `json:"updated_at"`
	CompletedAt    *int64     `json:"completed_at,omitempty"`
}

type Message struct {
	ID           string      `json:"id"`
	RoomID       string      `json:"room_id"`
	TurnID       string      `json:"turn_id"`
	Seq          int64       `json:"seq"`
	Role         MessageRole `json:"role"`
	AuthorID     string      `json:"author_id"`
	AuthorName   string      `json:"author_name"`
	Content      string      `json:"content"`
	Subtext      string      `json:"-"` // private inner subtext, never exposed in public JSON
	ObservedBy   []string    `json:"observed_by"`
	LocationTag  string      `json:"location_tag,omitempty"`
	MetadataJSON string      `json:"metadata_json,omitempty"`
	CreatedAt    int64       `json:"created_at"`
}

func (m Message) ObservedByActor(actorID string) bool {
	return slices.Contains(m.ObservedBy, actorID)
}

type ToolInvocation struct {
	ID             string       `json:"id"`
	TurnID         string       `json:"turn_id"`
	RoomID         string       `json:"room_id"`
	ActorID        string       `json:"actor_id"`
	CallID         string       `json:"call_id"`
	ToolName       string       `json:"tool_name"`
	ToolSource     ToolSource   `json:"tool_source"`
	ArgsJSON       string       `json:"args_json"`
	GateMode       ApprovalMode `json:"gate_mode"`
	DecisionReason string       `json:"decision_reason,omitempty"`
	Status         ToolStatus   `json:"status"`
	ResultSummary  string       `json:"result_summary,omitempty"`
	ArtifactID     string       `json:"artifact_id,omitempty"`
	ErrorMessage   string       `json:"error_message,omitempty"`
	CreatedAt      int64        `json:"created_at"`
	UpdatedAt      int64        `json:"updated_at"`
}

type UserQuestion struct {
	ID            string         `json:"id"`
	TurnID        string         `json:"turn_id"`
	RoomID        string         `json:"room_id"`
	ActorID       string         `json:"actor_id"`
	Prompt        string         `json:"prompt"`
	Choices       []string       `json:"choices,omitempty"`
	AllowFreeform bool           `json:"allow_freeform"`
	Status        QuestionStatus `json:"status"`
	AnswerText    string         `json:"answer_text,omitempty"`
	CreatedAt     int64          `json:"created_at"`
	AnsweredAt    *int64         `json:"answered_at,omitempty"`
}

type Memory struct {
	ID             string       `json:"id"`
	RoomID         string       `json:"room_id"`
	OwnerID        string       `json:"owner_id"`
	SourceTurnID   string       `json:"source_turn_id,omitempty"`
	Kind           MemoryKind   `json:"kind"`
	Content        string       `json:"content"`
	ObservedBy     []string     `json:"observed_by"`
	OccurredAt     int64        `json:"occurred_at"`
	Pinned         bool         `json:"pinned"`
	Status         MemoryStatus `json:"status"`
	Revision       int64        `json:"revision"`
	EmbeddingModel string       `json:"embedding_model,omitempty"`
	EmbeddingDim   int          `json:"embedding_dim,omitempty"`
	Embedding      []float32    `json:"-"`
	CreatedAt      int64        `json:"created_at"`
}

type ValueJob struct {
	ID                 string         `json:"id"`
	RoomID             string         `json:"room_id"`
	CharacterID        string         `json:"character_id"`
	SourceTurnID       string         `json:"source_turn_id"`
	BaseValuesRevision int64          `json:"base_values_revision"`
	CandidatesJSON     string         `json:"candidates_json"`
	Status             ValueJobStatus `json:"status"`
	Attempts           int            `json:"attempts"`
	LastError          string         `json:"last_error,omitempty"`
	NextRunAt          int64          `json:"next_run_at"`
	CreatedAt          int64          `json:"created_at"`
	UpdatedAt          int64          `json:"updated_at"`
}

type KnowledgeDoc struct {
	ID            string          `json:"id"`
	Scope         string          `json:"scope"`
	Title         string          `json:"title"`
	SourceURI     string          `json:"source_uri,omitempty"`
	MediaType     string          `json:"media_type"`
	Content       string          `json:"content"`
	ContentSHA256 string          `json:"content_sha256"`
	Revision      int64           `json:"revision"`
	Status        KnowledgeStatus `json:"status"`
	CreatedAt     int64           `json:"created_at"`
	UpdatedAt     int64           `json:"updated_at"`
}

type KnowledgeChunk struct {
	ID             string    `json:"id"`
	DocID          string    `json:"doc_id"`
	DocRevision    int64     `json:"doc_revision"`
	ChunkOrdinal   int       `json:"chunk_ordinal"`
	CharOffset     int       `json:"char_offset"`
	Text           string    `json:"text"`
	EmbeddingModel string    `json:"embedding_model,omitempty"`
	EmbeddingDim   int       `json:"embedding_dim,omitempty"`
	Embedding      []float32 `json:"-"`
}

type OutboxEntry struct {
	Seq           int64        `json:"seq"`
	EntityType    string       `json:"entity_type"`
	EntityID      string       `json:"entity_id"`
	Op            OutboxOp     `json:"op"`
	Revision      int64        `json:"revision"`
	PayloadJSON   string       `json:"payload_json"`
	Status        OutboxStatus `json:"status"`
	Attempts      int          `json:"attempts"`
	NextAttemptAt int64        `json:"next_attempt_at"`
	LastError     string       `json:"last_error,omitempty"`
	CreatedAt     int64        `json:"created_at"`
}

type Artifact struct {
	ID             string       `json:"id"`
	Kind           ArtifactKind `json:"kind"`
	MediaType      string       `json:"media_type"`
	ByteSize       int64        `json:"byte_size"`
	SHA256         string       `json:"sha256"`
	BlobKey        string       `json:"blob_key"`
	SourceMetaJSON string       `json:"source_meta_json,omitempty"`
	Pinned         bool         `json:"pinned"`
	CreatedAt      int64        `json:"created_at"`
	LastAccessedAt int64        `json:"last_accessed_at"`
}
