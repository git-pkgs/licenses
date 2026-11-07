package aho

import (
	"slices"
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
