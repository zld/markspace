// Package adapter contains versioned bitstream parsers. Each adapter turns a
// raw codec byte stream into normalized media.Unit events without consulting
// any container timestamps.
package adapter

import (
	"sort"
	"sync"

	"archive-recovery/internal/media"
)

// Unit is one complete coding unit located in the source byte stream.
type Unit struct {
	Offset     int64 // absolute offset of the start code / OBU header
	Length     int64 // full on-wire length
	TypeCode   int   // NAL unit type or OBU type
	IsFrame    bool  // VCL unit carrying picture data
	IsKeyframe bool  // random-access point
}

// Splitter is a streaming, offset-aware unit boundary detector. Write may be
// called repeatedly with fixed-size chunks; Close flushes the final unit or
// reports a dangling (truncated) one. Implementations retain at most one
// in-flight unit plus a few scan bytes.
type Splitter interface {
	Write(p []byte) (units []Unit, errs []media.UnitError, err error)
	Close() (units []Unit, dangling *media.UnitError)
}

// Adapter is a versioned parser for one codec.
type Adapter interface {
	Codec() string
	Version() string
	MaxUnitSize() int64
	NewSplitter() Splitter
}

var (
	regMu     sync.RWMutex
	registry  = map[string]map[string]Adapter{}
)

// Register installs an adapter version. It panics on duplicate registration
// because duplicates are a programming error, not runtime input.
func Register(a Adapter) {
	regMu.Lock()
	defer regMu.Unlock()
	vs, ok := registry[a.Codec()]
	if !ok {
		vs = map[string]Adapter{}
		registry[a.Codec()] = vs
	}
	if _, dup := vs[a.Version()]; dup {
		panic("adapter: duplicate registration " + a.Codec() + "@" + a.Version())
	}
	vs[a.Version()] = a
}

// Get requires an exact codec+version match; unknown versions fail explicitly
// instead of silently falling back to another parser.
func Get(codec, version string) (Adapter, error) {
	regMu.RLock()
	defer regMu.RUnlock()
	vs, ok := registry[codec]
	if !ok {
		return nil, media.NewError(media.ClassInvalidArgument, "adapter",
			"unknown codec: "+codec)
	}
	a, ok := vs[version]
	if !ok {
		return nil, media.NewError(media.ClassInvalidArgument, "adapter",
			"unknown adapter version "+codec+"@"+version+" (no silent downgrade)")
	}
	return a, nil
}

// Latest returns the highest registered version for a codec.
func Latest(codec string) (Adapter, error) {
	regMu.RLock()
	defer regMu.RUnlock()
	vs, ok := registry[codec]
	if !ok {
		return nil, media.NewError(media.ClassInvalidArgument, "adapter",
			"unknown codec: "+codec)
	}
	versions := make([]string, 0, len(vs))
	for v := range vs {
		versions = append(versions, v)
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i] < versions[j] })
	return vs[versions[len(versions)-1]], nil
}

// SupportedCodecs lists registered codec names.
func SupportedCodecs() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]string, 0, len(registry))
	for c := range registry {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}
