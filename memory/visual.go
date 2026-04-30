package memory

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxVisualImageBytes       = 10 * 1024 * 1024
	maxVisualMemoriesPerLabel = 5
)

// VisualSaveOptions describes an image reference to persist as visual memory.
type VisualSaveOptions struct {
	Label       string
	Description string
	ServerID    string
	UserID      string
	ChannelID   string
	MessageID   string
	ContentType string
	Data        []byte
	Importance  float64
}

// VisualListOptions filters visual memories returned by ListVisual.
type VisualListOptions struct {
	ServerID string
	UserID   string
	Query    string
	Limit    int
	Offset   int
}

// VisualMemoryRow is metadata for a persisted visual reference.
type VisualMemoryRow struct {
	ID              string    `json:"id"`
	Label           string    `json:"label"`
	NormalizedLabel string    `json:"normalized_label"`
	Description     string    `json:"description,omitempty"`
	Importance      float64   `json:"importance"`
	ServerID        string    `json:"server_id"`
	UserID          string    `json:"user_id,omitempty"`
	ChannelID       string    `json:"channel_id,omitempty"`
	MessageID       string    `json:"message_id,omitempty"`
	ContentType     string    `json:"content_type"`
	FilePath        string    `json:"-"`
	SHA256          string    `json:"sha256"`
	SizeBytes       int64     `json:"size_bytes"`
	CreatedAt       time.Time `json:"created_at"`
}

func normalizeVisualLabel(label string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(label)), " "))
}

func visualFileExtension(contentType string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(contentType)) {
	case "image/jpeg", "image/jpg":
		return ".jpg", nil
	case "image/png":
		return ".png", nil
	case "image/webp":
		return ".webp", nil
	case "image/gif":
		return ".gif", nil
	default:
		return "", fmt.Errorf("unsupported visual memory content type %q", contentType)
	}
}

// SaveVisual persists a user-provided reference image and searchable metadata.
func (s *Store) SaveVisual(ctx context.Context, opts VisualSaveOptions) (SaveResult, error) {
	label := strings.TrimSpace(opts.Label)
	normalizedLabel := normalizeVisualLabel(label)
	if label == "" || normalizedLabel == "" {
		return SaveResult{}, fmt.Errorf("label is required")
	}
	if opts.ServerID == "" {
		return SaveResult{}, fmt.Errorf("serverID is required")
	}
	if len(opts.Data) == 0 {
		return SaveResult{}, fmt.Errorf("image data is required")
	}
	if len(opts.Data) > maxVisualImageBytes {
		return SaveResult{}, fmt.Errorf("image exceeds %d bytes", maxVisualImageBytes)
	}
	ext, err := visualFileExtension(opts.ContentType)
	if err != nil {
		return SaveResult{}, err
	}
	importance := opts.Importance
	if importance == 0 {
		importance = 0.5
	}

	sum := sha256.Sum256(opts.Data)
	hash := hex.EncodeToString(sum[:])
	var existingID string
	err = s.db.QueryRowContext(ctx,
		`SELECT id FROM visual_memories
		 WHERE server_id = ? AND normalized_label = ? AND sha256 = ? AND forgotten = 0
		 ORDER BY created_at DESC LIMIT 1`,
		opts.ServerID, normalizedLabel, hash,
	).Scan(&existingID)
	if err == nil {
		return SaveResult{ID: existingID, Status: SaveStatusExists}, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return SaveResult{}, fmt.Errorf("check visual duplicate: %w", err)
	}

	id, err := newID()
	if err != nil {
		return SaveResult{}, fmt.Errorf("generate id: %w", err)
	}
	if err := os.MkdirAll(s.mediaDir, 0o755); err != nil {
		return SaveResult{}, fmt.Errorf("create visual media dir: %w", err)
	}
	filePath := filepath.Join(s.mediaDir, id+ext)
	if err := os.WriteFile(filePath, opts.Data, 0o600); err != nil {
		return SaveResult{}, fmt.Errorf("write visual memory file: %w", err)
	}

	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		_ = os.Remove(filePath)
		return SaveResult{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err = tx.ExecContext(ctx,
		`INSERT INTO visual_memories
		 (id, label, normalized_label, description, importance, server_id, user_id, channel_id, message_id, content_type, file_path, sha256, size_bytes, created_at, updated_at, forgotten)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`,
		id, label, normalizedLabel, strings.TrimSpace(opts.Description), importance, opts.ServerID, opts.UserID, opts.ChannelID, opts.MessageID,
		strings.ToLower(opts.ContentType), filePath, hash, len(opts.Data), now, now,
	); err != nil {
		_ = os.Remove(filePath)
		return SaveResult{}, fmt.Errorf("insert visual memory: %w", err)
	}
	if err = tx.Commit(); err != nil {
		_ = os.Remove(filePath)
		return SaveResult{}, fmt.Errorf("commit visual memory: %w", err)
	}

	if err := s.pruneVisualLabel(ctx, opts.ServerID, normalizedLabel); err != nil {
		slog.Warn("prune visual memories failed", "error", err, "server_id", opts.ServerID, "label", normalizedLabel)
	}
	return SaveResult{ID: id, Status: SaveStatusSaved}, nil
}

func (s *Store) pruneVisualLabel(ctx context.Context, serverID, normalizedLabel string) error {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, file_path FROM visual_memories
		 WHERE server_id = ? AND normalized_label = ? AND forgotten = 0
		 ORDER BY created_at DESC LIMIT -1 OFFSET ?`,
		serverID, normalizedLabel, maxVisualMemoriesPerLabel,
	)
	if err != nil {
		return fmt.Errorf("query old visual memories: %w", err)
	}
	defer rows.Close()

	type oldVisual struct {
		id       string
		filePath string
	}
	var oldRows []oldVisual
	for rows.Next() {
		var old oldVisual
		if err := rows.Scan(&old.id, &old.filePath); err != nil {
			return fmt.Errorf("scan old visual memory: %w", err)
		}
		oldRows = append(oldRows, old)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, old := range oldRows {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE visual_memories SET forgotten = 1, updated_at = ? WHERE id = ? AND server_id = ?`,
			time.Now().UTC(), old.id, serverID,
		); err != nil {
			return fmt.Errorf("forget old visual memory: %w", err)
		}
		if err := os.Remove(old.filePath); err != nil && !os.IsNotExist(err) {
			slog.Warn("remove pruned visual memory file failed", "error", err, "path", old.filePath)
		}
	}
	return nil
}

// ListVisual returns visual memories, optionally filtered by user and query.
func (s *Store) ListVisual(ctx context.Context, opts VisualListOptions) ([]VisualMemoryRow, int, error) {
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
		q := "%" + escapeLIKE(normalizeVisualLabel(opts.Query)) + "%"
		where += ` AND (normalized_label LIKE ? ESCAPE '\' OR lower(description) LIKE ? ESCAPE '\')`
		args = append(args, q, q)
	}

	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM visual_memories WHERE "+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count visual memories: %w", err)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, label, normalized_label, COALESCE(description, ''), importance, server_id, COALESCE(user_id, ''),
		        COALESCE(channel_id, ''), COALESCE(message_id, ''), content_type, file_path, sha256, size_bytes, created_at
		 FROM visual_memories WHERE `+where+` ORDER BY created_at DESC LIMIT ? OFFSET ?`,
		append(args, opts.Limit, opts.Offset)...,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("list visual memories: %w", err)
	}
	defer rows.Close()
	out, err := scanVisualRows(rows)
	if err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// RecallVisual returns top visual memories for a query.
func (s *Store) RecallVisual(ctx context.Context, query, serverID string, topN int) ([]VisualMemoryRow, error) {
	if topN == 0 {
		topN = maxVisualMemoriesPerLabel
	}
	rows, _, err := s.ListVisual(ctx, VisualListOptions{ServerID: serverID, Query: query, Limit: topN})
	return rows, err
}

// GetVisual returns one active visual memory by ID.
func (s *Store) GetVisual(ctx context.Context, serverID, id string) (VisualMemoryRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, label, normalized_label, COALESCE(description, ''), importance, server_id, COALESCE(user_id, ''),
		        COALESCE(channel_id, ''), COALESCE(message_id, ''), content_type, file_path, sha256, size_bytes, created_at
		 FROM visual_memories WHERE id = ? AND server_id = ? AND forgotten = 0`,
		id, serverID,
	)
	if err != nil {
		return VisualMemoryRow{}, fmt.Errorf("get visual memory: %w", err)
	}
	defer rows.Close()
	out, err := scanVisualRows(rows)
	if err != nil {
		return VisualMemoryRow{}, err
	}
	if len(out) == 0 {
		return VisualMemoryRow{}, ErrMemoryNotFound
	}
	return out[0], nil
}

// ForgetVisual soft-deletes a visual memory and removes its stored file.
func (s *Store) ForgetVisual(ctx context.Context, serverID, id string) error {
	row, err := s.GetVisual(ctx, serverID, id)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx,
		`UPDATE visual_memories SET forgotten = 1, updated_at = ? WHERE id = ? AND server_id = ? AND forgotten = 0`,
		time.Now().UTC(), id, serverID,
	)
	if err != nil {
		return fmt.Errorf("forget visual memory: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check rows affected: %w", err)
	}
	if n == 0 {
		return ErrMemoryNotFound
	}
	if err := os.Remove(row.FilePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove visual memory file: %w", err)
	}
	return nil
}

type visualScanner interface {
	Scan(dest ...any) error
}

func scanVisualRows(rows *sql.Rows) ([]VisualMemoryRow, error) {
	var out []VisualMemoryRow
	for rows.Next() {
		var row VisualMemoryRow
		if err := scanVisualRow(rows, &row); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func scanVisualRow(scanner visualScanner, row *VisualMemoryRow) error {
	if err := scanner.Scan(
		&row.ID,
		&row.Label,
		&row.NormalizedLabel,
		&row.Description,
		&row.Importance,
		&row.ServerID,
		&row.UserID,
		&row.ChannelID,
		&row.MessageID,
		&row.ContentType,
		&row.FilePath,
		&row.SHA256,
		&row.SizeBytes,
		&row.CreatedAt,
	); err != nil {
		return fmt.Errorf("scan visual memory: %w", err)
	}
	return nil
}
