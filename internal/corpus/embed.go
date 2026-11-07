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

// LoadRuntime reads the embedded corpus without retaining stored rule tokens.
func LoadRuntime() (Index, error) {
	return ReadRuntime(bytes.NewReader(embedded))
}

// EmbeddedSize returns the compressed corpus size in bytes.
func EmbeddedSize() int {
	return len(embedded)
}
