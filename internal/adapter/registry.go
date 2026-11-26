// Package adapter registers versioned container parsers (ParserV1) and
// adapts their output to the kernel's shared types and error model.
package adapter

import (
	"fmt"
	"io"
	"sort"

	"continuity/internal/core"
)

// ParserV1 is the versioned container-parser contract.
type ParserV1 interface {
	Version() int
	ParseSegment(r io.ReadSeeker, source string) (*core.SegmentInfo, error)
}

var parsersV1 = map[string]ParserV1{}

// RegisterV1 registers a ParserV1 under a name. Only Version()==1
// implementations are accepted.
func RegisterV1(name string, p ParserV1) error {
	if p == nil || p.Version() != 1 {
		return fmt.Errorf("adapter %q: unsupported parser version", name)
	}
	if _, dup := parsersV1[name]; dup {
		return fmt.Errorf("adapter %q: already registered", name)
	}
	parsersV1[name] = p
	return nil
}

// Get returns the named parser.
func Get(name string) (ParserV1, bool) {
	p, ok := parsersV1[name]
	return p, ok
}

// Names returns registered parser names in sorted (deterministic) order.
func Names() []string {
	n := make([]string, 0, len(parsersV1))
	for k := range parsersV1 {
		n = append(n, k)
	}
	sort.Strings(n)
	return n
}
