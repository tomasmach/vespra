// Package logstore provides SQLite-backed persistent storage for slog entries
// and a custom slog.Handler that tees log records to an inner handler and to the DB.
package logstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

const migrationSQL = `
CREATE TABLE IF NOT EXISTS logs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    ts         DATETIME NOT NULL,
    level      TEXT NOT NULL,
    msg        TEXT NOT NULL,
    server_id  TEXT,
    channel_id TEXT,
    attrs      TEXT
);
CREATE INDEX IF NOT EXISTS idx_logs_server ON logs(server_id);
`

// LogRow is a single log entry returned by List.
type LogRow struct {
	ID        int64     `json:"id"`
	CreatedAt time.Time `json:"ts"`
	Level     string    `json:"level"`
	Msg       string    `json:"msg"`
	ServerID  string    `json:"server_id,omitempty"`
	ChannelID string    `json:"channel_id,omitempty"`
	Attrs     string    `json:"attrs,omitempty"`
}

// Store persists slog records in SQLite.
type Store struct {
	db *sql.DB
}

// Open opens (or creates) the log store at dbPath.
func Open(dbPath string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("create log db dir: %w", err)
	}
	dsn := dbPath + "?_foreign_keys=on&_journal_mode=WAL"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open log db: %w", err)
	}
	if _, err := db.ExecContext(context.Background(), migrationSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("log db migration: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// write persists a single log entry. Silently discards errors — logging the error
// here would recurse back into slog. Prunes the table 1 in 500 writes to avoid O(N) overhead.
func (s *Store) write(ctx context.Context, ts time.Time, level, msg, serverID, channelID, attrsJSON string) {
	_, _ = s.db.ExecContext(ctx,
		`INSERT INTO logs (ts, level, msg, server_id, channel_id, attrs) VALUES (?, ?, ?, ?, ?, ?)`,
		ts, level, msg, serverID, channelID, attrsJSON,
	)
	if rand.IntN(500) == 0 {
		// Use context.Background(): prune is a maintenance operation that should
		// not be cancelled by the short-lived request context that triggered the write.
		s.prune(context.Background())
	}
}

// prune keeps at most 10 000 rows per server_id by deleting the oldest excess rows.
func (s *Store) prune(ctx context.Context) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT server_id FROM logs`)
	if err != nil {
		return
	}
	defer rows.Close()

	var serverIDs []string
	for rows.Next() {
		var sid string
		if err := rows.Scan(&sid); err != nil {
			continue
		}
		serverIDs = append(serverIDs, sid)
	}
	rows.Close()

	for _, sid := range serverIDs {
		_, _ = s.db.ExecContext(ctx,
			`DELETE FROM logs WHERE server_id = ? AND id NOT IN (SELECT id FROM logs WHERE server_id = ? ORDER BY id DESC LIMIT 10000)`,
			sid, sid,
		)
	}
}

// List returns log rows for a server, optionally filtered by minimum level.
// level may be "debug", "info", "warn", "error", or "" (no filter).
func (s *Store) List(ctx context.Context, serverID, level string, limit, offset int) ([]LogRow, int, error) {
	if limit == 0 {
		limit = 100
	}

	where := "server_id = ?"
	args := []any{serverID}

	if level != "" {
		levels := map[string]int{"debug": -4, "info": 0, "warn": 4, "error": 8}
		if n, ok := levels[level]; ok {
			where += " AND CASE level WHEN 'DEBUG' THEN -4 WHEN 'INFO' THEN 0 WHEN 'WARN' THEN 4 WHEN 'ERROR' THEN 8 ELSE 0 END >= ?"
			args = append(args, n)
		}
	}

	var total int
	if err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM logs WHERE "+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count logs: %w", err)
	}

	rows, err := s.db.QueryContext(ctx,
		"SELECT id, ts, level, msg, COALESCE(server_id,''), COALESCE(channel_id,''), COALESCE(attrs,'') FROM logs WHERE "+where+
			" ORDER BY id DESC LIMIT ? OFFSET ?",
		append(args, limit, offset)...,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("list logs: %w", err)
	}
	defer rows.Close()

	var out []LogRow
	for rows.Next() {
		var r LogRow
		if err := rows.Scan(&r.ID, &r.CreatedAt, &r.Level, &r.Msg, &r.ServerID, &r.ChannelID, &r.Attrs); err != nil {
			return nil, 0, fmt.Errorf("scan log row: %w", err)
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// Handler is a slog.Handler that tees records to an inner handler and to a Store.
// Attrs added via WithAttrs are accumulated so that server_id/channel_id are
// available even when they were attached before the log call.
type Handler struct {
	inner    slog.Handler
	store    *Store
	preAttrs map[string]string // flat attrs accumulated via WithAttrs
}

// NewHandler wraps inner with a tee to store.
func NewHandler(inner slog.Handler, store *Store) *Handler {
	return &Handler{inner: inner, store: store, preAttrs: make(map[string]string)}
}

func (h *Handler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	child := &Handler{
		inner:    h.inner.WithAttrs(attrs),
		store:    h.store,
		preAttrs: copyMap(h.preAttrs),
	}
	for _, a := range attrs {
		// Use Value.String() for all kinds so non-string values are still captured.
		child.preAttrs[a.Key] = a.Value.String()
	}
	return child
}

func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{
		inner:    h.inner.WithGroup(name),
		store:    h.store,
		preAttrs: copyMap(h.preAttrs),
	}
}

func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	if err := h.inner.Handle(ctx, r); err != nil {
		return err
	}

	serverID := h.preAttrs["server_id"]
	channelID := h.preAttrs["channel_id"]

	extra := make(map[string]any)
	r.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case "server_id":
			serverID = a.Value.String()
		case "channel_id":
			channelID = a.Value.String()
		default:
			extra[a.Key] = a.Value.Any()
		}
		return true
	})

	var attrsJSON string
	if len(extra) > 0 {
		b, _ := json.Marshal(extra)
		attrsJSON = string(b)
	}

	h.store.write(ctx, r.Time, r.Level.String(), r.Message, serverID, channelID, attrsJSON)
	return nil
}

func copyMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
