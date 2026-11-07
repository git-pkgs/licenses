package aho

import (
	"slices"
	"strings"
	"testing"
)

func TestSparseEdgeStartsRoundTrip(t *testing.T) {
	t.Parallel()

	for _, want := range [][]uint32{
		{0, 2, 3, 3, 4},
		{0, 1},
		{0, 0},
	} {
		table := NewCompactEdgeStarts(want)
		if got := table.dense(); !slices.Equal(got, want) {
			t.Fatalf("dense = %v, want %v", got, want)
		}
	}
}

func TestSparseEdgeStartsCrossesWordBoundary(t *testing.T) {
	t.Parallel()

	want := make([]uint32, 131)
	for node := 0; node < 130; node++ {
		degree := uint32(1)
		if node == 0 {
			degree = 2
		}
		if node == 63 || node == 129 {
			degree = 0
		}
		want[node+1] = want[node] + degree
	}
	table := NewCompactEdgeStarts(want)
	if got := table.dense(); !slices.Equal(got, want) {
		t.Fatalf("dense = %v, want %v", got, want)
	}
}

func TestCompactEdgeStartsRejectsInvalidCorrections(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		nodeCount   int
		nodes       []uint32
		corrections []int32
		want        string
	}{
		{name: "empty", want: "invalid compact edge offsets"},
		{
			name:        "mismatched corrections",
			nodeCount:   2,
			nodes:       []uint32{0},
			corrections: nil,
			want:        "invalid compact edge offsets",
		},
		{
			name:        "duplicate node",
			nodeCount:   2,
			nodes:       []uint32{0, 0},
			corrections: []int32{0, 0},
			want:        "invalid compact edge node",
		},
		{
			name:        "node outside table",
			nodeCount:   2,
			nodes:       []uint32{2},
			corrections: []int32{0},
			want:        "invalid compact edge node",
		},
		{
			name:        "unconsumed trailing node",
			nodeCount:   2,
			nodes:       []uint32{0, 5},
			corrections: []int32{0, 0},
			want:        "invalid compact edge node",
		},
		{
			name:        "negative offset",
			nodeCount:   2,
			nodes:       []uint32{0},
			corrections: []int32{-2},
			want:        "compact edge offset exceeds 24 bits",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := NewCompactEdgeStartsFromCorrections(
				test.nodeCount,
				test.nodes,
				test.corrections,
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}
