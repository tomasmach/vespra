package memory

import (
	"context"
	"fmt"
	"sort"
)

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
