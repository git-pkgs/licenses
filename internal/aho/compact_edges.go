package aho

import "errors"

// CompactEdgeStarts stores edge offsets in 24 bits.
type CompactEdgeStarts struct {
	values packedUint24
}

// NewCompactEdgeStarts compacts dense edge offsets.
func NewCompactEdgeStarts(edgeStarts []uint32) CompactEdgeStarts {
	return CompactEdgeStarts{values: packUint24(edgeStarts)}
}

// NewCompactEdgeStartsFromCorrections builds compact offsets from exceptional nodes.
func NewCompactEdgeStartsFromCorrections(
	nodeCount int,
	nodes []uint32,
	corrections []int32,
) (CompactEdgeStarts, error) {
	if nodeCount <= 0 || len(nodes) != len(corrections) {
		return CompactEdgeStarts{}, errors.New("aho: invalid compact edge offsets")
	}
	values := make(packedUint24, (nodeCount+1)*packedUint24Width)
	var correction int32
	exception := 0
	for boundary := 0; boundary <= nodeCount; boundary++ {
		for exception < len(nodes) && nodes[exception] < uint32(boundary) {
			if exception > 0 && nodes[exception] <= nodes[exception-1] {
				return CompactEdgeStarts{}, errors.New("aho: invalid compact edge node")
			}
			if nodes[exception] >= uint32(nodeCount) {
				return CompactEdgeStarts{}, errors.New("aho: invalid compact edge node")
			}
			correction = corrections[exception]
			exception++
		}
		value := int64(boundary) + int64(correction)
		if value < 0 || value >= 1<<24 {
			return CompactEdgeStarts{}, errors.New("aho: compact edge offset exceeds 24 bits")
		}
		values.set(uint32(boundary), uint32(value))
	}
	return CompactEdgeStarts{values: values}, nil
}

// Get returns the edge offset for a node boundary.
func (s CompactEdgeStarts) Get(index uint32) uint32 {
	return s.values.get(index)
}

// Len returns the number of node boundaries.
func (s CompactEdgeStarts) Len() int {
	return s.values.len()
}

func (s CompactEdgeStarts) dense() []uint32 {
	return s.values.dense()
}
