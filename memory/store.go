// Package memory provides SQLite-backed persistent memory storage with hybrid search.
package memory

import (
	"context"
	crand "crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math/rand/v2"
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

CREATE TABLE IF NOT EXISTS conversations (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    channel_id TEXT NOT NULL,
    user_msg   TEXT NOT NULL,
    tool_calls TEXT,  -- JSON array [{name, result}]
    response   TEXT NOT NULL,
    ts         DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_conv_channel ON conversations(channel_id);
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
	if _, err := crand.Read(b); err != nil {
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

type ListOptions struct {
	ServerID string
	UserID   string
	Limit    int
	Offset   int
	Query    string
}

func (s *Store) List(ctx context.Context, opts ListOptions) ([]MemoryRow, int, error) {
	if opts.ServerID == "" {
		return nil, 0, fmt.Errorf("ServerID is required")
	}
	if opts.Limit == 0 {
		opts.Limit = 50
	}

	where := "server_id = ? AND forgotten = 0"
	args := []any{opts.ServerID}

	if opts.UserID != "" {
		where += " AND user_id = ?"
		args = append(args, opts.UserID)
	}
	if opts.Query != "" {
		escaped := strings.ReplaceAll(opts.Query, `\`, `\\`)
		escaped = strings.ReplaceAll(escaped, `%`, `\%`)
		escaped = strings.ReplaceAll(escaped, `_`, `\_`)
		where += ` AND content LIKE ? ESCAPE '\'`
		args = append(args, "%"+escaped+"%")
	}

	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM memories WHERE "+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count memories: %w", err)
	}

	rows, err := s.db.QueryContext(ctx,
		"SELECT id, content, importance, server_id, COALESCE(user_id, ''), COALESCE(channel_id, ''), created_at FROM memories WHERE "+where+" ORDER BY created_at DESC LIMIT ? OFFSET ?",
		append(args, opts.Limit, opts.Offset)...,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("list memories: %w", err)
	}
	defer rows.Close()

	var out []MemoryRow
	for rows.Next() {
		var row MemoryRow
		if err := rows.Scan(&row.ID, &row.Content, &row.Importance, &row.ServerID, &row.UserID, &row.ChannelID, &row.CreatedAt); err != nil {
			return nil, 0, fmt.Errorf("scan memory: %w", err)
		}
		out = append(out, row)
	}
	return out, total, rows.Err()
}

func (s *Store) UpdateContent(ctx context.Context, id, serverID, content string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE memories SET content = ?, updated_at = ? WHERE id = ? AND server_id = ? AND forgotten = 0`,
		content, time.Now().UTC(), id, serverID,
	)
	return err
}

// ConversationRow holds a single persisted conversation turn returned by ListConversations.
type ConversationRow struct {
	ID        int64     `json:"id"`
	ChannelID string    `json:"channel_id"`
	UserMsg   string    `json:"user_msg"`
	ToolCalls string    `json:"tool_calls,omitempty"`
	Response  string    `json:"response"`
	CreatedAt time.Time `json:"ts"`
}

// LogConversation inserts a single conversation turn into the conversations table.
// toolCallsJSON may be empty if the LLM produced no tool calls.
// Prunes the table 1 in 500 writes to keep it at most 10 000 rows.
func (s *Store) LogConversation(ctx context.Context, channelID, userMsg, toolCallsJSON, response string) error {
	var tc any
	if toolCallsJSON != "" {
		tc = toolCallsJSON
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO conversations (channel_id, user_msg, tool_calls, response, ts) VALUES (?, ?, ?, ?, ?)`,
		channelID, userMsg, tc, response, time.Now().UTC(),
	)
	if err != nil {
		return fmt.Errorf("insert conversation: %w", err)
	}
	if rand.IntN(500) == 0 {
		// Use context.Background(): prune is a maintenance operation that should
		// not be cancelled by the short-lived request context that triggered the write.
		_, _ = s.db.ExecContext(context.Background(),
			`DELETE FROM conversations WHERE id NOT IN (SELECT id FROM conversations ORDER BY id DESC LIMIT 10000)`,
		)
	}
	return nil
}

// ListConversations returns conversation rows, optionally filtered by channelID.
// Pass an empty channelID to list across all channels. Returns the total row count
// (before pagination) alongside the page of results.
func (s *Store) ListConversations(ctx context.Context, channelID string, limit, offset int) ([]ConversationRow, int, error) {
	if limit == 0 {
		limit = 50
	}

	var (
		where string
		args  []any
	)
	if channelID != "" {
		where = "WHERE channel_id = ?"
		args = []any{channelID}
	}

	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM conversations "+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count conversations: %w", err)
	}

	rows, err := s.db.QueryContext(ctx,
		"SELECT id, channel_id, user_msg, COALESCE(tool_calls, ''), response, ts FROM conversations "+where+" ORDER BY id DESC LIMIT ? OFFSET ?",
		append(args, limit, offset)...,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("list conversations: %w", err)
	}
	defer rows.Close()

	var out []ConversationRow
	for rows.Next() {
		var row ConversationRow
		if err := rows.Scan(&row.ID, &row.ChannelID, &row.UserMsg, &row.ToolCalls, &row.Response, &row.CreatedAt); err != nil {
			return nil, 0, fmt.Errorf("scan conversation: %w", err)
		}
		out = append(out, row)
	}
	return out, total, rows.Err()
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
