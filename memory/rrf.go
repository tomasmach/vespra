package memory

import (
	"math"
	"sort"
)

// cosine returns cosine similarity between two equal-length float32 vectors.
func cosine(a, b []float32) float32 {
	var dot, normA, normB float32
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (sqrt32(normA) * sqrt32(normB))
}

func sqrt32(x float32) float32 {
	return float32(math.Sqrt(float64(x)))
}

// rrfMerge merges two ranked ID lists using Reciprocal Rank Fusion with k=60.
// Returns merged IDs sorted by descending RRF score.
func rrfMerge(semantic, keyword []string) []string {
	scores := make(map[string]float64)
	for rank, id := range semantic {
		scores[id] += 1.0 / float64(60+rank+1)
	}
	for rank, id := range keyword {
		scores[id] += 1.0 / float64(60+rank+1)
	}
	ids := make([]string, 0, len(scores))
	for id := range scores {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		return scores[ids[i]] > scores[ids[j]]
	})
	return ids
}
