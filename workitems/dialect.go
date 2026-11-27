package workitems

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"modernc.org/sqlite"
	sqlitecode "modernc.org/sqlite/lib"
)

const pgWriteLockKey int64 = 739150421

type sqlDialect interface {
	rebind(query string) string
	jsonArg(data []byte) any
	isUniqueViolation(err error) bool
	indexedBy(index string) string
	boolFalseLit() string
	emptyJSONLit() string
	max2(a, b string) string
	jsonText(col, path string) string
	jsonBoolFalse(col, path string) string
	jsonPresent(col, key string) string
	tsExpr(expr string) string
	jsonEachArray(placeholder string) string
	itemReadPayload() string
	beginWriteTx(ctx context.Context, db *sql.DB) (*storeTx, error)
}

type storeTx struct {
	*sql.Tx
	d sqlDialect
}

func (t *storeTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return t.Tx.ExecContext(ctx, t.d.rebind(query), args...)
}

func (t *storeTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return t.Tx.QueryContext(ctx, t.d.rebind(query), args...)
}

func (t *storeTx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return t.Tx.QueryRowContext(ctx, t.d.rebind(query), args...)
}

func (t *storeTx) queryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return t.QueryContext(ctx, query, args...)
}

func (s *Store) dialect() sqlDialect {
	if s != nil && s.d != nil {
		return s.d
	}
	return sqliteDialect{}
}

func (s *Store) beginTx(ctx context.Context) (*storeTx, error) {
	return s.dialect().beginWriteTx(ctx, s.db)
}

func (s *Store) beginReadTx(ctx context.Context) (*storeTx, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	return &storeTx{Tx: tx, d: s.dialect()}, nil
}

func (s *Store) execContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return s.db.ExecContext(ctx, s.dialect().rebind(query), args...)
}

func (s *Store) queryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return s.db.QueryContext(ctx, s.dialect().rebind(query), args...)
}

func (s *Store) queryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return s.db.QueryRowContext(ctx, s.dialect().rebind(query), args...)
}

type sqliteDialect struct{}

func (sqliteDialect) rebind(query string) string { return query }
func (sqliteDialect) jsonArg(data []byte) any    { return data }
func (sqliteDialect) isUniqueViolation(err error) bool {
	var sqliteErr *sqlite.Error
	return errors.As(err, &sqliteErr) && (sqliteErr.Code() == sqlitecode.SQLITE_CONSTRAINT_UNIQUE || sqliteErr.Code() == sqlitecode.SQLITE_CONSTRAINT_PRIMARYKEY)
}
func (sqliteDialect) indexedBy(index string) string     { return " INDEXED BY " + index }
func (sqliteDialect) boolFalseLit() string              { return "0" }
func (sqliteDialect) emptyJSONLit() string              { return "'{}'" }
func (sqliteDialect) max2(a, b string) string           { return "MAX(" + a + "," + b + ")" }
func (sqliteDialect) jsonText(col, path string) string  { return "json_extract(" + col + ",'$." + path + "')" }
func (d sqliteDialect) jsonBoolFalse(col, path string) string {
	return "coalesce(" + d.jsonText(col, path) + ",0)=0"
}
func (d sqliteDialect) jsonPresent(col, key string) string {
	return d.jsonText(col, key) + " IS NOT NULL"
}
func (sqliteDialect) tsExpr(expr string) string          { return "julianday(" + expr + ")" }
func (sqliteDialect) jsonEachArray(placeholder string) string {
	return "SELECT value FROM json_each(" + placeholder + ")"
}
func (d sqliteDialect) itemReadPayload() string {
	return `json_set(items.payload,'$.waiting_on',json((SELECT json_group_array(prerequisite_id) FROM (` + unmetDependenciesSQL(d) + ` ORDER BY dependency.prerequisite_id))))`
}
func (d sqliteDialect) beginWriteTx(ctx context.Context, db *sql.DB) (*storeTx, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &storeTx{Tx: tx, d: d}, nil
}

type postgresDialect struct{}

func (postgresDialect) rebind(query string) string {
	if !strings.ContainsRune(query, '?') {
		return query
	}
	var out strings.Builder
	out.Grow(len(query) + 16)
	arg := 1
	inSingleQuote := false
	for i := 0; i < len(query); i++ {
		ch := query[i]
		if ch == '\'' {
			if inSingleQuote && i+1 < len(query) && query[i+1] == '\'' {
				out.WriteString("''")
				i++
				continue
			}
			inSingleQuote = !inSingleQuote
			out.WriteByte(ch)
			continue
		}
		if ch == '?' && !inSingleQuote {
			out.WriteByte('$')
			writePositiveInt(&out, arg)
			arg++
			continue
		}
		out.WriteByte(ch)
	}
	return out.String()
}

func writePositiveInt(b *strings.Builder, n int) {
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	b.Write(buf[i:])
}

func (postgresDialect) jsonArg(data []byte) any {
	if data == nil {
		return nil
	}
	return string(data)
}

func (postgresDialect) isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func (postgresDialect) indexedBy(string) string    { return "" }
func (postgresDialect) boolFalseLit() string       { return "FALSE" }
func (postgresDialect) emptyJSONLit() string       { return "'{}'::jsonb" }
func (postgresDialect) max2(a, b string) string    { return "GREATEST(" + a + "," + b + ")" }
func (postgresDialect) tsExpr(expr string) string  { return "workitems_ts_epoch(" + expr + ")" }
func (postgresDialect) jsonEachArray(placeholder string) string {
	return "SELECT jsonb_array_elements_text(" + placeholder + "::jsonb)"
}

func (postgresDialect) jsonText(col, path string) string {
	parts := strings.Split(path, ".")
	if len(parts) == 1 {
		return col + "->>'" + parts[0] + "'"
	}
	return col + "#>>'{" + strings.Join(parts, ",") + "}'"
}

func (d postgresDialect) jsonBoolFalse(col, path string) string {
	return "coalesce((" + d.jsonText(col, path) + ")::boolean,false)=false"
}

func (postgresDialect) jsonPresent(col, key string) string {
	return "(" + col + "->'" + key + "' IS NOT NULL AND jsonb_typeof(" + col + "->'" + key + "')<>'null')"
}

func (d postgresDialect) itemReadPayload() string {
	return `jsonb_set(items.payload,'{waiting_on}',COALESCE((SELECT jsonb_agg(prerequisite_id ORDER BY prerequisite_id) FROM (` + unmetDependenciesSQL(d) + `) AS deps),'[]'::jsonb),true)`
}

func (d postgresDialect) beginWriteTx(ctx context.Context, db *sql.DB) (*storeTx, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, pgWriteLockKey); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return &storeTx{Tx: tx, d: d}, nil
}

// Shared dialect-aware SQL predicate builders.

func visibleBoardTicketSQL(d sqlDialect) string {
	return `kind='ticket' AND ` + d.jsonBoolFalse("payload", "deleted")
}

func activeBoardTicketsSQL(d sqlDialect) string {
	return visibleBoardTicketSQL(d) + ` AND ` + d.jsonText("payload", "state") + ` IN ('todo','in_progress')`
}

func doneBoardTicketsSQL(d sqlDialect) string {
	return visibleBoardTicketSQL(d) + ` AND ` + d.jsonText("payload", "state") + `='done'`
}

func unmetDependenciesSQL(d sqlDialect) string {
	return `SELECT dependency.prerequisite_id FROM ticket_dependencies AS dependency JOIN items AS prerequisite ON prerequisite.id=dependency.prerequisite_id WHERE dependency.ticket_id=items.id AND ` + d.jsonText("prerequisite.payload", "state") + `<>'done'`
}

func assignmentQueueSQL(d sqlDialect) string {
	return `kind='ticket' AND ` + d.jsonBoolFalse("payload", "deleted") + ` AND coalesce(` + d.jsonText("payload", "schedule.every") + `,'')='' AND ` + d.jsonBoolFalse("payload", "schedule.paused") + ` AND (` + d.jsonText("payload", "execution_state") + ` IN ('queued','assignment_waiting') OR (coalesce(` + d.jsonText("payload", "execution_state") + `,'')='' AND ` + d.jsonText("payload", "state") + `='todo' AND coalesce(` + d.jsonText("payload", "active_attempt_id") + `,'')=''))`
}

func mergeQueueSQL(d sqlDialect) string {
	return `kind='ticket' AND ` + d.jsonBoolFalse("payload", "deleted") + ` AND ` + d.jsonText("payload", "execution_state") + ` IN ('merge_queued','close_queued')`
}

func resumeQueueSQL(d sqlDialect) string {
	return `kind='ticket' AND ` + d.jsonBoolFalse("payload", "deleted") + ` AND ` + d.jsonText("payload", "execution_state") + `='waiting_children'`
}

func assignmentDueSQL(d sqlDialect) string {
	return d.max2(`coalesce(`+d.tsExpr(d.jsonText("payload", "next_assignment_at"))+`,0)`, `coalesce(`+d.tsExpr(d.jsonText("payload", "schedule.not_before"))+`,0)`)
}

func activeAttemptsSQL(d sqlDialect) string {
	return d.jsonText("payload", "status") + ` IN ('reserved','running')`
}

func leasedAttemptsSQL(d sqlDialect) string {
	return d.jsonText("payload", "status") + ` IN ('reserved','running','merging','closing')`
}

func pendingInboxSQL(d sqlDialect) string {
	return d.jsonBoolFalse("payload", "delivered")
}

func pendingScheduleRunSQL(d sqlDialect) string {
	return `coalesce(` + d.jsonText("payload", "schedule_id") + `,'')<>'' AND ` + d.jsonBoolFalse("payload", "deleted") + ` AND ` + d.jsonText("payload", "state") + `<>'done' AND coalesce(` + d.jsonText("payload", "execution_state") + `,'') NOT IN ('cancelled','rejected')`
}

func recurringScheduleQueueSQL(d sqlDialect) string {
	return `kind='ticket' AND ` + d.jsonBoolFalse("payload", "deleted") + ` AND coalesce(` + d.jsonText("payload", "schedule.every") + `,'')<>'' AND ` + d.jsonBoolFalse("payload", "schedule.paused") + ` AND ` + d.jsonText("payload", "execution_state") + `='scheduled' AND ` + d.jsonText("payload", "state") + `='todo'`
}

func childPredicateSQL(d sqlDialect) string {
	return `kind='ticket' AND ` + d.jsonBoolFalse("payload", "deleted") + ` AND ` + d.jsonText("payload", "parent_ticket_id") + `=? AND ` + d.jsonText("payload", "parent_worktree_id") + `=?`
}

func parentReadyCondSQL(d sqlDialect) string {
	return `(` + d.jsonText("items.payload", "parent_ticket_id") + ` IS NULL OR EXISTS (SELECT 1 FROM items AS parent JOIN ticket_attempts AS worker ON worker.id=` + d.jsonText("parent.payload", "active_attempt_id") + ` WHERE parent.id=` + d.jsonText("items.payload", "parent_ticket_id") + ` AND parent.workspace_id=items.workspace_id AND ` + d.jsonBoolFalse("parent.payload", "deleted") + ` AND ` + d.jsonText("parent.payload", "execution_state") + `='waiting_children' AND ` + d.jsonText("worker.payload", "worktree_id") + `=` + d.jsonText("items.payload", "parent_worktree_id") + `))`
}

func directChildCondSQL(d sqlDialect) string {
	return `child.kind='ticket' AND ` + d.jsonBoolFalse("child.payload", "deleted") + ` AND ` + d.jsonText("child.payload", "parent_ticket_id") + `=items.id AND ` + d.jsonText("child.payload", "parent_worktree_id") + `=(SELECT ` + d.jsonText("worker.payload", "worktree_id") + ` FROM ticket_attempts AS worker WHERE worker.id=` + d.jsonText("items.payload", "active_attempt_id") + `)`
}

func resumeReadyCondSQL(d sqlDialect) string {
	dc := directChildCondSQL(d)
	return `(EXISTS (SELECT 1 FROM items AS child WHERE ` + dc + ` AND ` + d.jsonText("child.payload", "execution_state") + ` IN ` + childReviewStates + `) OR NOT EXISTS (SELECT 1 FROM items AS child WHERE ` + dc + ` AND ` + d.jsonText("child.payload", "state") + `<>'done')) AND NOT EXISTS (SELECT 1 FROM items AS child WHERE ` + dc + ` AND ` + d.jsonText("child.payload", "execution_state") + ` IN ('merging','closing'))`
}
