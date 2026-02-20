// Package memory provides SQLite-backed persistent memory storage with hybrid search.
package memory

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/tomasmach/mnemon-bot/config"
	"github.com/tomasmach/mnemon-bot/llm"
)

const migrationSQL = `
CREATE TABLE IF NOT EXISTS memories (
    id          TEXT PRIMARY KEY,
    content     TEXT NOT NULL,
    importance  REAL DEFAULT 0.5,
    server_id   TEXT NOT NULL,
    user_id     TEXT,
    channel_id  TEXT,
    created_at  DATETIME NOT NULL,
    updated_at  DATETIME NOT NULL,
    forgotten   INTEGER DEFAULT 0
);

CREATE TABLE IF NOT EXISTS embeddings (
    memory_id   TEXT PRIMARY KEY REFERENCES memories(id) ON DELETE CASCADE,
    vector      BLOB NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_memories_server ON memories(server_id);
CREATE INDEX IF NOT EXISTS idx_memories_user   ON memories(server_id, user_id);
`

type Store struct {
	db  *sql.DB
	llm *llm.Client
}

func expandPath(path string) string {
	path = os.ExpandEnv(path)
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	return path
}

func New(cfg *config.MemoryConfig, llmClient *llm.Client) (*Store, error) {
	path := expandPath(cfg.DBPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}
	dsn := path + "?_foreign_keys=on&_journal_mode=WAL"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if _, err := db.ExecContext(context.Background(), migrationSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("run migration: %w", err)
	}
	return &Store{db: db, llm: llmClient}, nil
}

func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (s *Store) Save(ctx context.Context, content, serverID, userID, channelID string, importance float64) (string, error) {
	id, err := newID()
	if err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	now := time.Now().UTC()
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO memories (id, content, importance, server_id, user_id, channel_id, created_at, updated_at, forgotten)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0)`,
		id, content, importance, serverID, userID, channelID, now, now,
	)
	if err != nil {
		return "", fmt.Errorf("insert memory: %w", err)
	}

	vec, err := s.llm.Embed(ctx, content)
	if err != nil {
		slog.Warn("embed failed, skipping embedding", "error", err)
		return id, nil
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO embeddings (memory_id, vector) VALUES (?, ?)`,
		id, llm.VectorToBlob(vec),
	); err != nil {
		slog.Warn("insert embedding failed", "error", err)
	}
	return id, nil
}

func (s *Store) Forget(ctx context.Context, serverID, memoryID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE memories SET forgotten = 1, updated_at = ? WHERE id = ? AND server_id = ?`,
		time.Now().UTC(), memoryID, serverID,
	)
	return err
}

func (s *Store) allEmbeddings(ctx context.Context, serverID string) (map[string][]float32, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT e.memory_id, e.vector FROM embeddings e
		 JOIN memories m ON m.id = e.memory_id
		 WHERE m.server_id = ? AND m.forgotten = 0`,
		serverID,
	)
	if err != nil {
		return nil, fmt.Errorf("query embeddings: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]float32)
	for rows.Next() {
		var memID string
		var blob []byte
		if err := rows.Scan(&memID, &blob); err != nil {
			return nil, fmt.Errorf("scan embedding: %w", err)
		}
		result[memID] = llm.BlobToVector(blob)
	}
	return result, rows.Err()
}
