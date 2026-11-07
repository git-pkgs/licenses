package aho

import (
	"slices"
	"testing"
)

func TestSparseOutputs(t *testing.T) {
	links := []uint32{None, 3, None, None, 7, None, None, None, None, 11}
	heads := []uint32{None, None, 5, None, 9, None, None, None, 13, None}
	table := NewSparseOutputs(links, heads)
	if table.Len() != len(links) {
		t.Fatalf("Len = %d, want %d", table.Len(), len(links))
	}
	for index := range links {
		gotLink, gotHead := table.Get(uint32(index))
		if gotLink != links[index] || gotHead != heads[index] {
			t.Fatalf("Get(%d) = (%d, %d), want (%d, %d)", index, gotLink, gotHead, links[index], heads[index])
		}
	}
	if got := denseOutputLinks(table); !slices.Equal(got, links) {
		t.Fatalf("DenseOutputLinks = %v, want %v", got, links)
	}
	if got := denseTerminalHeads(table); !slices.Equal(got, heads) {
		t.Fatalf("DenseTerminalHeads = %v, want %v", got, heads)
	}
}

func TestSparseOutputsCrossesWordBoundary(t *testing.T) {
	links := make([]uint32, 130)
	heads := make([]uint32, 130)
	for index := range links {
		links[index] = None
		heads[index] = None
	}
	links[0] = 1
	heads[63] = 2
	links[64] = 3
	heads[129] = 4
	table := NewSparseOutputs(links, heads)
	for index := range links {
		gotLink, gotHead := table.Get(uint32(index))
		if gotLink != links[index] || gotHead != heads[index] {
			t.Fatalf("Get(%d) = (%d, %d), want (%d, %d)", index, gotLink, gotHead, links[index], heads[index])
		}
	}
}

func TestSparseTerminalHeadsWithOutputLinks(t *testing.T) {
	nodes := []uint32{2, 4, 8}
	heads := []uint32{5, 9, 13}
	table := NewSparseTerminalHeads(10, nodes, heads)
	links := []uint32{None, 3, None, None, 7, None, None, None, None, 11}
	table = table.WithOutputLinks(links)
	wantHeads := []uint32{None, None, 5, None, 9, None, None, None, 13, None}
	if got := denseOutputLinks(table); !slices.Equal(got, links) {
		t.Fatalf("DenseOutputLinks = %v, want %v", got, links)
	}
	if got := denseTerminalHeads(table); !slices.Equal(got, wantHeads) {
		t.Fatalf("DenseTerminalHeads = %v, want %v", got, wantHeads)
	}
}

func denseOutputLinks(table SparseOutputs) []uint32 {
	links := make([]uint32, table.Len())
	for index := range links {
		links[index] = None
	}
	table.Range(func(index, link, _ uint32) {
		links[index] = link
	})
	return links
}

func denseTerminalHeads(table SparseOutputs) []uint32 {
	heads := make([]uint32, table.Len())
	for index := range heads {
		heads[index] = None
	}
	table.Range(func(index, _ uint32, head uint32) {
		heads[index] = head
	})
	return heads
}

func TestSparseOutputsRejectsMismatchedLengths(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewSparseOutputs accepted mismatched lengths")
		}
	}()
	_ = NewSparseOutputs([]uint32{None}, nil)
}

func TestSparseOutputsRejectsOutOfRangeIndex(t *testing.T) {
	table := NewSparseOutputs([]uint32{None}, []uint32{None})
	defer func() {
		if recover() == nil {
			t.Fatal("Get accepted an out-of-range index")
		}
	}()
	_, _ = table.Get(1)
}

func TestSparseTerminalHeadsRejectsUnorderedNodes(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewSparseTerminalHeads accepted unordered nodes")
		}
	}()
	_ = NewSparseTerminalHeads(3, []uint32{2, 1}, []uint32{0, 1})
}

func TestSparseOutputsRejectsMismatchedOutputLinkLength(t *testing.T) {
	table := NewSparseTerminalHeads(1, nil, nil)
	defer func() {
		if recover() == nil {
			t.Fatal("WithOutputLinks accepted a mismatched length")
		}
	}()
	_ = table.WithOutputLinks(nil)
}
