package licenses

import (
	"errors"
	"io"

	"github.com/git-pkgs/licenses/internal/corpus"
)

// NewFromReader loads a corpus produced by cmd/corpusgen from r. Each call
// builds an independent engine; reuse the returned Matcher across calls to
// Match. It does not close r or initialize the shared embedded corpus.
// The corpus must use the format supported by this version of the package.
// Programs using only NewFromReader can omit the full embedded corpus from
// their linked binary.
func NewFromReader(r io.Reader, options ...Option) (*Matcher, error) {
	config, err := configureMatcher(options)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, errors.New("licenses: nil corpus reader")
	}
	index, err := corpus.Read(r)
	if err != nil {
		return nil, err
	}
	engine, err := newMatchEngine(index)
	if err != nil {
		return nil, err
	}
	return &Matcher{engine: engine, matchedText: config.matchedText}, nil
}

func configureMatcher(options []Option) (matcherOptions, error) {
	var config matcherOptions
	for _, option := range options {
		if option == nil {
			return matcherOptions{}, errors.New("licenses: nil option")
		}
		option(&config)
	}
	return config, nil
}
