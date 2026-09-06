// Package aho implements a compact Aho-Corasick automaton over integer tokens.
package aho

import (
	"errors"
	"fmt"
	"runtime"
	"slices"
	"sync"
)

// None is the sentinel used for absent nodes and outputs.
const None = ^uint32(0)

// Pattern associates a token sequence with a caller-defined value.
type Pattern struct {
	Tokens []uint32
	Value  uint32
}

// Automaton stores a token trie, failure links, and pattern outputs.
type Automaton struct {
	EdgeStarts    []uint32
	EdgeTokens    []uint32
	Failures      []uint32
	OutputLinks   []uint32
	TerminalHeads []uint32
	OutputNext    []uint32

	// rootNext maps token to state 0's child for that token, or 0 for no
	// edge. It is derived from EdgeStarts and EdgeTokens by BuildRootTable
	// and covers tokens up to the highest root edge; tokens above that
	// index also have no root edge.
	rootNext []uint32
}

// Build constructs an automaton. Empty patterns are ignored.
func Build(patterns []Pattern, valueCount int) (Automaton, error) {
	if valueCount < 0 || uint64(valueCount) > uint64(None) {
		return Automaton{}, fmt.Errorf("aho: invalid value count %d", valueCount)
	}
	refs := make([]Pattern, 0, len(patterns))
	seen := make([]bool, valueCount)
	var tokenCount uint64
	for _, pattern := range patterns {
		if uint64(pattern.Value) >= uint64(valueCount) {
			return Automaton{}, fmt.Errorf("aho: pattern value %d exceeds count %d", pattern.Value, valueCount)
		}
		if seen[pattern.Value] {
			return Automaton{}, fmt.Errorf("aho: duplicate pattern value %d", pattern.Value)
		}
		seen[pattern.Value] = true
		if len(pattern.Tokens) == 0 {
			continue
		}
		for _, token := range pattern.Tokens {
			if token == 0 {
				return Automaton{}, fmt.Errorf("aho: pattern %d contains token zero", pattern.Value)
			}
		}
		tokenCount += uint64(len(pattern.Tokens))
		if tokenCount >= uint64(None) {
			return Automaton{}, errors.New("aho: patterns exceed node ID space")
		}
		refs = append(refs, pattern)
	}
	slices.SortFunc(refs, func(a, b Pattern) int {
		if compared := slices.Compare(a.Tokens, b.Tokens); compared != 0 {
			return compared
		}
		if a.Value < b.Value {
			return -1
		}
		if a.Value > b.Value {
			return 1
		}
		return 0
	})

	capacity := int(tokenCount) + 1
	parents := make([]uint32, 1, capacity)
	incoming := make([]uint32, 1, capacity)
	terminalHeads := make([]uint32, 1, capacity)
	terminalHeads[0] = None
	outputNext := make([]uint32, valueCount)
	for index := range outputNext {
		outputNext[index] = None
	}

	path := []uint32{0}
	var previous []uint32
	for _, pattern := range refs {
		common := commonPrefix(previous, pattern.Tokens)
		path = path[:common+1]
		for _, token := range pattern.Tokens[common:] {
			node := uint32(len(parents))
			parents = append(parents, path[len(path)-1])
			incoming = append(incoming, token)
			terminalHeads = append(terminalHeads, None)
			path = append(path, node)
		}
		terminal := path[len(path)-1]
		outputNext[pattern.Value] = terminalHeads[terminal]
		terminalHeads[terminal] = pattern.Value
		previous = pattern.Tokens
	}

	edgeStarts, edgeTokens, edgeTargets, err := compactEdges(parents, incoming)
	if err != nil {
		return Automaton{}, err
	}
	failures, outputLinks := buildFailures(edgeStarts, edgeTokens, edgeTargets, terminalHeads)
	automaton := reorderBreadthFirst(
		edgeStarts,
		edgeTokens,
		edgeTargets,
		failures,
		outputLinks,
		terminalHeads,
		outputNext,
	)
	automaton.BuildRootTable()
	return automaton, nil
}

func commonPrefix(first, second []uint32) int {
	limit := min(len(first), len(second))
	for index := 0; index < limit; index++ {
		if first[index] != second[index] {
			return index
		}
	}
	return limit
}

func compactEdges(parents, incoming []uint32) ([]uint32, []uint32, []uint32, error) {
	nodeCount := len(parents)
	edgeStarts := make([]uint32, nodeCount+1)
	for node := 1; node < nodeCount; node++ {
		edgeStarts[parents[node]+1]++
	}
	for node := 1; node < len(edgeStarts); node++ {
		edgeStarts[node] += edgeStarts[node-1]
	}

	edgeTokens := make([]uint32, nodeCount-1)
	edgeTargets := make([]uint32, nodeCount-1)
	positions := slices.Clone(edgeStarts[:nodeCount])
	for node := 1; node < nodeCount; node++ {
		parent := parents[node]
		position := positions[parent]
		edgeTokens[position] = incoming[node]
		edgeTargets[position] = uint32(node)
		positions[parent]++
	}
	for node := range nodeCount {
		start, end := edgeStarts[node], edgeStarts[node+1]
		for edge := start + 1; edge < end; edge++ {
			if edgeTokens[edge-1] >= edgeTokens[edge] {
				return nil, nil, nil, fmt.Errorf("aho: edges for node %d are not sorted", node)
			}
		}
	}
	return edgeStarts, edgeTokens, edgeTargets, nil
}

func buildFailures(
	edgeStarts []uint32,
	edgeTokens []uint32,
	edgeTargets []uint32,
	terminalHeads []uint32,
) ([]uint32, []uint32) {
	nodeCount := len(terminalHeads)
	failures := make([]uint32, nodeCount)
	outputLinks := make([]uint32, nodeCount)
	for node := range outputLinks {
		outputLinks[node] = None
	}

	queue := make([]uint32, 0, nodeCount-1)
	for edge := edgeStarts[0]; edge < edgeStarts[1]; edge++ {
		queue = append(queue, edgeTargets[edge])
	}
	for position := 0; position < len(queue); position++ {
		parent := queue[position]
		for edge := edgeStarts[parent]; edge < edgeStarts[parent+1]; edge++ {
			token := edgeTokens[edge]
			child := edgeTargets[edge]
			failure := failures[parent]
			target, found := direct(edgeStarts, edgeTokens, edgeTargets, failure, token)
			for !found && failure != 0 {
				failure = failures[failure]
				target, found = direct(edgeStarts, edgeTokens, edgeTargets, failure, token)
			}
			if found {
				failures[child] = target
			}
			failure = failures[child]
			if terminalHeads[failure] != None {
				outputLinks[child] = failure
			} else {
				outputLinks[child] = outputLinks[failure]
			}
			queue = append(queue, child)
		}
	}
	return failures, outputLinks
}

// BuildFailureLinks derives failure and output links from a breadth-first trie.
func BuildFailureLinks(
	edgeStarts []uint32,
	edgeTokens []uint32,
	terminalHeads []uint32,
) ([]uint32, []uint32, error) {
	const minimumParallelEdges = 256

	nodeCount := len(terminalHeads)
	if nodeCount == 0 || len(edgeStarts) != nodeCount+1 || len(edgeTokens) != nodeCount-1 {
		return nil, nil, errors.New("aho: inconsistent trie arrays")
	}
	failures := make([]uint32, nodeCount)
	outputLinks := make([]uint32, nodeCount)
	for node := range outputLinks {
		outputLinks[node] = None
	}
	rootNext := buildRootTable(edgeStarts, edgeTokens)
	workerCount := min(runtime.GOMAXPROCS(0), nodeCount)
	if workerCount == 1 {
		buildFailureLinkRange(
			edgeStarts,
			edgeTokens,
			rootNext,
			terminalHeads,
			failures,
			outputLinks,
			1,
			uint32(nodeCount),
		)
		return failures, outputLinks, nil
	}
	type linkRange struct {
		start uint32
		end   uint32
	}
	work := make(chan linkRange, workerCount)
	done := make(chan struct{}, workerCount)
	var workers sync.WaitGroup
	for range workerCount {
		workers.Go(func() {
			for nodes := range work {
				buildFailureLinkRange(
					edgeStarts,
					edgeTokens,
					rootNext,
					terminalHeads,
					failures,
					outputLinks,
					nodes.start,
					nodes.end,
				)
				done <- struct{}{}
			}
		})
	}

	levelStart := uint32(1)
	levelEnd := edgeStarts[1] + 1
	for levelStart < levelEnd {
		// Failure targets are complete because nodes are numbered breadth-first.
		parentCount := int(levelEnd - levelStart)
		levelWorkers := min(workerCount, parentCount)
		edgeCount := edgeStarts[levelEnd] - edgeStarts[levelStart]
		if levelWorkers == 1 || edgeCount < minimumParallelEdges {
			buildFailureLinkRange(
				edgeStarts,
				edgeTokens,
				rootNext,
				terminalHeads,
				failures,
				outputLinks,
				levelStart,
				levelEnd,
			)
		} else {
			chunkSize := (parentCount + levelWorkers - 1) / levelWorkers
			jobs := 0
			for start := levelStart; start < levelEnd; start += uint32(chunkSize) {
				end := min(start+uint32(chunkSize), levelEnd)
				work <- linkRange{start: start, end: end}
				jobs++
			}
			for range jobs {
				<-done
			}
		}
		levelStart, levelEnd = edgeStarts[levelStart]+1, edgeStarts[levelEnd]+1
	}
	close(work)
	workers.Wait()
	return failures, outputLinks, nil
}

func buildFailureLinkRange(
	edgeStarts []uint32,
	edgeTokens []uint32,
	rootNext []uint32,
	terminalHeads []uint32,
	failures []uint32,
	outputLinks []uint32,
	start uint32,
	end uint32,
) {
	for parent := start; parent < end; parent++ {
		first, last := edgeStarts[parent], edgeStarts[parent+1]
		for edge := first; edge < last; edge++ {
			child := edge + 1
			token := edgeTokens[edge]
			failure := failures[parent]
			target, found := directFailure(edgeStarts, edgeTokens, rootNext, failure, token)
			for !found && failure != 0 {
				failure = failures[failure]
				target, found = directFailure(edgeStarts, edgeTokens, rootNext, failure, token)
			}
			if found {
				failures[child] = target
			}
			failure = failures[child]
			if terminalHeads[failure] != None {
				outputLinks[child] = failure
			} else {
				outputLinks[child] = outputLinks[failure]
			}
		}
	}
}

func directFailure(
	edgeStarts []uint32,
	edgeTokens []uint32,
	rootNext []uint32,
	state uint32,
	token uint32,
) (uint32, bool) {
	if state != 0 {
		return directImplicit(edgeStarts, edgeTokens, state, token)
	}
	if uint64(token) >= uint64(len(rootNext)) {
		return 0, false
	}
	target := rootNext[token]
	return target, target != 0
}

func reorderBreadthFirst(
	edgeStarts []uint32,
	edgeTokens []uint32,
	edgeTargets []uint32,
	failures []uint32,
	outputLinks []uint32,
	terminalHeads []uint32,
	outputNext []uint32,
) Automaton {
	nodeCount := len(failures)
	order := make([]uint32, 1, nodeCount)
	remapped := make([]uint32, nodeCount)
	for position := 0; position < len(order); position++ {
		oldNode := order[position]
		for edge := edgeStarts[oldNode]; edge < edgeStarts[oldNode+1]; edge++ {
			child := edgeTargets[edge]
			remapped[child] = uint32(len(order))
			order = append(order, child)
		}
	}

	newEdgeStarts := make([]uint32, nodeCount+1)
	newEdgeTokens := make([]uint32, 0, len(edgeTokens))
	newFailures := make([]uint32, nodeCount)
	newOutputLinks := make([]uint32, nodeCount)
	newTerminalHeads := make([]uint32, nodeCount)
	for newNode, oldNode := range order {
		newEdgeStarts[newNode] = uint32(len(newEdgeTokens))
		for edge := edgeStarts[oldNode]; edge < edgeStarts[oldNode+1]; edge++ {
			newEdgeTokens = append(newEdgeTokens, edgeTokens[edge])
		}
		newFailures[newNode] = remapped[failures[oldNode]]
		if outputLinks[oldNode] == None {
			newOutputLinks[newNode] = None
		} else {
			newOutputLinks[newNode] = remapped[outputLinks[oldNode]]
		}
		newTerminalHeads[newNode] = terminalHeads[oldNode]
	}
	newEdgeStarts[nodeCount] = uint32(len(newEdgeTokens))
	return Automaton{
		EdgeStarts:    newEdgeStarts,
		EdgeTokens:    newEdgeTokens,
		Failures:      newFailures,
		OutputLinks:   newOutputLinks,
		TerminalHeads: newTerminalHeads,
		OutputNext:    outputNext,
	}
}

// BuildRootTable derives the root transition table from EdgeStarts and
// EdgeTokens. Root receives the majority of transition probes and has the
// widest fanout, so a direct token-indexed lookup replaces its binary
// search. Call after EdgeStarts and EdgeTokens are populated.
func (a *Automaton) BuildRootTable() {
	a.rootNext = buildRootTable(a.EdgeStarts, a.EdgeTokens)
}

func buildRootTable(edgeStarts []uint32, edgeTokens []uint32) []uint32 {
	if len(edgeStarts) < 2 || edgeStarts[1] == 0 {
		return nil
	}
	end := edgeStarts[1]
	table := make([]uint32, edgeTokens[end-1]+1)
	for edge := edgeStarts[0]; edge < end; edge++ {
		table[edgeTokens[edge]] = edge + 1
	}
	return table
}

// Next advances state with token, following failure links as needed.
func (a *Automaton) Next(state, token uint32) uint32 {
	for state != 0 {
		if target, found := directImplicit(a.EdgeStarts, a.EdgeTokens, state, token); found {
			return target
		}
		state = a.Failures[state]
	}
	if a.rootNext != nil {
		if uint64(token) < uint64(len(a.rootNext)) {
			return a.rootNext[token]
		}
		return 0
	}
	target, _ := directImplicit(a.EdgeStarts, a.EdgeTokens, 0, token)
	return target
}

// AppendOutputs appends all pattern values ending at state to values.
func (a *Automaton) AppendOutputs(values []uint32, state uint32) []uint32 {
	for node := state; node != None; node = a.OutputLinks[node] {
		for value := a.TerminalHeads[node]; value != None; value = a.OutputNext[value] {
			values = append(values, value)
		}
	}
	return values
}

// HasOutput reports whether value is among the patterns ending at state.
func (a *Automaton) HasOutput(state, value uint32) bool {
	for node := state; node != None; node = a.OutputLinks[node] {
		for output := a.TerminalHeads[node]; output != None; output = a.OutputNext[output] {
			if output == value {
				return true
			}
		}
	}
	return false
}

func direct(
	edgeStarts []uint32,
	edgeTokens []uint32,
	edgeTargets []uint32,
	state uint32,
	token uint32,
) (uint32, bool) {
	start, end := edgeStarts[state], edgeStarts[state+1]
	position, found := slices.BinarySearch(edgeTokens[start:end], token)
	if !found {
		return 0, false
	}
	return edgeTargets[start+uint32(position)], true
}

func directImplicit(
	edgeStarts []uint32,
	edgeTokens []uint32,
	state uint32,
	token uint32,
) (uint32, bool) {
	start, end := edgeStarts[state], edgeStarts[state+1]
	if start == end {
		return 0, false
	}
	if end-start == 1 {
		if edgeTokens[start] == token {
			return start + 1, true
		}
		return 0, false
	}
	position, found := slices.BinarySearch(edgeTokens[start:end], token)
	if !found {
		return 0, false
	}
	return start + uint32(position) + 1, true
}

// Validate checks array lengths, references, and edge ordering.
func (a *Automaton) Validate(valueCount int) error {
	nodeCount := len(a.Failures)
	if nodeCount == 0 {
		return errors.New("aho: automaton has no root")
	}
	if len(a.EdgeStarts) != nodeCount+1 ||
		len(a.OutputLinks) != nodeCount ||
		len(a.TerminalHeads) != nodeCount {
		return errors.New("aho: inconsistent node array lengths")
	}
	if len(a.EdgeTokens) != nodeCount-1 ||
		a.EdgeStarts[nodeCount] != uint32(len(a.EdgeTokens)) {
		return errors.New("aho: inconsistent edge array lengths")
	}
	if len(a.OutputNext) != valueCount {
		return fmt.Errorf("aho: %d output links for %d values", len(a.OutputNext), valueCount)
	}
	for node := range nodeCount {
		if a.Failures[node] >= uint32(nodeCount) {
			return fmt.Errorf("aho: invalid failure from node %d", node)
		}
		link := a.OutputLinks[node]
		if link != None && link >= uint32(nodeCount) {
			return fmt.Errorf("aho: invalid output link from node %d", node)
		}
		head := a.TerminalHeads[node]
		if head != None && head >= uint32(valueCount) {
			return fmt.Errorf("aho: invalid terminal output at node %d", node)
		}
		start, end := a.EdgeStarts[node], a.EdgeStarts[node+1]
		if start > end || end > uint32(len(a.EdgeTokens)) {
			return fmt.Errorf("aho: invalid edge range for node %d", node)
		}
		for edge := start; edge < end; edge++ {
			if edge > start && a.EdgeTokens[edge-1] >= a.EdgeTokens[edge] {
				return fmt.Errorf("aho: unsorted edges for node %d", node)
			}
		}
	}
	for value, next := range a.OutputNext {
		if next != None && next >= uint32(valueCount) {
			return fmt.Errorf("aho: invalid output chain at value %d", value)
		}
	}
	if err := validateOutputChains(a.OutputNext); err != nil {
		return err
	}
	return nil
}

func validateOutputChains(nextValues []uint32) error {
	const (
		visiting = 1
		visited  = 2
	)
	states := make([]uint8, len(nextValues))
	for start := range nextValues {
		node := uint32(start)
		for node != None && states[node] == 0 {
			states[node] = visiting
			node = nextValues[node]
		}
		if node != None && states[node] == visiting {
			return fmt.Errorf("aho: cyclic output chain at value %d", node)
		}
		node = uint32(start)
		for node != None && states[node] == visiting {
			states[node] = visited
			node = nextValues[node]
		}
	}
	return nil
}

// NodeCount returns the number of trie nodes, including the root.
func (a *Automaton) NodeCount() int {
	return len(a.Failures)
}

// EdgeCount returns the number of trie edges.
func (a *Automaton) EdgeCount() int {
	return len(a.EdgeTokens)
}
