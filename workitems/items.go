package workitems

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	Issue  = "issue"
	Ticket = "ticket"
)

// Item contains thread metadata; its body lives only in the referenced Post.
type Item struct {
	ID                string    `json:"id"`
	WorkspaceID       string    `json:"workspace_id"`
	Kind              string    `json:"kind"`
	Number            int64     `json:"number"`
	Title             string    `json:"title"`
	BodyPostID        string    `json:"body_post_id"`
	State             string    `json:"state"`
	Labels            []string  `json:"labels"`
	Acceptance        []string  `json:"acceptance,omitempty"`
	ExecutionMode     string    `json:"execution_mode,omitempty"`
	RequestedMemberID string    `json:"requested_member_id,omitempty"`
	LinkedIssueID     string    `json:"linked_issue_id,omitempty"`
	ExecutionState    string    `json:"execution_state,omitempty"`
	ExecutionReason   string    `json:"execution_reason,omitempty"`
	ActiveAttemptID   string    `json:"active_attempt_id,omitempty"`
	AssignedMemberID  string    `json:"assigned_member_id,omitempty"`
	NextAssignmentAt  time.Time `json:"next_assignment_at,omitempty"`
	Revision          int64     `json:"revision"`
	SourceRevision    int64     `json:"source_revision"`
	Deleted           bool      `json:"deleted"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type Post struct {
	ContentTruncated bool      `json:"content_truncated,omitempty"`
	ID               string    `json:"id"`
	WorkspaceID      string    `json:"workspace_id"`
	ItemID           string    `json:"item_id"`
	Kind             string    `json:"kind"`
	Seq              int64     `json:"seq"`
	Content          string    `json:"content"`
	Author           string    `json:"author"`
	Refs             []string  `json:"refs"`
	ReplyToPostID    string    `json:"reply_to_post_id,omitempty"`
	Revision         int64     `json:"revision"`
	Deleted          bool      `json:"deleted"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type CreateInput struct {
	WorkspaceID       string   `json:"workspace_id"`
	Kind              string   `json:"kind"`
	Title             string   `json:"title"`
	Content           string   `json:"content"`
	Labels            []string `json:"labels,omitempty"`
	Acceptance        []string `json:"acceptance,omitempty"`
	ExecutionMode     string   `json:"execution_mode,omitempty"`
	RequestedMemberID string   `json:"requested_member_id,omitempty"`
	LinkedIssueID     string   `json:"linked_issue_id,omitempty"`
	Author            string   `json:"author"`
	Refs              []string `json:"refs,omitempty"`
	RequestID         string   `json:"request_id,omitempty"`
}

type UpdateInput struct {
	Revision          int64    `json:"revision"`
	Title             string   `json:"title"`
	State             string   `json:"state"`
	Labels            []string `json:"labels"`
	Acceptance        []string `json:"acceptance,omitempty"`
	RequestedMemberID string   `json:"requested_member_id,omitempty"`
	LinkedIssueID     string   `json:"linked_issue_id,omitempty"`
	ExecutionMode     string   `json:"execution_mode,omitempty"`
	Deleted           bool     `json:"deleted"`
	RequestID         string   `json:"request_id,omitempty"`
}

type PostInput struct {
	Content       string   `json:"content"`
	Author        string   `json:"author"`
	Refs          []string `json:"refs,omitempty"`
	ReplyToPostID string   `json:"reply_to_post_id,omitempty"`
	Revision      int64    `json:"revision,omitempty"`
	Deleted       bool     `json:"deleted,omitempty"`
	RequestID     string   `json:"request_id,omitempty"`
}

type Thread struct {
	Item       Item   `json:"item"`
	Posts      []Post `json:"posts"`
	NextCursor string `json:"next_cursor,omitempty"`
}
type ItemPage struct {
	Items      []Item `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}
type ContentPage struct {
	Post       Post   `json:"post"`
	Offset     int    `json:"offset"`
	Total      int    `json:"total"`
	NextCursor string `json:"next_cursor,omitempty"`
}
type Change struct {
	Seq         int64  `json:"seq"`
	WorkspaceID string `json:"workspace_id"`
	ItemID      string `json:"item_id"`
	PostID      string `json:"post_id,omitempty"`
}

func (s *Store) initializeItems(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS items (
 seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id), kind TEXT NOT NULL CHECK(kind IN ('issue','ticket')),
 number INTEGER NOT NULL, payload BLOB NOT NULL, UNIQUE(workspace_id,kind,number)
);
CREATE INDEX IF NOT EXISTS items_workspace ON items(workspace_id,kind,seq);
CREATE TABLE IF NOT EXISTS posts (
 id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), item_id TEXT NOT NULL REFERENCES items(id),
 seq INTEGER NOT NULL, revision INTEGER NOT NULL, payload BLOB NOT NULL, UNIQUE(item_id,seq)
);
CREATE TABLE IF NOT EXISTS post_revisions (post_id TEXT NOT NULL REFERENCES posts(id), revision INTEGER NOT NULL, payload BLOB NOT NULL, PRIMARY KEY(post_id,revision));
CREATE TABLE IF NOT EXISTS item_revisions (item_id TEXT NOT NULL REFERENCES items(id), revision INTEGER NOT NULL, payload BLOB NOT NULL, PRIMARY KEY(item_id,revision));
CREATE TABLE IF NOT EXISTS mutation_receipts (workspace_id TEXT NOT NULL, request_id TEXT NOT NULL, fingerprint BLOB NOT NULL, result BLOB NOT NULL, PRIMARY KEY(workspace_id,request_id));
CREATE TABLE IF NOT EXISTS changes (seq INTEGER PRIMARY KEY AUTOINCREMENT, workspace_id TEXT NOT NULL, item_id TEXT NOT NULL, post_id TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS projection_receipts (projection TEXT PRIMARY KEY, applied_seq INTEGER NOT NULL);
`)
	return err
}

func validateContent(content, author string) error {
	if strings.TrimSpace(content) == "" || !utf8.ValidString(content) || len(content) > 2<<20 || strings.TrimSpace(author) == "" || len(author) > 256 {
		return fmt.Errorf("%w: content (up to 2 MiB) and author are required", ErrInvalidInput)
	}
	return nil
}

func validKind(kind string) bool { return kind == Issue || kind == Ticket }

func validItemText(labels, acceptance []string) bool {
	if len(labels) > 32 || len(acceptance) > 64 {
		return false
	}
	for _, label := range labels {
		if strings.TrimSpace(label) == "" || len(label) > 128 || !utf8.ValidString(label) {
			return false
		}
	}
	for _, criterion := range acceptance {
		if strings.TrimSpace(criterion) == "" || len(criterion) > 2048 || !utf8.ValidString(criterion) {
			return false
		}
	}
	return true
}

func requireWorkspace(ctx context.Context, tx *sql.Tx, id string) error {
	var found int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM workspaces WHERE id=? AND archived_at IS NULL`, id).Scan(&found); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else {
		return err
	}
}

// mutate commits a receipt with the source mutation. Reusing a key with a
// different operation or input is a conflict, including after process restart.
func mutate[T any](ctx context.Context, s *Store, workspaceID, operation, requestID string, input any, action func(*sql.Tx) (T, error)) (_ T, returnErr error) {
	var zero T
	if len(requestID) > 256 {
		return zero, ErrInvalidInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer func() { _ = tx.Rollback() }()
	body, err := json.Marshal([]any{operation, input})
	if err != nil {
		return zero, err
	}
	hash := sha256.Sum256(body)
	if requestID != "" {
		var fingerprint, result []byte
		err := tx.QueryRowContext(ctx, `SELECT fingerprint,result FROM mutation_receipts WHERE workspace_id=? AND request_id=?`, workspaceID, requestID).Scan(&fingerprint, &result)
		if err == nil {
			if string(fingerprint) != string(hash[:]) {
				return zero, ErrConflict
			}
			var value T
			err = json.Unmarshal(result, &value)
			return value, err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return zero, err
		}
	}
	if err := requireWorkspace(ctx, tx, workspaceID); err != nil {
		return zero, err
	}
	value, err := action(tx)
	if err != nil {
		return zero, err
	}
	if requestID != "" {
		result, err := json.Marshal(value)
		if err != nil {
			return zero, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO mutation_receipts VALUES(?,?,?,?)`, workspaceID, requestID, hash[:], result); err != nil {
			return zero, err
		}
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return value, nil
}

func saveItem(ctx context.Context, tx *sql.Tx, item Item) error {
	body, err := json.Marshal(item)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE items SET payload=? WHERE id=?`, body, item.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO item_revisions VALUES(?,?,?)`, item.ID, item.Revision, body); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO changes(workspace_id,item_id,post_id) VALUES(?,?,'')`, item.WorkspaceID, item.ID)
	return err
}

func savePost(ctx context.Context, tx *sql.Tx, post Post, create bool) error {
	body, err := json.Marshal(post)
	if err != nil {
		return err
	}
	if create {
		_, err = tx.ExecContext(ctx, `INSERT INTO posts VALUES(?,?,?,?,?,?)`, post.ID, post.WorkspaceID, post.ItemID, post.Seq, post.Revision, body)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE posts SET revision=?,payload=? WHERE id=?`, post.Revision, body, post.ID)
	}
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO post_revisions VALUES(?,?,?)`, post.ID, post.Revision, body); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO changes(workspace_id,item_id,post_id) VALUES(?,?,?)`, post.WorkspaceID, post.ItemID, post.ID)
	return err
}

func decodeRow[T any](row *sql.Row) (T, error) {
	var value T
	var body []byte
	if err := row.Scan(&body); errors.Is(err, sql.ErrNoRows) {
		return value, ErrNotFound
	} else if err != nil {
		return value, err
	}
	err := json.Unmarshal(body, &value)
	return value, err
}

func (s *Store) CreateItem(ctx context.Context, in CreateInput) (Item, error) {
	in.Title = strings.TrimSpace(in.Title)
	if !validKind(in.Kind) || in.Title == "" || len(in.Title) > 512 || !validItemText(in.Labels, in.Acceptance) {
		return Item{}, ErrInvalidInput
	}
	if err := validateContent(in.Content, in.Author); err != nil {
		return Item{}, err
	}
	if in.ExecutionMode == "" {
		in.ExecutionMode = "report"
	}
	if in.ExecutionMode != "report" && in.ExecutionMode != "git" {
		return Item{}, ErrInvalidInput
	}
	return mutate(ctx, s, in.WorkspaceID, "create", in.RequestID, in, func(tx *sql.Tx) (Item, error) {
		now := time.Now().UTC()
		item := Item{ID: uuid.NewString(), WorkspaceID: in.WorkspaceID, Kind: in.Kind, Title: in.Title, State: "open", Labels: in.Labels, Revision: 1, SourceRevision: 1, CreatedAt: now, UpdatedAt: now}
		if item.Labels == nil {
			item.Labels = []string{}
		}
		if in.Kind == Ticket {
			item.State = "todo"
			item.ExecutionState = "queued"
			item.Acceptance = in.Acceptance
			item.ExecutionMode = in.ExecutionMode
			item.RequestedMemberID = in.RequestedMemberID
			item.LinkedIssueID = in.LinkedIssueID
		}
		if in.LinkedIssueID != "" {
			linked, err := decodeRow[Item](tx.QueryRowContext(ctx, `SELECT payload FROM items WHERE id=? AND workspace_id=? AND kind='issue'`, in.LinkedIssueID, in.WorkspaceID))
			if err != nil || linked.Deleted {
				return Item{}, ErrInvalidInput
			}
		}
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(number),0)+1 FROM items WHERE workspace_id=? AND kind=?`, in.WorkspaceID, in.Kind).Scan(&item.Number); err != nil {
			return Item{}, err
		}
		post := Post{ID: uuid.NewString(), WorkspaceID: item.WorkspaceID, ItemID: item.ID, Kind: "body", Seq: 1, Content: in.Content, Author: in.Author, Refs: in.Refs, Revision: 1, CreatedAt: now, UpdatedAt: now}
		item.BodyPostID = post.ID
		if _, err := tx.ExecContext(ctx, `INSERT INTO items(id,workspace_id,kind,number,payload) VALUES(?,?,?,?,?)`, item.ID, item.WorkspaceID, item.Kind, item.Number, []byte(`{}`)); err != nil {
			return Item{}, err
		}
		if err := savePost(ctx, tx, post, true); err != nil {
			return Item{}, err
		}
		return item, saveItem(ctx, tx, item)
	})
}

func (s *Store) GetItem(ctx context.Context, id string) (Item, error) {
	return decodeRow[Item](s.db.QueryRowContext(ctx, `SELECT payload FROM items WHERE id=?`, id))
}

func (s *Store) UpdateItem(ctx context.Context, id string, in UpdateInput) (Item, error) {
	current, err := s.GetItem(ctx, id)
	if err != nil {
		return Item{}, err
	}
	return mutate(ctx, s, current.WorkspaceID, "update:"+id, in.RequestID, in, func(tx *sql.Tx) (Item, error) {
		item, err := decodeRow[Item](tx.QueryRowContext(ctx, `SELECT payload FROM items WHERE id=?`, id))
		if err != nil {
			return Item{}, err
		}
		if item.Revision != in.Revision {
			return Item{}, ErrConflict
		}
		if strings.TrimSpace(in.Title) == "" || len(in.Title) > 512 || !validItemText(in.Labels, in.Acceptance) {
			return Item{}, ErrInvalidInput
		}
		if item.Kind == Issue && in.State != "open" && in.State != "closed" {
			return Item{}, ErrInvalidInput
		}
		// Ticket transitions belong to the coordinator's attempt/review methods.
		if item.Kind == Ticket && in.State != item.State {
			return Item{}, fmt.Errorf("%w: use the ticket execution/review operations to move columns", ErrInvalidInput)
		}
		if item.Kind == Ticket && in.ExecutionMode != "" && in.ExecutionMode != item.ExecutionMode {
			if in.ExecutionMode != "git" && in.ExecutionMode != "report" {
				return Item{}, ErrInvalidInput
			}
			if item.ActiveAttemptID != "" {
				a, err := decodeRow[Attempt](tx.QueryRowContext(ctx, `SELECT payload FROM ticket_attempts WHERE id=?`, item.ActiveAttemptID))
				if err != nil {
					return Item{}, err
				}
				if a.Status == "reserved" || a.Status == "running" || a.Status == "merging" || a.Status == "closing" || a.ChangeRequest != nil && !a.ChangeRequest.Final() {
					return Item{}, ErrConflict
				}
			}
			item.ExecutionMode = in.ExecutionMode
		}
		if in.LinkedIssueID != "" {
			linked, err := decodeRow[Item](tx.QueryRowContext(ctx, `SELECT payload FROM items WHERE id=? AND workspace_id=? AND kind='issue'`, in.LinkedIssueID, item.WorkspaceID))
			if err != nil || linked.Deleted {
				return Item{}, ErrInvalidInput
			}
		}
		if item.Kind == Ticket && in.Deleted && item.ActiveAttemptID != "" {
			a, err := decodeRow[Attempt](tx.QueryRowContext(ctx, `SELECT payload FROM ticket_attempts WHERE id=?`, item.ActiveAttemptID))
			if err != nil {
				return Item{}, err
			}
			if a.Status == "reserved" || a.Status == "running" || a.Status == "merging" || a.Status == "closing" || a.ChangeRequest != nil && !a.ChangeRequest.Final() {
				return Item{}, fmt.Errorf("%w: stop and resolve active work before deleting the ticket", ErrConflict)
			}
		}
		item.Title, item.State, item.Labels, item.Deleted = strings.TrimSpace(in.Title), in.State, in.Labels, in.Deleted
		item.Acceptance, item.RequestedMemberID, item.LinkedIssueID = in.Acceptance, in.RequestedMemberID, in.LinkedIssueID
		item.Revision++
		item.SourceRevision++
		item.UpdatedAt = time.Now().UTC()
		return item, saveItem(ctx, tx, item)
	})
}

func (s *Store) AddPost(ctx context.Context, itemID string, in PostInput) (Post, error) {
	current, err := s.GetItem(ctx, itemID)
	if err != nil {
		return Post{}, err
	}
	if err := validateContent(in.Content, in.Author); err != nil {
		return Post{}, err
	}
	return mutate(ctx, s, current.WorkspaceID, "reply:"+itemID, in.RequestID, in, func(tx *sql.Tx) (Post, error) {
		item, err := decodeRow[Item](tx.QueryRowContext(ctx, `SELECT payload FROM items WHERE id=?`, itemID))
		if err != nil {
			return Post{}, err
		}
		if item.Deleted {
			return Post{}, ErrNotFound
		}
		if in.ReplyToPostID != "" {
			var found int
			if err := tx.QueryRowContext(ctx, `SELECT 1 FROM posts WHERE id=? AND item_id=?`, in.ReplyToPostID, itemID).Scan(&found); err != nil {
				return Post{}, ErrInvalidInput
			}
		}
		now := time.Now().UTC()
		post := Post{ID: uuid.NewString(), WorkspaceID: item.WorkspaceID, ItemID: itemID, Kind: "reply", Content: in.Content, Author: in.Author, Refs: in.Refs, ReplyToPostID: in.ReplyToPostID, Revision: 1, CreatedAt: now, UpdatedAt: now}
		if err := tx.QueryRowContext(ctx, `SELECT MAX(seq)+1 FROM posts WHERE item_id=?`, itemID).Scan(&post.Seq); err != nil {
			return Post{}, err
		}
		if err := savePost(ctx, tx, post, true); err != nil {
			return Post{}, err
		}
		item.UpdatedAt = now
		item.Revision++
		item.SourceRevision++
		return post, saveItem(ctx, tx, item)
	})
}

func (s *Store) GetPost(ctx context.Context, id string, revision int64) (Post, error) {
	if revision > 0 {
		return decodeRow[Post](s.db.QueryRowContext(ctx, `SELECT payload FROM post_revisions WHERE post_id=? AND revision=?`, id, revision))
	}
	return decodeRow[Post](s.db.QueryRowContext(ctx, `SELECT payload FROM posts WHERE id=?`, id))
}

func (s *Store) UpdatePost(ctx context.Context, id string, in PostInput) (Post, error) {
	current, err := s.GetPost(ctx, id, 0)
	if err != nil {
		return Post{}, err
	}
	if !in.Deleted {
		if err := validateContent(in.Content, in.Author); err != nil {
			return Post{}, err
		}
	}
	return mutate(ctx, s, current.WorkspaceID, "post:"+id, in.RequestID, in, func(tx *sql.Tx) (Post, error) {
		post, err := decodeRow[Post](tx.QueryRowContext(ctx, `SELECT payload FROM posts WHERE id=?`, id))
		if err != nil {
			return Post{}, err
		}
		if post.Revision != in.Revision {
			return Post{}, ErrConflict
		}
		post.Revision++
		post.UpdatedAt = time.Now().UTC()
		post.Deleted = in.Deleted
		if !in.Deleted {
			post.Content, post.Author, post.Refs = in.Content, in.Author, in.Refs
		}
		if err := savePost(ctx, tx, post, false); err != nil {
			return Post{}, err
		}
		item, err := decodeRow[Item](tx.QueryRowContext(ctx, `SELECT payload FROM items WHERE id=?`, post.ItemID))
		if err != nil {
			return Post{}, err
		}
		item.UpdatedAt = post.UpdatedAt
		item.Revision++
		item.SourceRevision++
		return post, saveItem(ctx, tx, item)
	})
}

type pageCursor struct {
	Scope    string `json:"s"`
	After    int64  `json:"a"`
	Upper    int64  `json:"u"`
	Revision int64  `json:"r,omitempty"`
}

func encodeCursor(c pageCursor) string {
	body, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(body)
}
func readCursor(token, scope string) (pageCursor, error) {
	c := pageCursor{Scope: scope}
	if token == "" {
		return c, nil
	}
	if len(token) > 2048 {
		return c, ErrInvalidInput
	}
	body, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return c, ErrInvalidInput
	}
	if json.Unmarshal(body, &c) != nil || c.Scope != scope || c.After < 0 || c.Upper < c.After || c.Revision < 0 {
		return c, fmt.Errorf("%w: cursor does not match the requested resource", ErrInvalidInput)
	}
	return c, nil
}
func pageLimit(limit int) int {
	if limit <= 0 {
		return 30
	}
	return min(limit, 100)
}

func (s *Store) ListItems(ctx context.Context, workspaceID, kind, token string, limit int) (ItemPage, error) {
	if !validKind(kind) {
		return ItemPage{}, ErrInvalidInput
	}
	c, err := readCursor(token, "items:"+workspaceID+":"+kind)
	if err != nil {
		return ItemPage{}, err
	}
	limit = pageLimit(limit)
	if token == "" {
		if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM items`).Scan(&c.Upper); err != nil {
			return ItemPage{}, err
		}
	}
	query := `SELECT seq,payload FROM items WHERE seq>? AND seq<=? AND kind=?`
	args := []any{c.After, c.Upper, kind}
	if workspaceID != "" {
		query += ` AND workspace_id=?`
		args = append(args, workspaceID)
	}
	query += ` ORDER BY seq LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return ItemPage{}, err
	}
	defer func() { _ = rows.Close() }()
	out := ItemPage{Items: []Item{}}
	for rows.Next() {
		var seq int64
		var body []byte
		if err := rows.Scan(&seq, &body); err != nil {
			return out, err
		}
		if len(out.Items) == limit {
			out.NextCursor = encodeCursor(c)
			break
		}
		var item Item
		if err := json.Unmarshal(body, &item); err != nil {
			return out, err
		}
		out.Items = append(out.Items, item)
		c.After = seq
	}
	return out, rows.Err()
}

func (s *Store) ReadThread(ctx context.Context, id, token string, limit int) (Thread, error) {
	item, err := s.GetItem(ctx, id)
	if err != nil {
		return Thread{}, err
	}
	c, err := readCursor(token, "thread:"+id)
	if err != nil {
		return Thread{}, err
	}
	limit = pageLimit(limit)
	if token == "" {
		if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM posts WHERE item_id=?`, id).Scan(&c.Upper); err != nil {
			return Thread{}, err
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT payload FROM posts WHERE item_id=? AND seq>? AND seq<=? ORDER BY seq LIMIT ?`, id, c.After, c.Upper, limit+1)
	if err != nil {
		return Thread{}, err
	}
	defer func() { _ = rows.Close() }()
	out := Thread{Item: item, Posts: []Post{}}
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			return out, err
		}
		if len(out.Posts) == limit {
			out.NextCursor = encodeCursor(c)
			break
		}
		var post Post
		if err := json.Unmarshal(body, &post); err != nil {
			return out, err
		}
		if post.Deleted {
			post.Content = ""
		}
		if content := []rune(post.Content); len(content) > 512 {
			post.Content = string(content[:512])
			post.ContentTruncated = true
		}
		out.Posts = append(out.Posts, post)
		c.After = post.Seq
	}
	return out, rows.Err()
}

func (s *Store) ReadContent(ctx context.Context, id, token string, limit int) (ContentPage, error) {
	c, err := readCursor(token, "content:"+id)
	if err != nil {
		return ContentPage{}, err
	}
	post, err := s.GetPost(ctx, id, c.Revision)
	if err != nil {
		return ContentPage{}, err
	}
	content := []rune(post.Content)
	if post.Deleted {
		content = nil
	}
	if token == "" {
		c.Revision = post.Revision
		c.Upper = int64(len(content))
	}
	if c.After > int64(len(content)) || c.Upper != int64(len(content)) {
		return ContentPage{}, ErrInvalidInput
	}
	if limit <= 0 {
		limit = 8192
	}
	limit = min(limit, 16384)
	end := min(int(c.After)+limit, len(content))
	out := ContentPage{Post: post, Offset: int(c.After), Total: len(content)}
	out.Post.Content = string(content[c.After:int64(end)])
	if end < len(content) {
		c.After = int64(end)
		out.NextCursor = encodeCursor(c)
	}
	return out, nil
}

func (s *Store) Changes(ctx context.Context, after int64, limit int) ([]Change, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT seq,workspace_id,item_id,post_id FROM changes WHERE seq>? ORDER BY seq LIMIT ?`, after, pageLimit(limit))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	values := []Change{}
	for rows.Next() {
		var c Change
		if err := rows.Scan(&c.Seq, &c.WorkspaceID, &c.ItemID, &c.PostID); err != nil {
			return nil, err
		}
		values = append(values, c)
	}
	return values, rows.Err()
}
func (s *Store) Applied(ctx context.Context, projection string) (int64, error) {
	var seq int64
	err := s.db.QueryRowContext(ctx, `SELECT applied_seq FROM projection_receipts WHERE projection=?`, projection).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return seq, err
}
func (s *Store) ApplyReceipt(ctx context.Context, projection string, seq int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO projection_receipts VALUES(?,?) ON CONFLICT(projection) DO UPDATE SET applied_seq=MAX(applied_seq,excluded.applied_seq)`, projection, seq)
	return err
}

func (s *Store) LatestChange(ctx context.Context) (int64, error) {
	var seq int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM changes`).Scan(&seq)
	return seq, err
}
func (s *Store) ResetReceipt(ctx context.Context, projection string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM projection_receipts WHERE projection=?`, projection)
	return err
}
