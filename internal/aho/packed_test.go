package aho

import (
	"slices"
	"testing"
)

func TestPackedUint24RoundTrip(t *testing.T) {
	t.Parallel()

	want := []uint32{0, 1, 255, 256, 65_535, 65_536, 0xff_ffff}
	packed := packUint24(want)
	if packed.len() != len(want) {
		t.Fatalf("len = %d, want %d", packed.len(), len(want))
	}
	if got := packed.dense(); !slices.Equal(got, want) {
		t.Fatalf("dense = %v, want %v", got, want)
	}
}
