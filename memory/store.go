// Package memory provides SQLite-backed persistent memory storage with hybrid search.
package memory

import (
	"context"
	crand "crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/tomasmach/vespra/config"
	"github.com/tomasmach/vespra/llm"
)

// ErrMemoryNotFound is returned when a memory operation targets an ID that does
// not exist or belongs to a different server.
var ErrMemoryNotFound = errors.New("memory not found")

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
	db          *sql.DB
	llm         *llm.Client
	fts5Enabled bool // true when the SQLite build includes FTS5 support
}

func New(cfg *config.MemoryConfig, llmClient *llm.Client) (*Store, error) {
	path := config.ExpandPath(cfg.DBPath)
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

	// Attempt to create the FTS5 virtual table. FTS5 requires the SQLite binary
	// to be compiled with SQLITE_ENABLE_FTS5 (via -tags sqlite_fts5 at build time).
	// If FTS5 is not available, we fall back to LIKE-based keyword search transparently.
	fts5Enabled := true
	if _, err := db.ExecContext(context.Background(),
		`CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(memory_id UNINDEXED, content)`,
	); err != nil {
		slog.Warn("FTS5 not available, falling back to LIKE keyword search (rebuild with -tags sqlite_fts5 for better search)", "error", err)
		fts5Enabled = false
	}

	s := &Store{db: db, llm: llmClient, fts5Enabled: fts5Enabled}

	if fts5Enabled {
		// Backfill FTS index for existing memories that predate FTS5 migration.
		var missing int
		if err := db.QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM memories WHERE forgotten = 0 AND id NOT IN (SELECT memory_id FROM memories_fts)`,
		).Scan(&missing); err != nil {
			db.Close()
			return nil, fmt.Errorf("count missing fts entries: %w", err)
		}
		if missing > 0 {
			if _, err := db.ExecContext(context.Background(),
				`INSERT INTO memories_fts(memory_id, content)
				 SELECT id, content FROM memories WHERE forgotten = 0 AND id NOT IN (SELECT memory_id FROM memories_fts)`,
			); err != nil {
				db.Close()
				return nil, fmt.Errorf("backfill fts index: %w", err)
			}
		}
	}

	return s, nil
}

func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := crand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (s *Store) Save(ctx context.Context, content, serverID, userID, channelID string, importance float64, dedupThreshold float64) (SaveResult, error) {
	vec, embedErr := s.llm.Embed(ctx, content)
	if embedErr != nil {
		slog.Warn("embed failed, skipping embedding", "error", embedErr)
	}

	// Dedup check: find existing similar memory if embedding succeeded and threshold > 0.
	if embedErr == nil && dedupThreshold > 0 {
		match, err := s.findSimilar(ctx, serverID, vec, dedupThreshold)
		if err != nil {
			slog.Warn("dedup check failed, saving as new", "error", err)
		} else if match != nil {
			var existingContent string
			err := s.db.QueryRowContext(ctx,
				`SELECT content FROM memories WHERE id = ? AND server_id = ? AND forgotten = 0`,
				match.id, serverID,
			).Scan(&existingContent)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				slog.Warn("dedup fetch failed, saving as new", "error", err)
			} else if err == nil {
				if len(content) > len(existingContent) {
					if err := s.updateForDedup(ctx, match.id, serverID, content, importance, vec); err != nil {
						return SaveResult{}, fmt.Errorf("dedup update: %w", err)
					}
					return SaveResult{ID: match.id, Status: "updated"}, nil
				}
				return SaveResult{ID: match.id, Status: "exists"}, nil
			}
		}
	}

	id, err := newID()
	if err != nil {
		return SaveResult{}, fmt.Errorf("generate id: %w", err)
	}
	now := time.Now().UTC()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SaveResult{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err = tx.ExecContext(ctx,
		`INSERT INTO memories (id, content, importance, server_id, user_id, channel_id, created_at, updated_at, forgotten)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0)`,
		id, content, importance, serverID, userID, channelID, now, now,
	); err != nil {
		return SaveResult{}, fmt.Errorf("insert memory: %w", err)
	}

	if embedErr == nil {
		if _, err = tx.ExecContext(ctx,
			`INSERT INTO embeddings (memory_id, vector) VALUES (?, ?)`,
			id, llm.VectorToBlob(vec),
		); err != nil {
			return SaveResult{}, fmt.Errorf("insert embedding: %w", err)
		}
	}

	if s.fts5Enabled {
		if _, err = tx.ExecContext(ctx,
			`INSERT INTO memories_fts(memory_id, content) VALUES (?, ?)`,
			id, content,
		); err != nil {
			return SaveResult{}, fmt.Errorf("insert fts: %w", err)
		}
	}

	if err = tx.Commit(); err != nil {
		return SaveResult{}, fmt.Errorf("commit transaction: %w", err)
	}
	return SaveResult{ID: id, Status: "saved"}, nil
}

// updateForDedup updates an existing memory's content, importance, embedding, and FTS entry.
func (s *Store) updateForDedup(ctx context.Context, id, serverID, content string, importance float64, vec []float32) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	result, err := tx.ExecContext(ctx,
		`UPDATE memories SET content = ?, importance = ?, updated_at = ? WHERE id = ? AND server_id = ? AND forgotten = 0`,
		content, importance, time.Now().UTC(), id, serverID,
	)
	if err != nil {
		return fmt.Errorf("update memory: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check rows affected: %w", err)
	}
	if n == 0 {
		return ErrMemoryNotFound
	}

	if _, err = tx.ExecContext(ctx,
		`INSERT INTO embeddings (memory_id, vector) VALUES (?, ?)
		 ON CONFLICT(memory_id) DO UPDATE SET vector = excluded.vector`,
		id, llm.VectorToBlob(vec),
	); err != nil {
		return fmt.Errorf("upsert embedding: %w", err)
	}

	if s.fts5Enabled {
		if _, err = tx.ExecContext(ctx,
			`DELETE FROM memories_fts WHERE memory_id = ?`, id,
		); err != nil {
			return fmt.Errorf("delete old fts: %w", err)
		}
		if _, err = tx.ExecContext(ctx,
			`INSERT INTO memories_fts(memory_id, content) VALUES (?, ?)`, id, content,
		); err != nil {
			return fmt.Errorf("insert new fts: %w", err)
		}
	}

	return tx.Commit()
}

func (s *Store) Forget(ctx context.Context, serverID, memoryID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	result, err := tx.ExecContext(ctx,
		`UPDATE memories SET forgotten = 1, updated_at = ? WHERE id = ? AND server_id = ?`,
		time.Now().UTC(), memoryID, serverID,
	)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check rows affected: %w", err)
	}
	if n == 0 {
		return ErrMemoryNotFound
	}

	if s.fts5Enabled {
		if _, err = tx.ExecContext(ctx,
			`DELETE FROM memories_fts WHERE memory_id = ?`, memoryID,
		); err != nil {
			return fmt.Errorf("delete fts: %w", err)
		}
	}

	return tx.Commit()
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
		where += ` AND content LIKE ? ESCAPE '\'`
		args = append(args, "%"+escapeLIKE(opts.Query)+"%")
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
	vec, err := s.llm.Embed(ctx, content)
	if err != nil {
		return fmt.Errorf("embed content: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	result, err := tx.ExecContext(ctx,
		`UPDATE memories SET content = ?, updated_at = ? WHERE id = ? AND server_id = ? AND forgotten = 0`,
		content, time.Now().UTC(), id, serverID,
	)
	if err != nil {
		return fmt.Errorf("update memory: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check rows affected: %w", err)
	}
	if n == 0 {
		return ErrMemoryNotFound
	}

	if _, err = tx.ExecContext(ctx,
		`INSERT INTO embeddings (memory_id, vector) VALUES (?, ?)
		 ON CONFLICT(memory_id) DO UPDATE SET vector = excluded.vector`,
		id, llm.VectorToBlob(vec),
	); err != nil {
		return fmt.Errorf("upsert embedding: %w", err)
	}

	if s.fts5Enabled {
		if _, err = tx.ExecContext(ctx,
			`DELETE FROM memories_fts WHERE memory_id = ?`, id,
		); err != nil {
			return fmt.Errorf("delete old fts: %w", err)
		}
		if _, err = tx.ExecContext(ctx,
			`INSERT INTO memories_fts(memory_id, content) VALUES (?, ?)`, id, content,
		); err != nil {
			return fmt.Errorf("insert new fts: %w", err)
		}
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
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

// escapeLIKE escapes SQL LIKE special characters (%, _, \) for literal matching.
func escapeLIKE(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
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

// similarMatch holds a memory ID and its cosine similarity score.
type similarMatch struct {
	id    string
	score float32
}

// findSimilar returns the single best matching memory above the threshold
// for the given server. Returns nil if no match exceeds the threshold.
func (s *Store) findSimilar(ctx context.Context, serverID string, vec []float32, threshold float64) (*similarMatch, error) {
	embeddings, err := s.allEmbeddings(ctx, serverID)
	if err != nil {
		return nil, fmt.Errorf("load embeddings: %w", err)
	}

	type scored struct {
		id    string
		score float32
	}
	var candidates []scored
	for id, emb := range embeddings {
		if len(emb) != len(vec) {
			continue
		}
		sim := cosine(vec, emb)
		if float64(sim) >= threshold {
			candidates = append(candidates, scored{id, sim})
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})
	return &similarMatch{id: candidates[0].id, score: candidates[0].score}, nil
}
