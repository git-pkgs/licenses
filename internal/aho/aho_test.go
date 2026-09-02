package aho

import (
	"slices"
	"testing"
)

func TestAutomatonMatchesPatternsAndSuffixes(t *testing.T) {
	t.Parallel()

	automaton, err := Build([]Pattern{
		{Tokens: []uint32{1, 2}, Value: 0},
		{Tokens: []uint32{2}, Value: 1},
		{Tokens: []uint32{1, 2, 3}, Value: 2},
		{Tokens: []uint32{2, 3}, Value: 3},
	}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := automaton.Validate(4); err != nil {
		t.Fatal(err)
	}

	state := uint32(0)
	var got [][]uint32
	for _, token := range []uint32{1, 2, 3} {
		state = automaton.Next(state, token)
		outputs := automaton.AppendOutputs(nil, state)
		slices.Sort(outputs)
		for value := range uint32(4) {
			if automaton.HasOutput(state, value) != slices.Contains(outputs, value) {
				t.Fatalf("HasOutput(%d, %d) differs from outputs %v", state, value, outputs)
			}
		}
		got = append(got, outputs)
	}
	want := [][]uint32{nil, {0, 1}, {2, 3}}
	if !slices.EqualFunc(got, want, slices.Equal[[]uint32]) {
		t.Fatalf("outputs = %#v, want %#v", got, want)
	}
}

func TestBuildIsDeterministicAcrossPatternOrder(t *testing.T) {
	t.Parallel()

	first, err := Build([]Pattern{
		{Tokens: []uint32{3, 4}, Value: 0},
		{Tokens: []uint32{1, 2}, Value: 1},
	}, 2)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Build([]Pattern{
		{Tokens: []uint32{1, 2}, Value: 1},
		{Tokens: []uint32{3, 4}, Value: 0},
	}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !equalAutomata(first, second) {
		t.Fatal("automata differ")
	}
}

func TestBuildFailureLinksReconstructsAutomaton(t *testing.T) {
	t.Parallel()

	automaton, err := Build([]Pattern{
		{Tokens: []uint32{1, 2}, Value: 0},
		{Tokens: []uint32{2}, Value: 1},
		{Tokens: []uint32{1, 2, 3}, Value: 2},
		{Tokens: []uint32{2, 3}, Value: 3},
	}, 4)
	if err != nil {
		t.Fatal(err)
	}
	failures, outputLinks, err := BuildFailureLinks(
		automaton.EdgeStarts,
		automaton.EdgeTokens,
		automaton.TerminalHeads,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(failures, automaton.Failures) {
		t.Fatalf("failures differ:\n got: %#v\nwant: %#v", failures, automaton.Failures)
	}
	if !slices.Equal(outputLinks, automaton.OutputLinks) {
		t.Fatalf("output links differ:\n got: %#v\nwant: %#v", outputLinks, automaton.OutputLinks)
	}
}

func TestBuildRejectsZeroToken(t *testing.T) {
	t.Parallel()

	if _, err := Build([]Pattern{{Tokens: []uint32{0}, Value: 0}}, 1); err == nil {
		t.Fatal("Build accepted token zero")
	}
}

func TestValidateRejectsCyclicOutputChain(t *testing.T) {
	t.Parallel()

	automaton, err := Build([]Pattern{{Tokens: []uint32{1}, Value: 0}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	automaton.OutputNext[0] = 0
	if err := automaton.Validate(1); err == nil {
		t.Fatal("Validate accepted a cyclic output chain")
	}
}

func equalAutomata(first, second Automaton) bool {
	return slices.Equal(first.EdgeStarts, second.EdgeStarts) &&
		slices.Equal(first.EdgeTokens, second.EdgeTokens) &&
		slices.Equal(first.Failures, second.Failures) &&
		slices.Equal(first.OutputLinks, second.OutputLinks) &&
		slices.Equal(first.TerminalHeads, second.TerminalHeads) &&
		slices.Equal(first.OutputNext, second.OutputNext)
}
