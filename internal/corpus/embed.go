package corpus

import (
	"bytes"
	_ "embed"
)

//go:embed corpus.bin.gz
var embedded []byte

// Load reads the embedded corpus.
func Load() (Index, error) {
	return Read(bytes.NewReader(embedded))
}

// EmbeddedSize returns the compressed corpus size in bytes.
func EmbeddedSize() int {
	return len(embedded)
}
