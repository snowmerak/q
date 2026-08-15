package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const (
	propositionQueueFileName = "proposition-jobs.sqlite"
	propositionJobRetention  = 7 * 24 * time.Hour
	propositionJobLease      = 10 * time.Minute
)

type propositionJob struct {
	Key       string
	Digest    string
	Request   PropositionRegisterRequest
	State     string
	Decision  PropositionDecision
	Attempts  int
	CreatedAt time.Time
}

type propositionQueue struct {
	db     *sql.DB
	notify chan struct{}
	now    func() time.Time
}

type propositionJobFailure struct{ message string }

func (e *propositionJobFailure) Error() string { return e.message }

func openPropositionQueue(dir string) (*propositionQueue, error) {
	root := filepath.Join(dir, "library")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("library: create proposition queue directory: %w", err)
	}
	path := filepath.Join(root, propositionQueueFileName)
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("library: open proposition queue: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	queue := &propositionQueue{db: db, notify: make(chan struct{}, 1), now: time.Now}
	if err := queue.initialize(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("library: secure proposition queue: %w", err)
	}
	return queue, nil
}

func (q *propositionQueue) initialize() error {
	if q == nil || q.db == nil {
		return errors.New("library: proposition queue is unavailable")
	}
	_, err := q.db.Exec(`
CREATE TABLE IF NOT EXISTS proposition_jobs (
  idempotency_key TEXT PRIMARY KEY,
  request_digest TEXT NOT NULL,
  request_json BLOB NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('queued','running','decided','applying','succeeded','failed')),
  attempts INTEGER NOT NULL DEFAULT 0,
  lease_until INTEGER,
  decision_json BLOB,
  result_json BLOB,
  last_error TEXT,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  completed_at INTEGER
);
CREATE INDEX IF NOT EXISTS proposition_jobs_pending
  ON proposition_jobs(state, created_at);
CREATE TABLE IF NOT EXISTS proposition_receipts (
  idempotency_key TEXT PRIMARY KEY,
  request_digest TEXT NOT NULL,
  result_json BLOB NOT NULL,
  completed_at INTEGER NOT NULL
);`)
	if err != nil {
		return fmt.Errorf("library: initialize proposition queue: %w", err)
	}
	if _, err := q.db.Exec(`
UPDATE proposition_jobs
SET state = CASE WHEN state = 'applying' THEN 'decided' ELSE 'queued' END,
    lease_until = NULL
WHERE state IN ('running','applying')`); err != nil {
		return fmt.Errorf("library: recover proposition queue: %w", err)
	}
	return nil
}

func (q *propositionQueue) close() error {
	if q == nil || q.db == nil {
		return nil
	}
	return q.db.Close()
}

func (q *propositionQueue) submit(ctx context.Context, key, digest string, request PropositionRegisterRequest) (PropositionRegisterResponse, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return PropositionRegisterResponse{}, fmt.Errorf("library: encode proposition job: %w", err)
	}
	now := q.now().UTC().UnixMilli()
	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return PropositionRegisterResponse{}, err
	}
	defer tx.Rollback()
	if response, found, err := receiptFromQuerier(ctx, tx, key, digest); err != nil {
		return PropositionRegisterResponse{}, err
	} else if found {
		if response.Action == PropositionActionCreate {
			response.Created = false
		}
		return response, nil
	}
	var existingDigest, existingState string
	ownsAttempt := false
	err = tx.QueryRowContext(ctx, `SELECT request_digest, state FROM proposition_jobs WHERE idempotency_key = ?`, key).Scan(&existingDigest, &existingState)
	switch {
	case err == nil && existingDigest != digest:
		return PropositionRegisterResponse{}, errPropositionIdempotencyConflict
	case err == nil:
		if existingState == "failed" {
			if _, err = tx.ExecContext(ctx, `
UPDATE proposition_jobs
SET state = 'queued', last_error = NULL, lease_until = NULL, updated_at = ?
WHERE idempotency_key = ?`, now, key); err != nil {
				return PropositionRegisterResponse{}, err
			}
			ownsAttempt = true
		}
	case errors.Is(err, sql.ErrNoRows):
		if _, err = tx.ExecContext(ctx, `
INSERT INTO proposition_jobs(idempotency_key, request_digest, request_json, state, created_at, updated_at)
VALUES(?, ?, ?, 'queued', ?, ?)`, key, digest, body, now, now); err != nil {
			return PropositionRegisterResponse{}, fmt.Errorf("library: enqueue proposition job: %w", err)
		}
		ownsAttempt = true
	default:
		return PropositionRegisterResponse{}, err
	}
	if err := tx.Commit(); err != nil {
		return PropositionRegisterResponse{}, err
	}
	q.signal()
	return q.wait(ctx, key, digest, ownsAttempt)
}

func (q *propositionQueue) wait(ctx context.Context, key, digest string, ownsAttempt bool) (PropositionRegisterResponse, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if response, found, err := receiptFromQuerier(ctx, q.db, key, digest); err != nil {
			return PropositionRegisterResponse{}, err
		} else if found {
			if !ownsAttempt && response.Action == PropositionActionCreate {
				response.Created = false
			}
			return response, nil
		}
		var state string
		var lastError sql.NullString
		err := q.db.QueryRowContext(ctx, `SELECT state, last_error FROM proposition_jobs WHERE idempotency_key = ?`, key).Scan(&state, &lastError)
		if errors.Is(err, sql.ErrNoRows) {
			return PropositionRegisterResponse{}, errors.New("library: proposition job disappeared before completion")
		}
		if err != nil {
			return PropositionRegisterResponse{}, err
		}
		if state == "failed" {
			message := "library: proposition job failed"
			if lastError.Valid && lastError.String != "" {
				message += ": " + lastError.String
			}
			return PropositionRegisterResponse{}, &propositionJobFailure{message: message}
		}
		select {
		case <-ctx.Done():
			return PropositionRegisterResponse{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

type sqlQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func receiptFromQuerier(ctx context.Context, query sqlQuerier, key, digest string) (PropositionRegisterResponse, bool, error) {
	var existingDigest string
	var body []byte
	err := query.QueryRowContext(ctx, `
SELECT request_digest, result_json FROM proposition_receipts WHERE idempotency_key = ?`, key).Scan(&existingDigest, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return PropositionRegisterResponse{}, false, nil
	}
	if err != nil {
		return PropositionRegisterResponse{}, false, err
	}
	if existingDigest != digest {
		return PropositionRegisterResponse{}, false, errPropositionIdempotencyConflict
	}
	var response PropositionRegisterResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return PropositionRegisterResponse{}, false, fmt.Errorf("library: decode proposition receipt: %w", err)
	}
	return response, true, nil
}

func (q *propositionQueue) claim(ctx context.Context) (propositionJob, bool, error) {
	now := q.now().UTC()
	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return propositionJob{}, false, err
	}
	defer tx.Rollback()
	row := tx.QueryRowContext(ctx, `
SELECT idempotency_key, request_digest, request_json, state, decision_json, attempts, created_at
FROM proposition_jobs
WHERE state IN ('queued','decided','applying') OR (state = 'running' AND lease_until < ?)
ORDER BY created_at, idempotency_key
LIMIT 1`, now.UnixMilli())
	var job propositionJob
	var requestBody, decisionBody []byte
	var createdAt int64
	if err := row.Scan(&job.Key, &job.Digest, &requestBody, &job.State, &decisionBody, &job.Attempts, &createdAt); errors.Is(err, sql.ErrNoRows) {
		return propositionJob{}, false, nil
	} else if err != nil {
		return propositionJob{}, false, err
	}
	if err := json.Unmarshal(requestBody, &job.Request); err != nil {
		return propositionJob{}, false, fmt.Errorf("library: decode queued proposition: %w", err)
	}
	if len(decisionBody) > 0 {
		if err := json.Unmarshal(decisionBody, &job.Decision); err != nil {
			return propositionJob{}, false, fmt.Errorf("library: decode proposition decision: %w", err)
		}
	}
	job.CreatedAt = time.UnixMilli(createdAt).UTC()
	nextState := "running"
	if job.State == "decided" || job.State == "applying" {
		nextState = "applying"
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE proposition_jobs
SET state = ?, attempts = attempts + 1, lease_until = ?, updated_at = ?, last_error = NULL
WHERE idempotency_key = ?`, nextState, now.Add(propositionJobLease).UnixMilli(), now.UnixMilli(), job.Key); err != nil {
		return propositionJob{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return propositionJob{}, false, err
	}
	job.State = nextState
	job.Attempts++
	return job, true, nil
}

func (q *propositionQueue) saveDecision(ctx context.Context, key string, decision PropositionDecision) error {
	body, err := json.Marshal(decision)
	if err != nil {
		return err
	}
	_, err = q.db.ExecContext(ctx, `
UPDATE proposition_jobs SET state = 'decided', decision_json = ?, lease_until = NULL, updated_at = ?
WHERE idempotency_key = ?`, body, q.now().UTC().UnixMilli(), key)
	return err
}

func (q *propositionQueue) complete(ctx context.Context, job propositionJob, response PropositionRegisterResponse) error {
	body, err := json.Marshal(response)
	if err != nil {
		return err
	}
	now := q.now().UTC().UnixMilli()
	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO proposition_receipts(idempotency_key, request_digest, result_json, completed_at)
VALUES(?, ?, ?, ?)
ON CONFLICT(idempotency_key) DO NOTHING`, job.Key, job.Digest, body, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE proposition_jobs
SET state = 'succeeded', result_json = ?, lease_until = NULL, updated_at = ?, completed_at = ?
WHERE idempotency_key = ?`, body, now, now, job.Key); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	q.signal()
	return nil
}

func (q *propositionQueue) fail(ctx context.Context, key string, jobErr error) error {
	message := "proposition processing failed"
	if jobErr != nil {
		message = jobErr.Error()
	}
	_, err := q.db.ExecContext(ctx, `
UPDATE proposition_jobs
SET state = 'failed', last_error = ?, lease_until = NULL, updated_at = ?
WHERE idempotency_key = ?`, message, q.now().UTC().UnixMilli(), key)
	q.signal()
	return err
}

func (q *propositionQueue) cleanup(ctx context.Context) error {
	cutoff := q.now().UTC().Add(-propositionJobRetention).UnixMilli()
	_, err := q.db.ExecContext(ctx, `DELETE FROM proposition_jobs WHERE state = 'succeeded' AND completed_at < ?`, cutoff)
	return err
}

func (q *propositionQueue) signal() {
	select {
	case q.notify <- struct{}{}:
	default:
	}
}
