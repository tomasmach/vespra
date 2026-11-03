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
	"unicode"
	"unicode/utf8"
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
		keywordIDs, err = s.ftsSearch(ctx, query, serverID, topN)
		if err != nil {
			slog.Warn("fts search failed, falling back to LIKE", "error", err)
			keywordIDs, err = s.likeSearch(ctx, query, serverID, topN)
			if err != nil {
				return nil, fmt.Errorf("keyword search: %w", err)
			}
		}
	} else {
		keywordIDs, err = s.likeSearch(ctx, query, serverID, topN)
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

// minKeywordRunes is the shortest query word used for keyword search. Shorter
// words ("a", "co", "the") match nearly every memory.
const minKeywordRunes = 4

// keywordStopwords are common longer words that would otherwise make every
// message match a large share of memories.
var keywordStopwords = map[string]bool{
	"jsem": true, "jsme": true, "jste": true, "jsou": true, "není": true, "bude": true,
	"když": true, "jako": true, "taky": true, "také": true, "tady": true, "proč": true,
	"nebo": true, "tohle": true, "něco": true, "ještě": true, "prostě": true,
	"that": true, "this": true, "with": true, "have": true, "from": true, "what": true,
	"there": true, "they": true, "just": true, "like": true, "about": true, "your": true,
}

// keywordTokens splits a query into distinct words of letters and digits.
// Everything else, including FTS5 operators and quotes, separates words. Case is
// kept because SQLite LIKE folds only ASCII case; FTS5 folds case on its own.
func keywordTokens(query string) []string {
	words := strings.FieldsFunc(query, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.IsMark(r)
	})
	seen := make(map[string]bool, len(words))
	var tokens []string
	for _, w := range words {
		if utf8.RuneCountInString(w) < minKeywordRunes || seen[w] || keywordStopwords[strings.ToLower(w)] {
			continue
		}
		seen[w] = true
		tokens = append(tokens, w)
	}
	return tokens
}

// ftsSearch returns memory IDs that contain any query word as a word prefix,
// via FTS5 full-text search.
func (s *Store) ftsSearch(ctx context.Context, query, serverID string, limit int) ([]string, error) {
	tokens := keywordTokens(query)
	if len(tokens) == 0 {
		return nil, nil
	}
	terms := make([]string, len(tokens))
	for i, t := range tokens {
		terms[i] = `"` + t + `"*`
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT f.memory_id FROM memories_fts f
		 JOIN memories m ON m.id = f.memory_id
		 WHERE m.server_id = ? AND m.forgotten = 0
		 AND memories_fts MATCH ?
		 ORDER BY rank LIMIT ?`,
		serverID, strings.Join(terms, " OR "), limit,
	)
	if err != nil {
		return nil, err
	}
	return scanIDs(rows)
}

// likeSearch returns memory IDs whose content contains any query word, via SQL
// LIKE substring search. Used when FTS5 is unavailable or its search fails.
func (s *Store) likeSearch(ctx context.Context, query, serverID string, limit int) ([]string, error) {
	tokens := keywordTokens(query)
	if len(tokens) == 0 {
		return nil, nil
	}
	conditions := make([]string, len(tokens))
	args := []any{serverID}
	for i, t := range tokens {
		conditions[i] = `content LIKE ? ESCAPE '\'`
		args = append(args, "%"+escapeLIKE(t)+"%")
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM memories WHERE server_id = ? AND forgotten = 0 AND (`+strings.Join(conditions, " OR ")+`) LIMIT ?`,
		args...,
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
