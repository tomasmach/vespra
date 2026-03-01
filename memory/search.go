package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
)

type MemoryRow struct {
	ID         string
	Content    string
	Importance float64
	ServerID   string
	UserID     string
	ChannelID  string
	CreatedAt  time.Time
}

// SaveResult holds the outcome of a Save operation.
type SaveResult struct {
	ID     string // memory ID (new or existing)
	Status string // one of the SaveStatus* constants
}

// SaveStatus constants for SaveResult.Status.
const (
	SaveStatusSaved   = "saved"
	SaveStatusUpdated = "updated"
	SaveStatusExists  = "exists"
)

// scored pairs a memory ID with its cosine similarity score.
type scored struct {
	id    string
	score float32
}

func (s *Store) Recall(ctx context.Context, query, serverID string, topN int, simThreshold float64) ([]MemoryRow, error) {
	var semanticIDs []string

	vec, err := s.llm.Embed(ctx, query)
	if err != nil {
		slog.Warn("embed failed, falling back to keyword-only search", "error", err)
	} else {
		embeddings, err := s.allEmbeddings(ctx, serverID)
		if err != nil {
			return nil, fmt.Errorf("load embeddings: %w", err)
		}
		results := make([]scored, 0, len(embeddings))
		for id, emb := range embeddings {
			if len(emb) != len(vec) {
				continue
			}
			sim := cosine(vec, emb)
			if simThreshold > 0 && float64(sim) < simThreshold {
				continue
			}
			results = append(results, scored{id, sim})
		}
		sort.Slice(results, func(i, j int) bool {
			return results[i].score > results[j].score
		})
		semanticIDs = make([]string, len(results))
		for i, r := range results {
			semanticIDs[i] = r.id
		}
	}

	// Keyword search: use FTS5 when available, otherwise fall back to LIKE.
	var keywordIDs []string
	if s.fts5Enabled {
		keywordIDs, err = s.ftsSearch(ctx, query, serverID)
		if err != nil {
			slog.Warn("fts search failed, falling back to LIKE", "error", err)
			keywordIDs, err = s.likeSearch(ctx, query, serverID)
			if err != nil {
				return nil, fmt.Errorf("keyword search: %w", err)
			}
		}
	} else {
		keywordIDs, err = s.likeSearch(ctx, query, serverID)
		if err != nil {
			return nil, fmt.Errorf("keyword search: %w", err)
		}
	}

	merged := rrfMerge(semanticIDs, keywordIDs)
	if len(merged) > topN {
		merged = merged[:topN]
	}

	out := make([]MemoryRow, 0, len(merged))
	for _, id := range merged {
		var row MemoryRow
		err := s.db.QueryRowContext(ctx,
			`SELECT id, content, importance, COALESCE(user_id, ''), COALESCE(channel_id, ''), created_at
			 FROM memories WHERE id = ? AND server_id = ? AND forgotten = 0`,
			id, serverID,
		).Scan(&row.ID, &row.Content, &row.Importance, &row.UserID, &row.ChannelID, &row.CreatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("fetch memory %s: %w", id, err)
		}
		out = append(out, row)
	}
	return out, nil
}

// RecallByUser returns memories associated with a specific user, ordered by
// importance (descending) then recency. Used for user-specific recall pass.
func (s *Store) RecallByUser(ctx context.Context, serverID, userID string, limit int) ([]MemoryRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, content, importance, COALESCE(user_id, ''), COALESCE(channel_id, ''), created_at
		 FROM memories
		 WHERE server_id = ? AND user_id = ? AND forgotten = 0
		 ORDER BY importance DESC, updated_at DESC
		 LIMIT ?`,
		serverID, userID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("recall by user: %w", err)
	}
	defer rows.Close()

	var out []MemoryRow
	for rows.Next() {
		var row MemoryRow
		if err := rows.Scan(&row.ID, &row.Content, &row.Importance, &row.UserID, &row.ChannelID, &row.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan memory: %w", err)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// ftsSearch returns memory IDs matching the query via FTS5 full-text search.
func (s *Store) ftsSearch(ctx context.Context, query, serverID string) ([]string, error) {
	// FTS5 MATCH uses its own syntax (AND, OR, -, ", *, NEAR). Wrap the query
	// in double quotes to treat it as a phrase, escaping embedded quotes.
	ftsQuery := `"` + strings.ReplaceAll(query, `"`, `""`) + `"`
	rows, err := s.db.QueryContext(ctx,
		`SELECT f.memory_id FROM memories_fts f
		 JOIN memories m ON m.id = f.memory_id
		 WHERE m.server_id = ? AND m.forgotten = 0
		 AND memories_fts MATCH ?
		 ORDER BY rank`,
		serverID, ftsQuery,
	)
	if err != nil {
		return nil, err
	}
	return scanIDs(rows)
}

// likeSearch returns memory IDs matching the query via SQL LIKE substring search.
// Used as fallback when FTS5 search fails (e.g., syntax errors in query).
func (s *Store) likeSearch(ctx context.Context, query, serverID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM memories WHERE server_id = ? AND forgotten = 0 AND content LIKE ? ESCAPE '\'`,
		serverID, "%"+escapeLIKE(query)+"%",
	)
	if err != nil {
		return nil, err
	}
	return scanIDs(rows)
}

// scanIDs collects a single string column from each row and returns them as a slice.
func scanIDs(rows *sql.Rows) ([]string, error) {
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
