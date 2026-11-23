// Package adapter hosts the protocol adapters and the registry that performs
// parser version registration and capability negotiation.
package adapter

import (
	"errors"
	"fmt"
	"sort"
	"sync"
)

// Kind identifies an adapter protocol family.
type Kind string

const (
	KindHLS  Kind = "hls"
	KindDASH Kind = "dash"
	KindMP4  Kind = "mp4"
	KindRTP  Kind = "rtp"
)

// Limits are hard, defensive caps applied by every parser so a hostile or
// corrupted manifest cannot drive unbounded memory or recursion.
type Limits struct {
	MaxSegments   int
	MaxBytes      int64
	MaxDepth      int
	MaxTracks     int
	MaxWindowSize int
}

// DefaultLimits are conservative, documented caps for local QC processing.
func DefaultLimits() Limits {
	return Limits{
		MaxSegments:   4096,
		MaxBytes:      64 << 20,
		MaxDepth:      32,
		MaxTracks:     32,
		MaxWindowSize: 1024,
	}
}

// Capability describes what one parser version can handle. Feature strings
// are protocol tokens such as "discontinuity", "fec-xor", "frag-mp4".
type Capability struct {
	Kind     Kind
	Name     string
	Version  string
	Features []string
	Limit    Limits
}

// Supports reports whether the capability includes a feature.
func (c Capability) Supports(feature string) bool {
	for _, f := range c.Features {
		if f == feature {
			return true
		}
	}
	return false
}

// Parser is a registered adapter constructor-level descriptor. Actual parsing
// functions live in the protocol sub-packages; the registry negotiates which
// implementation version is allowed to handle a given manifest.
type Parser interface {
	Descriptor() Capability
	Supports(feature string) bool
}

// Registry maps "kind/name@version" keys to parser descriptors.
type Registry struct {
	mu       sync.RWMutex
	parsers  map[string]Parser
	features map[string][]string // kind -> union of features
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		parsers:  make(map[string]Parser),
		features: make(map[string][]string),
	}
}

func key(k Kind, name, version string) string {
	return fmt.Sprintf("%s/%s@%s", k, name, version)
}

// Register adds a parser. Re-registering the exact same key is rejected so a
// version collision cannot silently replace an implementation.
func (r *Registry) Register(p Parser) error {
	c := p.Descriptor()
	if c.Name == "" || c.Version == "" {
		return errors.New("adapter: parser descriptor requires name and version")
	}
	k := key(c.Kind, c.Name, c.Version)
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.parsers[k]; exists {
		return fmt.Errorf("adapter: %s already registered", k)
	}
	r.parsers[k] = p
	for _, f := range c.Features {
		if !contains(r.features[string(c.Kind)], f) {
			r.features[string(c.Kind)] = append(r.features[string(c.Kind)], f)
		}
	}
	sort.Strings(r.features[string(c.Kind)])
	return nil
}

// Lookup returns the parser for an exact key.
func (r *Registry) Lookup(k Kind, name, version string) (Parser, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.parsers[key(k, name, version)]
	if !ok {
		return nil, fmt.Errorf("adapter: no parser registered for %s", key(k, name, version))
	}
	return p, nil
}

// Negotiate selects a parser of the given kind that supports every required
// feature. Among candidates it picks the highest registered version, making
// selection deterministic for a fixed registry state.
func (r *Registry) Negotiate(k Kind, required []string) (Parser, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var best Parser
	bestVer := ""
	for _, p := range r.parsers {
		c := p.Descriptor()
		if c.Kind != k {
			continue
		}
		ok := true
		for _, req := range required {
			if !p.Supports(req) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		if best == nil || compareVersions(c.Version, bestVer) > 0 {
			best, bestVer = p, c.Version
		}
	}
	if best == nil {
		return nil, fmt.Errorf("adapter: no %s parser supports features %v", k, required)
	}
	return best, nil
}

// Features returns the sorted union of features registered for a kind.
func (r *Registry) Features(k Kind) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := append([]string(nil), r.features[string(k)]...)
	return out
}

// Default is the process-wide registry populated by sub-package init funcs.
var Default = NewRegistry()

// MustRegister is used by package init functions.
func MustRegister(p Parser) {
	if err := Default.Register(p); err != nil {
		panic(err)
	}
}

func contains(set []string, v string) bool {
	i := sort.SearchStrings(set, v)
	return i < len(set) && set[i] == v
}

// compareVersions compares dotted numeric versions (e.g. 1.4.1).
func compareVersions(a, b string) int {
	na, nb := segments(a), segments(b)
	for i := 0; i < len(na) || i < len(nb); i++ {
		x, y := 0, 0
		if i < len(na) {
			x = na[i]
		}
		if i < len(nb) {
			y = nb[i]
		}
		if x != y {
			if x > y {
				return 1
			}
			return -1
		}
	}
	return 0
}

func segments(v string) []int {
	var out []int
	cur := 0
	hasDigit := false
	for _, ch := range v {
		if ch >= '0' && ch <= '9' {
			cur = cur*10 + int(ch-'0')
			hasDigit = true
		} else {
			if hasDigit {
				out = append(out, cur)
			}
			cur, hasDigit = 0, false
		}
	}
	if hasDigit {
		out = append(out, cur)
	}
	return out
}
