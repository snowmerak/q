package workitems

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/google/uuid"
)

// Edges are committed with the source item. Readiness is derived from the
// prerequisite's current done state, never from worker success or queue order.
func (s *Store) initializeDependencies(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS ticket_dependencies (
 ticket_id TEXT NOT NULL REFERENCES items(id), prerequisite_id TEXT NOT NULL REFERENCES items(id),
 PRIMARY KEY(ticket_id,prerequisite_id), CHECK(ticket_id<>prerequisite_id)
);
CREATE INDEX IF NOT EXISTS ticket_dependents ON ticket_dependencies(prerequisite_id,ticket_id);
PRAGMA user_version=9;`)
	return err
}

const unmetDependencies = `SELECT dependency.prerequisite_id FROM ticket_dependencies AS dependency
 JOIN items AS prerequisite ON prerequisite.id=dependency.prerequisite_id
 WHERE dependency.ticket_id=items.id AND json_extract(prerequisite.payload,'$.state')<>'done'`

// The response-only waiting list is read from the same SQLite snapshot as the
// item. It is deliberately absent from source revisions and mutation receipts.
const itemReadPayload = `json_set(items.payload,'$.waiting_on',json((SELECT json_group_array(prerequisite_id) FROM (` + unmetDependencies + ` ORDER BY dependency.prerequisite_id))))`

func setTicketDependencies(ctx context.Context, tx *sql.Tx, item *Item, ids []string, initial bool) error {
	if item.Kind != Ticket || len(ids) > 64 {
		return fmt.Errorf("%w: depends_on accepts up to 64 ticket IDs", ErrInvalidInput)
	}
	ids = slices.Clone(ids)
	sort.Strings(ids)
	for i, id := range ids {
		if uuid.Validate(id) != nil || id == item.ID || i > 0 && ids[i-1] == id {
			return fmt.Errorf("%w: dependencies must be distinct tickets other than this ticket", ErrInvalidInput)
		}
	}
	if slices.Equal(item.DependsOn, ids) {
		return nil
	}
	if item.ActiveAttemptID != "" || !initial && item.ScheduleID != "" || item.State != "todo" {
		return fmt.Errorf("%w: dependencies can only change before execution or on a recurring template", ErrConflict)
	}
	for _, id := range ids {
		prerequisite, err := decodeRow[Item](tx.QueryRowContext(ctx, `SELECT payload FROM items WHERE id=? AND workspace_id=? AND kind='ticket'`, id, item.WorkspaceID))
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		if err != nil || prerequisite.Deleted || prerequisite.Schedule != nil && prerequisite.Schedule.Every != "" {
			return fmt.Errorf("%w: prerequisite %q must be an existing ticket in this workspace; recurring templates cannot complete", ErrInvalidInput, id)
		}
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	var cyclic bool
	err = tx.QueryRowContext(ctx, `WITH RECURSIVE ancestors(id) AS (
 SELECT value FROM json_each(?)
 UNION SELECT dependency.prerequisite_id FROM ticket_dependencies AS dependency JOIN ancestors ON dependency.ticket_id=ancestors.id
) SELECT EXISTS(SELECT 1 FROM ancestors WHERE id=?)`, string(encoded), item.ID).Scan(&cyclic)
	if err != nil {
		return err
	}
	if cyclic {
		return fmt.Errorf("%w: ticket dependencies would form a cycle", ErrInvalidInput)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM ticket_dependencies WHERE ticket_id=?`, item.ID); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `INSERT INTO ticket_dependencies VALUES(?,?)`, item.ID, id); err != nil {
			return err
		}
	}
	item.DependsOn = ids
	return nil
}

func requireReadyDependencies(ctx context.Context, tx *sql.Tx, id string) error {
	var waiting bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM items WHERE id=? AND EXISTS(`+unmetDependencies+`))`, id).Scan(&waiting)
	if err != nil {
		return err
	}
	if waiting {
		return fmt.Errorf("%w: all prerequisite tickets must be done before execution", ErrConflict)
	}
	return nil
}

func requireNoUnfinishedDependents(ctx context.Context, tx *sql.Tx, item Item) error {
	var referenced bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM ticket_dependencies AS dependency JOIN items AS dependent ON dependent.id=dependency.ticket_id
 WHERE dependency.prerequisite_id=? AND coalesce(json_extract(dependent.payload,'$.deleted'),0)=0 AND json_extract(dependent.payload,'$.state')<>'done'
)`, item.ID).Scan(&referenced)
	if err != nil {
		return err
	}
	if referenced {
		return fmt.Errorf("%w: unfinished tickets depend on this ticket; remove those dependencies first", ErrConflict)
	}
	return nil
}
