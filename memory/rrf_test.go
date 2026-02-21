package memory

import (
	"math"
	"testing"
)

func TestCosine(t *testing.T) {
	tests := []struct {
		name string
		a, b []float32
		want float32
	}{
		{"identical vectors", []float32{1, 0, 0}, []float32{1, 0, 0}, 1.0},
		{"orthogonal vectors", []float32{1, 0}, []float32{0, 1}, 0.0},
		{"opposite vectors", []float32{1, 0}, []float32{-1, 0}, -1.0},
		{"zero vector a", []float32{0, 0}, []float32{1, 0}, 0.0},
		{"zero vector b", []float32{1, 0}, []float32{0, 0}, 0.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cosine(tt.a, tt.b)
			if math.Abs(float64(got-tt.want)) > 1e-6 {
				t.Errorf("cosine() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRRFMerge(t *testing.T) {
	t.Run("deduplicates and ranks by combined score", func(t *testing.T) {
		semantic := []string{"a", "b", "c"}
		keyword := []string{"b", "c", "d"}
		result := rrfMerge(semantic, keyword)
		// "b" and "c" appear in both lists — should rank above "a" and "d"
		if len(result) != 4 {
			t.Fatalf("expected 4 unique IDs, got %d: %v", len(result), result)
		}
		if result[0] != "b" && result[0] != "c" {
			t.Errorf("expected top result to be 'b' or 'c', got %q", result[0])
		}
	})

	t.Run("empty inputs", func(t *testing.T) {
		result := rrfMerge(nil, nil)
		if len(result) != 0 {
			t.Errorf("expected empty result, got %v", result)
		}
	})

	t.Run("single list", func(t *testing.T) {
		result := rrfMerge([]string{"x", "y"}, nil)
		if len(result) != 2 || result[0] != "x" {
			t.Errorf("expected [x y], got %v", result)
		}
	})
}
