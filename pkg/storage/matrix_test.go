package storage_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mysqldrv "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/snowmerak/q/v2/pkg/domain"
	"github.com/snowmerak/q/v2/pkg/storage"
	"github.com/snowmerak/q/v2/pkg/storage/outbox"
	"github.com/snowmerak/q/v2/pkg/storage/rdb/mysql"
	"github.com/snowmerak/q/v2/pkg/storage/rdb/postgres"
	"github.com/snowmerak/q/v2/pkg/storage/rdb/sqlite"
	"github.com/snowmerak/q/v2/pkg/storage/search/blevehnsw"
	"github.com/snowmerak/q/v2/pkg/storage/search/opensearch"
)

func runOutboxAndSearchMatrixCase(t *testing.T, rdb storage.RDBStore, idx storage.SearchIndex) {
	t.Helper()
	ctx := t.Context()

	persona, err := rdb.Personas().Create(ctx, domain.Persona{Name: "Alex"})
	if err != nil {
		t.Fatalf("create persona: %v", err)
	}
	alice, err := rdb.Characters().Create(ctx, domain.Character{Name: "Alice"})
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := rdb.Characters().Create(ctx, domain.Character{Name: "Bob"})
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	room, err := rdb.Rooms().Create(ctx, domain.Room{
		Title:           "Matrix Harbor",
		Mode:            domain.RoomModeGroup,
		PersonaID:       persona.ID,
		PersonaSnapshot: persona,
	}, []domain.RoomMember{
		{CharacterID: alice.ID, CharacterSnapshot: alice, Active: true},
		{CharacterID: bob.ID, CharacterSnapshot: bob, Active: true},
	})
	if err != nil {
		t.Fatalf("create room: %v", err)
	}
	turn, created, err := rdb.Turns().CreateOrGetIdempotent(ctx, domain.Turn{
		RoomID:    room.ID,
		RequestID: "matrix-req-1",
		InputHash: domain.HashInput("matrix"),
		Status:    domain.TurnStatusCompleted,
	})
	if err != nil || !created {
		t.Fatalf("create turn: created=%v err=%v", created, err)
	}

	var m1, m2 domain.Message
	err = rdb.Tx(ctx, func(tx storage.RDBTx) error {
		var err error
		obsAll := []string{persona.ID, alice.ID, bob.ID}
		obsBobOnly := []string{persona.ID, bob.ID}

		m1, err = tx.Messages().Append(ctx, domain.Message{
			RoomID:     room.ID,
			TurnID:     turn.ID,
			Role:       domain.MessageRoleCharacter,
			AuthorID:   bob.ID,
			AuthorName: bob.Name,
			Content:    "harbor lantern coordinates alpha",
			ObservedBy: obsAll,
		})
		if err != nil {
			return err
		}
		p1, _ := json.Marshal(outbox.Payload{
			Scope:      "room:" + room.ID,
			OwnerID:    m1.AuthorID,
			ObservedBy: m1.ObservedBy,
			Text:       m1.Content,
			OccurredAt: m1.CreatedAt,
		})
		if _, err := tx.Outbox().Enqueue(ctx, domain.OutboxEntry{
			EntityType:  "message",
			EntityID:    m1.ID,
			Op:          domain.OutboxOpUpsert,
			Revision:    m1.Seq,
			PayloadJSON: string(p1),
			Status:      domain.OutboxStatusPending,
		}); err != nil {
			return err
		}

		m2, err = tx.Messages().Append(ctx, domain.Message{
			RoomID:     room.ID,
			TurnID:     turn.ID,
			Role:       domain.MessageRoleCharacter,
			AuthorID:   bob.ID,
			AuthorName: bob.Name,
			Content:    "secret vault passphrase omega",
			ObservedBy: obsBobOnly,
		})
		if err != nil {
			return err
		}
		p2, _ := json.Marshal(outbox.Payload{
			Scope:      "room:" + room.ID,
			OwnerID:    m2.AuthorID,
			ObservedBy: m2.ObservedBy,
			Text:       m2.Content,
			OccurredAt: m2.CreatedAt,
		})
		_, err = tx.Outbox().Enqueue(ctx, domain.OutboxEntry{
			EntityType:  "message",
			EntityID:    m2.ID,
			Op:          domain.OutboxOpUpsert,
			Revision:    m2.Seq,
			PayloadJSON: string(p2),
			Status:      domain.OutboxStatusPending,
		})
		return err
	})
	if err != nil {
		t.Fatalf("commit messages+outbox: %v", err)
	}

	worker := outbox.NewWorker(rdb, idx, nil, nil)
	worker.Start(ctx)
	deadline := time.Now().Add(10 * time.Second)
	for {
		pending, err := rdb.Outbox().ListPending(ctx, domain.NowMS()+60000, 10)
		if err == nil && len(pending) == 0 {
			// Also wait until search index reflects both docs.
			if docs, _, cerr := idx.Counts(ctx); cerr == nil && docs >= 2 {
				break
			}
		}
		if time.Now().After(deadline) {
			worker.Stop()
			t.Fatalf("timed out draining outbox into search index")
		}
		time.Sleep(50 * time.Millisecond)
	}
	worker.Stop()

	assertSearchIsolation := func(phase string) {
		t.Helper()
		aliceSecret, err := idx.Search(ctx, storage.SearchQuery{
			Text:       "passphrase",
			Mode:       "lexical",
			Scopes:     []string{"room:" + room.ID},
			ObserverID: alice.ID,
			Limit:      10,
		})
		if err != nil {
			t.Fatalf("%s alice secret search: %v", phase, err)
		}
		if len(aliceSecret) != 0 {
			t.Fatalf("%s isolation breach: alice saw %+v", phase, aliceSecret)
		}

		alicePublic, err := idx.Search(ctx, storage.SearchQuery{
			Text:       "coordinates",
			Mode:       "lexical",
			Scopes:     []string{"room:" + room.ID},
			ObserverID: alice.ID,
			Limit:      10,
		})
		if err != nil || len(alicePublic) != 1 || alicePublic[0].ID != m1.ID {
			t.Fatalf("%s alice public search = %+v err=%v, want %s", phase, alicePublic, err, m1.ID)
		}

		bobSecret, err := idx.Search(ctx, storage.SearchQuery{
			Text:       "passphrase",
			Mode:       "lexical",
			Scopes:     []string{"room:" + room.ID},
			ObserverID: bob.ID,
			Limit:      10,
		})
		if err != nil || len(bobSecret) != 1 || bobSecret[0].ID != m2.ID {
			t.Fatalf("%s bob secret search = %+v err=%v, want %s", phase, bobSecret, err, m2.ID)
		}
	}

	assertSearchIsolation("initial")

	// Rebuild from authoritative RDB messages and verify counts + isolation persist.
	msgs, err := rdb.Messages().List(ctx, room.ID, 0, 50)
	if err != nil || len(msgs) != 2 {
		t.Fatalf("list authoritative messages: len=%d err=%v", len(msgs), err)
	}
	rebuildCh := make(chan storage.SearchDocument, len(msgs))
	for _, msg := range msgs {
		rebuildCh <- storage.SearchDocument{
			ID:         msg.ID,
			EntityType: "message",
			Scope:      "room:" + room.ID,
			OwnerID:    msg.AuthorID,
			ObservedBy: msg.ObservedBy,
			Text:       msg.Content,
			OccurredAt: msg.CreatedAt,
			Revision:   msg.Seq,
		}
	}
	close(rebuildCh)
	if err := idx.ResetAndRebuild(ctx, rebuildCh); err != nil {
		t.Fatalf("ResetAndRebuild: %v", err)
	}
	docs, _, err := idx.Counts(ctx)
	if err != nil || docs != 2 {
		t.Fatalf("Counts after rebuild = (%d, %v), want 2", docs, err)
	}
	assertSearchIsolation("rebuilt")
}

func openIsolatedPostgres(t *testing.T, baseDSN string) storage.RDBStore {
	t.Helper()
	schema := "q_matrix_" + strings.ReplaceAll(domain.NewID()[:16], "-", "")
	adminDB, err := sql.Open("pgx", baseDSN)
	if err != nil {
		t.Fatalf("open admin postgres: %v", err)
	}
	if _, err := adminDB.Exec(`CREATE SCHEMA "` + schema + `"`); err != nil {
		_ = adminDB.Close()
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = adminDB.Exec(`DROP SCHEMA IF EXISTS "` + schema + `" CASCADE`)
		_ = adminDB.Close()
	})
	sep := "?"
	if strings.Contains(baseDSN, "?") {
		sep = "&"
	}
	st, err := postgres.Open(t.Context(), baseDSN+sep+"search_path="+schema)
	if err != nil {
		t.Fatalf("postgres.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func openIsolatedMySQL(t *testing.T, baseDSN string) storage.RDBStore {
	t.Helper()
	parsed, err := mysqldrv.ParseDSN(baseDSN)
	if err != nil {
		t.Fatalf("ParseDSN: %v", err)
	}
	adminCfg := *parsed
	adminCfg.DBName = ""
	adminDB, err := sql.Open("mysql", adminCfg.FormatDSN())
	if err != nil {
		t.Fatalf("open admin mysql: %v", err)
	}
	dbName := "q_matrix_" + strings.ReplaceAll(domain.NewID()[:16], "-", "")
	if _, err := adminDB.Exec("CREATE DATABASE `" + dbName + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"); err != nil {
		_ = adminDB.Close()
		t.Fatalf("create db: %v", err)
	}
	t.Cleanup(func() {
		_, _ = adminDB.Exec("DROP DATABASE IF EXISTS `" + dbName + "`")
		_ = adminDB.Close()
	})
	scopedCfg := *parsed
	scopedCfg.DBName = dbName
	st, err := mysql.Open(t.Context(), scopedCfg.FormatDSN())
	if err != nil {
		t.Fatalf("mysql.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func openIsolatedOpenSearch(t *testing.T, endpoint string) storage.SearchIndex {
	t.Helper()
	prefix := "q_matrix_" + strings.ReplaceAll(domain.NewID()[:8], "-", "")
	idx, err := opensearch.Open(context.Background(), opensearch.Config{
		Endpoint:    endpoint,
		IndexPrefix: prefix,
	})
	if err != nil {
		t.Fatalf("opensearch.Open: %v", err)
	}
	t.Cleanup(func() {
		_ = idx.Close()
		req, _ := http.NewRequest(http.MethodDelete, strings.TrimRight(endpoint, "/")+"/"+prefix+"*", nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
	})
	return idx
}

func TestStorageSixWayMatrix(t *testing.T) {
	pgDSN := strings.TrimSpace(os.Getenv("Q_TEST_POSTGRES_DSN"))
	myDSN := strings.TrimSpace(os.Getenv("Q_TEST_MYSQL_DSN"))
	osURL := strings.TrimSpace(os.Getenv("Q_TEST_OPENSEARCH_URL"))

	t.Run("SQLite_x_BleveHNSW", func(t *testing.T) {
		dir := t.TempDir()
		rdb, err := sqlite.Open(t.Context(), filepath.Join(dir, "q.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rdb.Close() }()
		idx, err := blevehnsw.Open(filepath.Join(dir, "index"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = idx.Close() }()
		runOutboxAndSearchMatrixCase(t, rdb, idx)
	})

	t.Run("SQLite_x_OpenSearch", func(t *testing.T) {
		if osURL == "" {
			t.Skip("Q_TEST_OPENSEARCH_URL not set")
		}
		rdb, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "q.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rdb.Close() }()
		runOutboxAndSearchMatrixCase(t, rdb, openIsolatedOpenSearch(t, osURL))
	})

	t.Run("Postgres_x_BleveHNSW", func(t *testing.T) {
		if pgDSN == "" {
			t.Skip("Q_TEST_POSTGRES_DSN not set")
		}
		idx, err := blevehnsw.Open(filepath.Join(t.TempDir(), "index"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = idx.Close() }()
		runOutboxAndSearchMatrixCase(t, openIsolatedPostgres(t, pgDSN), idx)
	})

	t.Run("Postgres_x_OpenSearch", func(t *testing.T) {
		if pgDSN == "" || osURL == "" {
			t.Skip("Q_TEST_POSTGRES_DSN and Q_TEST_OPENSEARCH_URL must be set")
		}
		runOutboxAndSearchMatrixCase(t, openIsolatedPostgres(t, pgDSN), openIsolatedOpenSearch(t, osURL))
	})

	t.Run("MySQL_x_BleveHNSW", func(t *testing.T) {
		if myDSN == "" {
			t.Skip("Q_TEST_MYSQL_DSN not set")
		}
		idx, err := blevehnsw.Open(filepath.Join(t.TempDir(), "index"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = idx.Close() }()
		runOutboxAndSearchMatrixCase(t, openIsolatedMySQL(t, myDSN), idx)
	})

	t.Run("MySQL_x_OpenSearch", func(t *testing.T) {
		if myDSN == "" || osURL == "" {
			t.Skip("Q_TEST_MYSQL_DSN and Q_TEST_OPENSEARCH_URL must be set")
		}
		runOutboxAndSearchMatrixCase(t, openIsolatedMySQL(t, myDSN), openIsolatedOpenSearch(t, osURL))
	})
}
