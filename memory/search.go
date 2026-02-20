package memory

import (
	"context"
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
	UserID     string
	ChannelID  string
	CreatedAt  time.Time
}

func (s *Store) Recall(ctx context.Context, query, serverID string, topN int) ([]MemoryRow, error) {
	var semanticIDs []string

	vec, err := s.llm.Embed(ctx, query)
	if err != nil {
		slog.Warn("embed failed, falling back to keyword-only search", "error", err)
	} else {
		embeddings, err := s.allEmbeddings(ctx, serverID)
		if err != nil {
			return nil, fmt.Errorf("load embeddings: %w", err)
		}
		type scored struct {
			id    string
			score float32
		}
		results := make([]scored, 0, len(embeddings))
		for id, emb := range embeddings {
			if len(emb) == len(vec) {
				results = append(results, scored{id, cosine(vec, emb)})
			}
		}
		sort.Slice(results, func(i, j int) bool {
			return results[i].score > results[j].score
		})
		semanticIDs = make([]string, len(results))
		for i, r := range results {
			semanticIDs[i] = r.id
		}
	}

	escaped := strings.ReplaceAll(query, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `%`, `\%`)
	escaped = strings.ReplaceAll(escaped, `_`, `\_`)
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM memories WHERE server_id = ? AND forgotten = 0 AND content LIKE ? ESCAPE '\'`,
		serverID, "%"+escaped+"%",
	)
	if err != nil {
		return nil, fmt.Errorf("keyword search: %w", err)
	}
	defer rows.Close()

	var keywordIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan keyword result: %w", err)
		}
		keywordIDs = append(keywordIDs, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	merged := rrfMerge(semanticIDs, keywordIDs)
	if len(merged) > topN {
		merged = merged[:topN]
	}

	out := make([]MemoryRow, 0, len(merged))
	for _, id := range merged {
		var row MemoryRow
		err := s.db.QueryRowContext(ctx,
			`SELECT id, content, importance, COALESCE(user_id, ''), COALESCE(channel_id, ''), created_at FROM memories WHERE id = ?`,
			id,
		).Scan(&row.ID, &row.Content, &row.Importance, &row.UserID, &row.ChannelID, &row.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("fetch memory %s: %w", id, err)
		}
		out = append(out, row)
	}
	return out, nil
}
