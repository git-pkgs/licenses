package aho

import "math/bits"

const sparseWordBits = 64

type sparseOutput struct {
	link uint32
	head uint32
}

// SparseOutputs stores node-indexed output links and terminal heads.
type SparseOutputs struct {
	length uint32
	bits   []uint64
	ranks  []uint32
	values []sparseOutput
}

// NewSparseOutputs packs output links and terminal heads into one node table.
func NewSparseOutputs(outputLinks, terminalHeads []uint32) SparseOutputs {
	if len(outputLinks) != len(terminalHeads) {
		panic("aho: inconsistent sparse output lengths")
	}
	table := newSparseOutputs(len(outputLinks), 0)
	for index, link := range outputLinks {
		head := terminalHeads[index]
		if link == None && head == None {
			continue
		}
		table.bits[index/sparseWordBits] |= uint64(1) << (index % sparseWordBits)
		table.values = append(table.values, sparseOutput{link: link, head: head})
	}
	table.populateRanks()
	return table
}

// NewSparseTerminalHeads packs terminal node and value pairs into a node table.
func NewSparseTerminalHeads(length int, nodes, heads []uint32) SparseOutputs {
	if len(nodes) != len(heads) {
		panic("aho: inconsistent sparse terminal lengths")
	}
	table := newSparseOutputs(length, len(nodes))
	var previous uint32
	for index, node := range nodes {
		if node >= uint32(length) || (index > 0 && node <= previous) {
			panic("aho: invalid sparse terminal node")
		}
		previous = node
		if heads[index] == None {
			continue
		}
		table.bits[node/sparseWordBits] |= uint64(1) << (node % sparseWordBits)
		table.values = append(table.values, sparseOutput{link: None, head: heads[index]})
	}
	table.populateRanks()
	return table
}

func newSparseOutputs(length, capacity int) SparseOutputs {
	wordCount := (length + sparseWordBits - 1) / sparseWordBits
	return SparseOutputs{
		length: uint32(length),
		bits:   make([]uint64, wordCount),
		ranks:  make([]uint32, wordCount),
		values: make([]sparseOutput, 0, capacity),
	}
}

func (s *SparseOutputs) populateRanks() {
	var rank uint32
	for index, word := range s.bits {
		s.ranks[index] = rank
		rank += uint32(bits.OnesCount64(word))
	}
}

// Len returns the number of addressable node entries.
func (s SparseOutputs) Len() int {
	return int(s.length)
}

// TerminalCount returns the number of nodes with a terminal head.
func (s SparseOutputs) TerminalCount() int {
	count := 0
	for _, value := range s.values {
		if value.head != None {
			count++
		}
	}
	return count
}

// Get returns the output link and terminal head for a node.
func (s SparseOutputs) Get(index uint32) (uint32, uint32) {
	if index >= s.length {
		panic("aho: sparse output index out of range")
	}
	wordIndex := index / sparseWordBits
	bit := uint64(1) << (index % sparseWordBits)
	word := s.bits[wordIndex]
	if word&bit == 0 {
		return None, None
	}
	rank := s.ranks[wordIndex] + uint32(bits.OnesCount64(word&(bit-1)))
	value := s.values[rank]
	return value.link, value.head
}

// TerminalHead returns the terminal head for a node.
func (s SparseOutputs) TerminalHead(index uint32) uint32 {
	_, head := s.Get(index)
	return head
}

// WithOutputLinks returns a table containing the supplied links and existing heads.
func (s SparseOutputs) WithOutputLinks(outputLinks []uint32) SparseOutputs {
	if len(outputLinks) != s.Len() {
		panic("aho: inconsistent output link length")
	}
	capacity := len(s.values)
	for _, link := range outputLinks {
		if link != None {
			capacity++
		}
	}
	table := newSparseOutputs(s.Len(), capacity)
	valueIndex := 0
	for index, link := range outputLinks {
		head := uint32(None)
		word := s.bits[index/sparseWordBits]
		if word&(uint64(1)<<(index%sparseWordBits)) != 0 {
			head = s.values[valueIndex].head
			valueIndex++
		}
		if link == None && head == None {
			continue
		}
		table.bits[index/sparseWordBits] |= uint64(1) << (index % sparseWordBits)
		table.values = append(table.values, sparseOutput{link: link, head: head})
	}
	table.populateRanks()
	return table
}

// Range calls yield for each node that has an output link or terminal head.
func (s SparseOutputs) Range(yield func(index, link, head uint32)) {
	valueIndex := 0
	for wordIndex, word := range s.bits {
		for word != 0 {
			bit := bits.TrailingZeros64(word)
			index := uint32(wordIndex*sparseWordBits + bit)
			if index >= s.length {
				return
			}
			value := s.values[valueIndex]
			yield(index, value.link, value.head)
			valueIndex++
			word &= word - 1
		}
	}
}

// RangeTerminalHeads calls yield for each terminal head in node order.
func (s SparseOutputs) RangeTerminalHeads(yield func(index, head uint32)) {
	s.Range(func(index, _ uint32, head uint32) {
		if head != None {
			yield(index, head)
		}
	})
}
