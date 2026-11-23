// Package adapter contains versioned parser adapters that turn container and
// packet inputs into timeline.Track values on the unified nanosecond base.
package adapter

import (
	"fmt"
	"io"

	"mediad/timeline"
)

// Version is a semantic major.minor parser interface version.
type Version struct {
	Major int
	Minor int
}

func (v Version) String() string { return fmt.Sprintf("%d.%d", v.Major, v.Minor) }

// CompatibleWith reports whether a client requesting want may use an adapter
// at version got: major versions must match exactly.
func CompatibleWith(got, want Version) bool { return got.Major == want.Major }

// ParseResult is the normalized output of any adapter.
type ParseResult struct {
	Tracks  []timeline.Track
	Unknown []timeline.UnknownMeta // raw bytes preserved, never silently dropped
}

// Adapter is the versioned parser contract.
type Adapter interface {
	Name() string
	Version() Version
	Parse(r io.Reader, opts Options) (*ParseResult, error)
}

// Options carries optional parse parameters shared by adapters.
type Options struct {
	TrackID     uint32
	Timescale   int64
	OriginNS    int64
	WrapModulus int64
	MaxDriftNS  int64
}

// Registry indexes adapters by name with their interface versions.
type Registry struct {
	entries map[string]Adapter
}

// NewRegistry returns an empty registry; the default adapters register there.
func NewRegistry() *Registry { return &Registry{entries: map[string]Adapter{}} }

// Register adds an adapter, rejecting duplicate names.
func (r *Registry) Register(a Adapter) error {
	if _, ok := r.entries[a.Name()]; ok {
		return fmt.Errorf("adapter %q already registered", a.Name())
	}
	r.entries[a.Name()] = a
	return nil
}

// MustRegister panics on duplicate registration (package init time use).
func (r *Registry) MustRegister(a Adapter) {
	if err := r.Register(a); err != nil {
		panic(err)
	}
}

// Get returns the named adapter only when the requested major version matches.
func (r *Registry) Get(name string, want Version) (Adapter, error) {
	a, ok := r.entries[name]
	if !ok {
		return nil, fmt.Errorf("adapter %q not registered", name)
	}
	if !CompatibleWith(a.Version(), want) {
		return nil, fmt.Errorf("adapter %q version %s incompatible with requested %s: major mismatch",
			name, a.Version(), want)
	}
	return a, nil
}

// Names lists registered adapter names (test/diagnostics).
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.entries))
	for n := range r.entries {
		names = append(names, n)
	}
	return names
}

