// Package storage defines the authoritative RDB, derived Search, and immutable
// Blob contracts for q v2, along with stable sentinel errors.
package storage

import (
	"context"
	"errors"
	"io"

	"github.com/snowmerak/q/v2/pkg/domain"
)

var (
	ErrNotFound      = errors.New("storage: not found")
	ErrAlreadyExists = errors.New("storage: already exists")
	ErrConflict      = errors.New("storage: version or idempotency conflict")
	ErrInvalidInput  = errors.New("storage: invalid input")
	ErrUnavailable   = errors.New("storage: backend unavailable")
)

// RDBStore is the single source of truth across SQLite, PostgreSQL, and MySQL.
type RDBStore interface {
	Tx(ctx context.Context, fn func(tx RDBTx) error) error
	Personas() PersonaRepo
	Characters() CharacterRepo
	Rooms() RoomRepo
	Turns() TurnRepo
	Messages() MessageRepo
	Tools() ToolInvocationRepo
	Memories() MemoryRepo
	Knowledge() KnowledgeRepo
	Outbox() OutboxRepo
	Artifacts() ArtifactRepo
	Ping(ctx context.Context) error
	Close() error
}

// RDBTx exposes the same repositories inside a single database transaction.
type RDBTx interface {
	Personas() PersonaRepo
	Characters() CharacterRepo
	Rooms() RoomRepo
	Turns() TurnRepo
	Messages() MessageRepo
	Tools() ToolInvocationRepo
	Memories() MemoryRepo
	Knowledge() KnowledgeRepo
	Outbox() OutboxRepo
	Artifacts() ArtifactRepo
}

type PersonaRepo interface {
	Create(ctx context.Context, p domain.Persona) (domain.Persona, error)
	Get(ctx context.Context, id string) (domain.Persona, error)
	List(ctx context.Context) ([]domain.Persona, error)
	Update(ctx context.Context, p domain.Persona) (domain.Persona, error)
	Delete(ctx context.Context, id string, deletedAt int64) error
}

type CharacterRepo interface {
	Create(ctx context.Context, c domain.Character) (domain.Character, error)
	Get(ctx context.Context, id string) (domain.Character, error)
	List(ctx context.Context) ([]domain.Character, error)
	Update(ctx context.Context, c domain.Character) (domain.Character, error)
	Delete(ctx context.Context, id string, deletedAt int64) error
}

type RoomRepo interface {
	Create(ctx context.Context, r domain.Room, members []domain.RoomMember) (domain.Room, error)
	Get(ctx context.Context, id string) (domain.Room, error)
	List(ctx context.Context) ([]domain.Room, error)
	Update(ctx context.Context, r domain.Room) (domain.Room, error)
	Delete(ctx context.Context, id string, deletedAt int64) error
	ListMembers(ctx context.Context, roomID string) ([]domain.RoomMember, error)
	UpsertMember(ctx context.Context, m domain.RoomMember) error
}

type TurnRepo interface {
	// CreateOrGetIdempotent inserts turn if (room_id, request_id) does not exist.
	// If it exists with matching InputHash, it returns the existing Turn and created=false.
	// If it exists with a different InputHash, it returns ErrConflict.
	CreateOrGetIdempotent(ctx context.Context, t domain.Turn) (turn domain.Turn, created bool, err error)
	Get(ctx context.Context, id string) (domain.Turn, error)
	Update(ctx context.Context, t domain.Turn) error
	ListByRoom(ctx context.Context, roomID string, limit int) ([]domain.Turn, error)
}

type MessageRepo interface {
	Append(ctx context.Context, m domain.Message) (domain.Message, error)
	Get(ctx context.Context, id string) (domain.Message, error)
	List(ctx context.Context, roomID string, beforeSeq int64, limit int) ([]domain.Message, error)
}

type ToolInvocationRepo interface {
	CreateInvocation(ctx context.Context, inv domain.ToolInvocation) error
	GetInvocation(ctx context.Context, id string) (domain.ToolInvocation, error)
	UpdateInvocation(ctx context.Context, inv domain.ToolInvocation) error
	ListInvocationsByTurn(ctx context.Context, turnID string) ([]domain.ToolInvocation, error)

	CreateQuestion(ctx context.Context, q domain.UserQuestion) error
	GetQuestion(ctx context.Context, id string) (domain.UserQuestion, error)
	UpdateQuestion(ctx context.Context, q domain.UserQuestion) error
	ListQuestionsByTurn(ctx context.Context, turnID string) ([]domain.UserQuestion, error)
}

type MemoryRepo interface {
	CreateMemory(ctx context.Context, m domain.Memory) error
	GetMemory(ctx context.Context, id string) (domain.Memory, error)
	// ListMemories lists active memories, optionally filtered by roomID (empty lists across all rooms), ownerID, and observerID.
	ListMemories(ctx context.Context, roomID, ownerID, observerID string, limit int) ([]domain.Memory, error)
	UpdateMemory(ctx context.Context, m domain.Memory) error

	CreateValueJob(ctx context.Context, job domain.ValueJob) error
	ClaimNextValueJob(ctx context.Context, nowMS int64) (*domain.ValueJob, error)
	UpdateValueJob(ctx context.Context, job domain.ValueJob) error
}

type KnowledgeRepo interface {
	CreateDoc(ctx context.Context, doc domain.KnowledgeDoc) (domain.KnowledgeDoc, error)
	GetDoc(ctx context.Context, id string) (domain.KnowledgeDoc, error)
	ListDocs(ctx context.Context, scope string) ([]domain.KnowledgeDoc, error)
	UpdateDoc(ctx context.Context, doc domain.KnowledgeDoc) (domain.KnowledgeDoc, error)
	DeleteDoc(ctx context.Context, id string, updatedAt int64) error

	ReplaceChunks(ctx context.Context, docID string, chunks []domain.KnowledgeChunk) error
	ListChunksByDoc(ctx context.Context, docID string) ([]domain.KnowledgeChunk, error)
	GetChunk(ctx context.Context, id string) (domain.KnowledgeChunk, error)
}

type OutboxRepo interface {
	Enqueue(ctx context.Context, entry domain.OutboxEntry) (int64, error)
	ListPending(ctx context.Context, nowMS int64, limit int) ([]domain.OutboxEntry, error)
	MarkDone(ctx context.Context, seq int64) error
	MarkRetry(ctx context.Context, seq int64, status domain.OutboxStatus, nextAttemptAt int64, lastErr string) error
}

type LoomRootCandidates struct {
	ActiveInvocationArtifactIDs []string
	ReferencedTexts             []string
}

type ArtifactRepo interface {
	Create(ctx context.Context, a domain.Artifact) error
	Get(ctx context.Context, id string) (domain.Artifact, error)
	ListByKind(ctx context.Context, kind domain.ArtifactKind) ([]domain.Artifact, error)
	ListLoomRootCandidates(ctx context.Context) (LoomRootCandidates, error)
	ClearToolInvocationRefs(ctx context.Context, artifactID string) error
	Touch(ctx context.Context, id string, accessedAt int64) error
	Delete(ctx context.Context, id string) error
}

type SearchDocument struct {
	ID         string
	EntityType string // "message" | "memory" | "knowledge_chunk"
	Scope      string // e.g. "room:<id>", "global"
	OwnerID    string
	ObservedBy []string
	Text       string
	OccurredAt int64 // UTC Unix ms
	Revision   int64
	Vector     []float32 // nil indexes lexical BM25 only
}

type SearchQuery struct {
	Text          string
	Vector        []float32
	Mode          string // "hybrid" | "lexical" | "semantic"
	EntityTypes   []string
	Scopes        []string
	ObserverID    string // when non-empty, only docs observed by this ID match
	Limit         int
	KeywordWeight float64 // defaults to 0.5
	HalfLifeTurns int
	MinDecay      float64 // defaults to 0.2
}

type SearchHit struct {
	ID         string
	EntityType string
	Score      float64
	Revision   int64
}

// SearchIndex is a rebuildable lexical + vector projection fed by index_outbox.
type SearchIndex interface {
	Upsert(ctx context.Context, doc SearchDocument) error
	UpsertBatch(ctx context.Context, docs []SearchDocument) error
	Delete(ctx context.Context, entityType, id string) error
	Search(ctx context.Context, q SearchQuery) ([]SearchHit, error)
	ConfigureVector(ctx context.Context, model string, dim int) error
	ResetAndRebuild(ctx context.Context, docs <-chan SearchDocument) error
	Counts(ctx context.Context) (docs uint64, vectors int, err error)
	Close() error
}

// BlobStore persists immutable binary payloads for Loom artifacts and media.
type BlobStore interface {
	Put(ctx context.Context, key string, r io.Reader, mediaType string) (size int64, sha256Hex string, err error)
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
	Close() error
}
