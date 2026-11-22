// Package adapter defines a versioned parser interface for container formats
// and a registry that resolves an adapter for a declared version and codec
// before any output is written.
//
// Adapters must fail rather than guess: an unsupported major version or codec
// is an error, a truncated sample is reported with its byte offset, and no
// adapter may read past the end of its input buffer.
package adapter

import (
	"fmt"
	"sort"

	"example.com/mediaforge/internal/merrors"
)

// Major is the interface version an adapter implements.
type Major int

// Format identifies a container.
type Format string

const (
	FormatMP4 Format = "mp4"
)

// Codec identifies a sample codec.
type Codec string

const (
	CodecH264 Codec = "h264"
	CodecAAC  Codec = "aac"
	CodecOpus Codec = "opus"
)

// Key identifies one adapter implementation.
type Key struct {
	Format Format
	Major  Major
}

// TrackInfo is the parsed description of one track.
type TrackInfo struct {
	ID          uint32
	Codec       Codec
	Timescale   uint32
	SampleCnt   int
	Keyframes   []int    // 0-based sample indices flagged sync
	Durations   []uint64 // per-sample duration in timescale units
	CodecConfig []byte
}

// Sample is one decoded sample.
type Sample struct {
	Index      int
	Duration   uint64
	IsKeyframe bool
	Data       []byte
	// SourceOffset is the byte offset of this sample's data in the input, so
	// truncation errors can point at it.
	SourceOffset int64
}

// Container is the parsed container handed to adapters.
type Container struct {
	Format Format
	Major  Major
	Tracks []TrackInfo
	// Samples is keyed by track ID.
	Samples map[uint32][]Sample
	// Data is the raw input the container was parsed from.
	Data []byte
}

// ParseResult carries the parsed container plus any non-fatal findings.
type ParseResult struct {
	Container *Container
	Warnings  []error
}

// Adapter is the versioned parser contract.
type Adapter interface {
	// Key reports which format and major version this adapter handles.
	Key() Key
	// Sniff reports whether buf plausibly belongs to this adapter's format.
	// It must not read beyond buf.
	Sniff(buf []byte) bool
	// Parse decodes buf into a container. Errors must carry the adapter
	// stage, a category, and the byte offset of any truncated or malformed
	// region.
	Parse(buf []byte) (ParseResult, error)
	// Encode rewrites a container. Implementations must fail before writing
	// anything when the container holds a codec or version they cannot
	// express.
	Encode(c *Container) ([]byte, error)
}

// Registry holds adapters by key.
type Registry struct {
	adapters map[Key]Adapter
	order    []Key
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{adapters: make(map[Key]Adapter)}
}

// ErrDuplicateAdapter is returned when a key is registered twice.
var ErrDuplicateAdapter = fmt.Errorf("duplicate adapter registration")

// Register adds an adapter. A duplicate key is an error rather than a silent
// override: version resolution must be unambiguous.
func (r *Registry) Register(a Adapter) error {
	k := a.Key()
	if _, exists := r.adapters[k]; exists {
		return merrors.New(merrors.StageAdapter, merrors.Invalid,
			"adapter %s v%d already registered", k.Format, k.Major)
	}
	r.adapters[k] = a
	r.order = append(r.order, k)
	return nil
}

// Lookup returns the adapter for an exact key.
func (r *Registry) Lookup(k Key) (Adapter, bool) {
	a, ok := r.adapters[k]
	return a, ok
}

// Resolve finds an adapter by format and major version. An absent version
// yields an Unsupported error listing the versions that are available.
func (r *Registry) Resolve(format Format, major Major) (Adapter, error) {
	a, ok := r.adapters[Key{Format: format, Major: major}]
	if !ok {
		return nil, merrors.New(merrors.StageAdapter, merrors.Unsupported,
			"no adapter for %s v%d (have %v)", format, major, r.Versions(format))
	}
	return a, nil
}

// Versions lists the registered major versions for a format, ascending.
func (r *Registry) Versions(format Format) []Major {
	var out []Major
	for _, k := range r.order {
		if k.Format == format {
			out = append(out, k.Major)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Detect finds the highest-versioned adapter whose Sniff accepts buf. It is
// used when the caller does not know the container version up front.
func (r *Registry) Detect(buf []byte) (Adapter, error) {
	keys := append([]Key(nil), r.order...)
	sort.SliceStable(keys, func(i, j int) bool {
		if keys[i].Format != keys[j].Format {
			return keys[i].Format < keys[j].Format
		}
		return keys[i].Major > keys[j].Major
	})
	for _, k := range keys {
		if r.adapters[k].Sniff(buf) {
			return r.adapters[k], nil
		}
	}
	return nil, merrors.New(merrors.StageAdapter, merrors.Unsupported,
		"no adapter recognises the input")
}

// Supports reports whether an adapter can express codec for this format.
func Supports(format Format, codec Codec) bool {
	switch format {
	case FormatMP4:
		return codec == CodecH264 || codec == CodecAAC
	}
	return false
}
