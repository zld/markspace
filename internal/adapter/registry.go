package adapter

import (
	"fmt"
	"io"
	"sync"

	"medpipe/internal/clock"
)

// Document is the parser-neutral result returned by a versioned adapter.
type Document struct {
	ParserMajor int    `json:"parser_major"`
	Format      string `json:"format"`
	Boxes       []Box  `json:"boxes"`
	Segments    int    `json:"segments,omitempty"`
}

// Box is a minimal MP4 box descriptor.
type Box struct {
	Type   string `json:"type"`
	Offset uint64 `json:"offset"`
	Size   uint64 `json:"size"`
}

// Decoder is implemented by exactly one explicit parser major version.
type Decoder interface {
	ParserMajor() int
	Format() string
	Decode(r io.ReadSeeker, base *clock.Location) (*Document, error)
}

// Registry only contains explicitly registered parser majors.
type Registry struct {
	mu       sync.RWMutex
	decoders map[int]Decoder
}

func NewRegistry() *Registry {
	return &Registry{decoders: make(map[int]Decoder)}
}

var defaultRegistry = NewRegistry()

func DefaultRegistry() *Registry { return defaultRegistry }

func (r *Registry) Register(d Decoder) error {
	if d == nil {
		return clock.Errorf(clock.CodeMissingField, nil, "cannot register nil parser")
	}
	major := d.ParserMajor()
	if major <= 0 {
		return clock.Errorf(clock.CodeUnsupportedParserMajor, &clock.Location{ParserMajor: major},
			"parser major version must be positive")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.decoders[major]; exists {
		return clock.Errorf(clock.CodeUnsupportedParserMajor, &clock.Location{ParserMajor: major},
			"parser major version %d already registered", major)
	}
	r.decoders[major] = d
	return nil
}

func Register(d Decoder) error { return defaultRegistry.Register(d) }

func (r *Registry) Get(major int) (Decoder, *clock.Error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.decoders[major]
	if !ok {
		return nil, clock.Errorf(clock.CodeUnsupportedParserMajor, &clock.Location{ParserMajor: major},
			"no explicitly registered parser for major version %d", major)
	}
	return d, nil
}

func Get(major int) (Decoder, *clock.Error) { return defaultRegistry.Get(major) }

// DecodeWithMajor is a convenience boundary used by the kernel.
func DecodeWithMajor(major int, r io.ReadSeeker, loc *clock.Location) (*Document, error) {
	d, err := Get(major)
	if err != nil {
		return nil, err
	}
	doc, derr := d.Decode(r, loc)
	if derr != nil {
		if ce, ok := clock.AsError(derr); ok {
			return nil, ce
		}
		return nil, clock.Wrap(clock.CodeInvalidPacket, loc, derr, fmt.Sprintf("%s parser failed", d.Format()))
	}
	return doc, nil
}
