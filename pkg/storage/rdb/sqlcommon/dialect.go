package sqlcommon

import (
	"context"
	"database/sql"
	"strconv"
	"strings"

	"github.com/snowmerak/q/v2/pkg/domain"
)

// Execer abstracts *sql.DB and *sql.Tx so repository methods work identically
// inside or outside an explicit transaction.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Dialect encapsulates the narrow SQL and driver differences across PostgreSQL,
// MySQL, and embedded SQL engines while sharing all repository implementations.
type Dialect interface {
	Name() string
	Rebind(query string) string
	UpsertMemberSQL() string
	EnqueueOutbox(ctx context.Context, exec Execer, entry domain.OutboxEntry) (int64, error)
	BeforeAppendMessage(ctx context.Context, exec Execer, roomID string) error
	ClaimValueJobLockClause() string
	MapError(err error) error
	IsUniqueViolation(err error) bool
}

// RebindDollar converts '?' placeholders into '$1, $2, ...' outside single-quoted literals.
func RebindDollar(query string) string {
	if !strings.ContainsRune(query, '?') {
		return query
	}
	var out strings.Builder
	out.Grow(len(query) + 16)
	argIdx := 1
	inSingle := false
	for i := 0; i < len(query); i++ {
		ch := query[i]
		if ch == '\'' {
			inSingle = !inSingle
			out.WriteByte(ch)
			continue
		}
		if ch == '?' && !inSingle {
			out.WriteByte('$')
			out.WriteString(strconv.Itoa(argIdx))
			argIdx++
			continue
		}
		out.WriteByte(ch)
	}
	return out.String()
}
