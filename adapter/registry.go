// Package adapter contains versioned parsers that turn container (MP4) and
// transport (RTP) input into kernel media manifests, translating every
// parse-level failure into a media.Error with a locatable position.
package adapter

import (
	"fmt"
	"io"
	"sort"
	"sync"

	"mediacore/media"
)

// Capability declares one feature a parser version supports.
type Capability string

const (
	CapEditList    Capability = "edit_list"
	CapSparseKey   Capability = "sparse_keyframes"
	CapNonZeroPTS  Capability = "nonzero_start_pts"
	CapRTPWrap     Capability = "rtp_32bit_wrap"
	CapReorder     Capability = "packet_reorder"
	CapVariableFPS Capability = "variable_fps"
)

// Adapter is the common surface of every versioned parser.
type Adapter interface {
	Name() string
	Version() uint32
	Capabilities() []Capability
	// Accepts reports whether the adapter can honour every required
	// capability; missing ones are returned so callers can surface them.
	Accepts(required []Capability) (missing []Capability, ok bool)
}

type baseAdapter struct {
	name string
	ver  uint32
	caps map[Capability]struct{}
}

func (b baseAdapter) Name() string    { return b.name }
func (b baseAdapter) Version() uint32 { return b.ver }
func (b baseAdapter) Capabilities() []Capability {
	out := make([]Capability, 0, len(b.caps))
	for c := range b.caps {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (b baseAdapter) Accepts(required []Capability) ([]Capability, bool) {
	var missing []Capability
	for _, c := range required {
		if _, have := b.caps[c]; !have {
			missing = append(missing, c)
		}
	}
	return missing, len(missing) == 0
}

// Registry holds the set of compiled-in adapters and performs versioned
// lookup and capability negotiation.
type Registry struct {
	mu       sync.RWMutex
	adapters map[string]map[uint32]Adapter
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{adapters: make(map[string]map[uint32]Adapter)}
}

// DefaultRegistry registers every kernel adapter at its current version.
func DefaultRegistry() *Registry {
	r := NewRegistry()
	r.MustRegister(NewMP4AdapterV1())
	r.MustRegister(NewRTPAdapterV1())
	return r
}

// Register adds an adapter, rejecting duplicate name/version pairs.
func (r *Registry) Register(a Adapter) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	versions, exists := r.adapters[a.Name()]
	if !exists {
		versions = make(map[uint32]Adapter)
		r.adapters[a.Name()] = versions
	}
	if _, dup := versions[a.Version()]; dup {
		return media.Errorf(media.CodeIncompatible, 0, nil, -1,
			"adapter %s v%d registered twice", a.Name(), a.Version())
	}
	versions[a.Version()] = a
	return nil
}

// MustRegister panics on duplicate registration; intended for static setup.
func (r *Registry) MustRegister(a Adapter) {
	if err := r.Register(a); err != nil {
		panic(err)
	}
}

// Lookup returns the exact requested adapter version.
func (r *Registry) Lookup(name string, version uint32) (Adapter, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	versions, ok := r.adapters[name]
	if !ok {
		return nil, media.Errorf(media.CodeIncompatible, 0, nil, -1,
			"unknown adapter %q", name)
	}
	a, ok := versions[version]
	if !ok {
		return nil, media.Errorf(media.CodeUnsupportedVersion, 0, nil, -1,
			"adapter %q has no version %d", name, version)
	}
	return a, nil
}

// Negotiate selects the highest registered version of name that satisfies
// every required capability.
func (r *Registry) Negotiate(name string, required []Capability) (Adapter, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	versions, ok := r.adapters[name]
	if !ok {
		return nil, media.Errorf(media.CodeIncompatible, 0, nil, -1,
			"unknown adapter %q", name)
	}
	vers := make([]uint32, 0, len(versions))
	for v := range versions {
		vers = append(vers, v)
	}
	sort.Slice(vers, func(i, j int) bool { return vers[i] > vers[j] })
	for _, v := range vers {
		a := versions[v]
		if missing, ok := a.Accepts(required); ok {
			return a, nil
		} else {
			_ = missing
		}
	}
	return nil, media.Errorf(media.CodeIncompatible, 0, nil, -1,
		"no version of %q satisfies capabilities %v", name, required)
}

// EnforceLimits validates the global resource declarations of an input.
func EnforceLimits(tracks int, durationNanos int64, parseBytes int) error {
	if tracks > media.MaxTracks {
		return media.Errorf(media.CodeLimitExceeded, 0, nil, -1,
			"track count %d exceeds limit %d", tracks, media.MaxTracks)
	}
	if durationNanos > media.MaxInputDuration {
		return media.Errorf(media.CodeLimitExceeded, 0, nil, -1,
			"duration %d ns exceeds 24h limit", durationNanos)
	}
	if parseBytes > media.MaxParseBytes {
		return media.Errorf(media.CodeLimitExceeded, 0, nil, -1,
			"parse allocation %d exceeds %d bytes", parseBytes, media.MaxParseBytes)
	}
	return nil
}

// boundedReader counts bytes read and fails once the parse budget is spent,
// turning oversized declarations into hard errors instead of OOM.
type boundedReader struct {
	r      io.Reader
	read   int
	budget int
}

// NewBoundedReader wraps r with a cumulative byte budget.
func NewBoundedReader(r io.Reader, budget int) io.Reader {
	return &boundedReader{r: r, budget: budget}
}

func (b *boundedReader) Read(p []byte) (int, error) {
	if b.read >= b.budget {
		return 0, media.Errorf(media.CodeLimitExceeded, 0, nil, int64(b.read),
			"parse budget of %d bytes exhausted", b.budget)
	}
	limit := b.budget - b.read
	if len(p) > limit {
		p = p[:limit]
	}
	n, err := b.r.Read(p)
	b.read += n
	return n, err
}

// ReadCount reports cumulative bytes consumed by a bounded reader.
func ReadCount(r io.Reader) (int, error) {
	if br, ok := r.(*boundedReader); ok {
		return br.read, nil
	}
	return 0, fmt.Errorf("reader is not bounded")
}
