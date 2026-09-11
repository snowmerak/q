package usagelog

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/snowmerak/q/client"
)

func (s *Store) importLegacy(configDir string) error {
	directory := filepath.Join(configDir, "logs", "model-usage")
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("usage: list legacy logs: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "usage-") || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		if err := s.importLegacyFile(filepath.Join(directory, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) importLegacyFile(path string) error {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("usage: open legacy log: %w", err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return fmt.Errorf("usage: hash legacy log: %w", err)
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	cleanPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	cleanPath = filepath.Clean(cleanPath)
	var exists int
	err = s.db.QueryRow(`SELECT 1 FROM usage_imports WHERE source_path = ? AND source_sha256 = ?`, cleanPath, digest).Scan(&exists)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("usage: rewind legacy log: %w", err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	rows := 0
	invalid := 0
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := append([]byte(nil), scanner.Bytes()...)
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var record client.UsageRecord
		if err := json.Unmarshal(line, &record); err != nil {
			invalid++
			continue
		}
		record.EventID = legacyEventID(cleanPath, lineNumber, line)
		if record.Role == "" {
			record.Role = client.UsageRoleUnknown
		}
		if err := normalizeRecord(&record, s.currentTime()); err != nil {
			invalid++
			continue
		}
		inserted, err := appendRecord(context.Background(), tx, record)
		if err != nil {
			return err
		}
		if inserted {
			rows++
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("usage: scan legacy log: %w", err)
	}
	_, err = tx.Exec(`
INSERT INTO usage_imports(source_path, source_sha256, rows, invalid_rows, imported_at_ms)
VALUES(?, ?, ?, ?, ?)`, cleanPath, digest, rows, invalid, s.currentTime().UnixMilli())
	if err != nil {
		return fmt.Errorf("usage: record legacy import: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("usage: commit legacy import: %w", err)
	}
	return nil
}

func legacyEventID(path string, line int, body []byte) string {
	hash := sha256.New()
	_, _ = io.WriteString(hash, path)
	_, _ = fmt.Fprintf(hash, "\n%d\n", line)
	_, _ = hash.Write(body)
	return hex.EncodeToString(hash.Sum(nil))
}
