package workitems

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/snowmerak/q/internal/closeutil"
)

// WorkspaceRepository manages project workspace definitions and defaults.
// Alternate backends (such as PostgreSQL) must preserve ErrNotFound,
// ErrConflict, and ErrInvalidInput semantics.
type WorkspaceRepository interface {
	GetProjectDefaults(ctx context.Context) (ProjectDefaults, error)
	UpdateProjectDefaults(ctx context.Context, value ProjectDefaults) (ProjectDefaults, error)
	ListWorkspaces(ctx context.Context, includeArchived bool) ([]Workspace, error)
	CreateWorkspace(ctx context.Context, input WorkspaceInput) (Workspace, error)
	UpdateWorkspace(ctx context.Context, id string, input WorkspaceInput) (Workspace, error)
	ArchiveWorkspace(ctx context.Context, id string) error
}

// ItemRepository stores issues, tickets, and immutable thread posts with
// RequestID idempotency and optimistic Revision checks.
type ItemRepository interface {
	CreateItem(ctx context.Context, in CreateInput) (Item, error)
	GetItem(ctx context.Context, id string) (Item, error)
	UpdateItem(ctx context.Context, id string, in UpdateInput) (Item, error)
	ListItems(ctx context.Context, workspaceID, kind, token string, limit int) (ItemPage, error)
	ListBoardTickets(ctx context.Context, workspaceID, view, token string, limit int) (ItemPage, error)
	ReadThread(ctx context.Context, id, token string, limit int) (Thread, error)
	AddPost(ctx context.Context, itemID string, in PostInput) (Post, error)
	GetPost(ctx context.Context, id string, revision int64) (Post, error)
	ReadContent(ctx context.Context, id, token string, limit int) (ContentPage, error)
	UpdatePost(ctx context.Context, id string, in PostInput) (Post, error)
}

// AssetRepository stores and retrieves workspace media attachments.
type AssetRepository interface {
	PutAsset(ctx context.Context, workspace string, data []byte, imagesOnly bool) (Asset, error)
	GetAsset(ctx context.Context, workspace, id string) (Asset, error)
}

// OutboxRepository exposes the transactional change feed and projection
// checkpoints used to keep derived search indexes synchronized.
type OutboxRepository interface {
	Changes(ctx context.Context, after int64, limit int) ([]Change, error)
	LatestChange(ctx context.Context) (int64, error)
	Applied(ctx context.Context, projection string) (int64, error)
	ApplyReceipt(ctx context.Context, projection string, seq int64) error
	ResetReceipt(ctx context.Context, projection string) error
}

// CoordinatorRepository manages workspace members, ticket execution attempts,
// manager/human inboxes, schedules, and review/merge state transitions.
type CoordinatorRepository interface {
	ListMembers(ctx context.Context, workspaceID string) ([]Member, error)
	PutMember(ctx context.Context, workspaceID string, in MemberInput) (Member, error)
	ControlTicket(ctx context.Context, id string, in TicketAction) (Item, error)
	ClaimTicket(ctx context.Context, id string, in ClaimInput) (Attempt, error)
	ListAttempts(ctx context.Context, ticketID string) ([]Attempt, error)
	ListChildren(ctx context.Context, id, worktree string) (ItemPage, error)
	GetAttempt(ctx context.Context, id string) (Attempt, error)
	UpdateAttempt(ctx context.Context, id string, in AttemptUpdate) (Attempt, error)
	ReviewAttempt(ctx context.Context, id string, in ReviewInput) (Attempt, error)
	ClaimMerge(ctx context.Context, id, owner string) (Attempt, error)
	RecoverExpired(ctx context.Context) error
	ListInbox(ctx context.Context, workspaceID string) ([]InboxEntry, error)
	NextPendingInbox(ctx context.Context, workspaceID string) ([]InboxEntry, error)
	AcknowledgeInbox(ctx context.Context, id, managerID string) error
	DeferInbox(ctx context.Context, id, managerID, reason string) error
	HumanInbox(ctx context.Context, workspaceID string) (HumanInbox, error)
	AnswerQuestion(ctx context.Context, id string, in AnswerInput) (Item, error)
	QueueTicketGuidance(ctx context.Context, id string, in TicketGuidanceInput) (TicketGuidance, error)
	DispatchTicketSchedules(ctx context.Context, workspaceID string, now time.Time) (ItemPage, error)
	ListTicketQueue(ctx context.Context, workspaceID, queue, token string, limit int) (ItemPage, error)
	GetTicketLoad(ctx context.Context, workspaceID string) (TicketLoad, error)
}

// Repository is the full authoritative storage contract for workspaces, items,
// transactional outbox events, and coordinator execution state.
type Repository interface {
	WorkspaceRepository
	ItemRepository
	AssetRepository
	OutboxRepository
	CoordinatorRepository
	io.Closer
}

var _ Repository = (*Store)(nil)

// ComposedRepository assembles the five domain sub-repositories via struct
// embedding while owning a deduplicated set of io.Closer resources.
type ComposedRepository struct {
	WorkspaceRepository
	ItemRepository
	AssetRepository
	OutboxRepository
	CoordinatorRepository

	closers   []io.Closer
	closeOnce sync.Once
	closeErr  error
}

var _ Repository = (*ComposedRepository)(nil)

// ComposeRepository builds a Repository from split role adapters sharing one
// or more underlying client closers. Duplicate comparable closers are closed
// at most once, and repeated Close calls are idempotent.
func ComposeRepository(
	workspaces WorkspaceRepository,
	items ItemRepository,
	assets AssetRepository,
	outbox OutboxRepository,
	coordinator CoordinatorRepository,
	closers ...io.Closer,
) (*ComposedRepository, error) {
	if workspaces == nil || items == nil || assets == nil || outbox == nil || coordinator == nil {
		return nil, errors.New("workitems: composed repository requires all role repositories")
	}
	return &ComposedRepository{
		WorkspaceRepository:   workspaces,
		ItemRepository:        items,
		AssetRepository:       assets,
		OutboxRepository:      outbox,
		CoordinatorRepository: coordinator,
		closers:               closeutil.Deduplicate(closers),
	}, nil
}

// Close closes every registered closer once and returns any joined errors.
func (r *ComposedRepository) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		r.closeErr = closeutil.CloseAll(r.closers)
	})
	return r.closeErr
}
