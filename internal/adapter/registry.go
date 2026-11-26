// Package adapter contains versioned media parsers and codecs.
package adapter

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"assetpack/internal/clock"
)

const (
	FormatMP4   = "mp4"
	FormatFMP4  = "fmp4"
	FormatRTP   = "rtp"
	CodecRGBA8  = "rgba8"
	CodecPCMS16 = "pcm-s16le"

	APIVersion = 1
)

var (
	ErrUnknownVersion = errors.New("unknown adapter version")
	ErrUnknownCodec   = errors.New("unknown codec version")
	ErrBadInput       = errors.New("malformed media input")
)

// ID is a stable versioned adapter identifier such as mp4/v1 or rtp/v1.
type ID struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
}

func (id ID) String() string { return fmt.Sprintf("%s/v%d", id.Name, id.Version) }

// Descriptor describes one registered format, transport, or codec.
type Descriptor struct {
	ID      ID     `json:"id"`
	Kind    string `json:"kind"`
	Codec   string `json:"codec,omitempty"`
	ClockHz int64  `json:"clock_hz,omitempty"`
}

// Source is a bounded local input. Data takes precedence for generated samples.
type Source struct {
	Name           string
	Path           string
	Data           []byte
	MaxWindowBytes int64
}

// Reader returns a ReaderAt and its total size.
func (s Source) Reader() (io.ReaderAt, int64, error) {
	if s.Data != nil {
		return bytes.NewReader(s.Data), int64(len(s.Data)), nil
	}
	if s.Path != "" {
		f, err := os.Open(s.Path)
		if err != nil {
			return nil, 0, err
		}
		st, err := f.Stat()
		if err != nil {
			f.Close()
			return nil, 0, err
		}
		return f, st.Size(), nil
	}
	return nil, 0, fmt.Errorf("%w: source has neither data nor path", ErrBadInput)
}

// Sample is one addressable media sample or packet payload.
type Sample struct {
	Index    int         `json:"index"`
	TrackID  string      `json:"track_id"`
	PTS      clock.Ticks `json:"pts"`
	Duration clock.Ticks `json:"duration"`
	Offset   int64       `json:"offset"`
	Size     int         `json:"size"`
	Key      bool        `json:"key"`
	Preroll  []int       `json:"preroll,omitempty"`
	Seq      uint16      `json:"rtp_seq,omitempty"`
	Payload  []byte      `json:"-"`
}

// Track is a normalized elementary stream.
type Track struct {
	ID         string         `json:"id"`
	Kind       string         `json:"kind"`
	Codec      ID             `json:"codec"`
	Timebase   clock.Rational `json:"timebase"`
	Duration   clock.Ticks    `json:"duration"`
	Width      int            `json:"width,omitempty"`
	Height     int            `json:"height,omitempty"`
	SampleRate int            `json:"sample_rate,omitempty"`
	Channels   int            `json:"channels,omitempty"`
	Samples    []Sample       `json:"samples"`
}

// Event is a timed metadata event such as MP4 emsg.
type Event struct {
	ID              int         `json:"id"`
	TrackID         string      `json:"track_id"`
	PTS             clock.Ticks `json:"pts"`
	Duration        clock.Ticks `json:"duration"`
	SchemeIDURI     string      `json:"scheme_id_uri"`
	Value           string      `json:"value"`
	SourceTimescale uint32      `json:"source_timescale"`
	SourcePTS       uint64      `json:"source_pts"`
	MessageData     []byte      `json:"message_data,omitempty"`
}

// Asset is the parser output consumed by the index compiler.
type Asset struct {
	Descriptor Descriptor  `json:"descriptor"`
	SourceName string      `json:"source_name"`
	SourceSHA  string      `json:"source_sha256"`
	Duration   clock.Ticks `json:"duration"`
	Tracks     []*Track    `json:"tracks"`
	Events     []Event     `json:"events,omitempty"`
}

// FormatAdapter parses a container or transport into the unified model.
type FormatAdapter interface {
	Descriptor() Descriptor
	Detect(head []byte) bool
	Parse(ctx context.Context, src Source) (*Asset, error)
}

// CodecAdapter validates and converts one of the promised reference codecs.
type CodecAdapter interface {
	Descriptor() Descriptor
	Validate(t *Track) error
}

// Registry is the versioned plugin table.
type Registry struct {
	formats map[string]FormatAdapter
	codecs  map[string]CodecAdapter
}

func NewRegistry() *Registry {
	return &Registry{formats: map[string]FormatAdapter{}, codecs: map[string]CodecAdapter{}}
}

func (r *Registry) RegisterFormat(a FormatAdapter) error {
	key := a.Descriptor().ID.String()
	if _, exists := r.formats[key]; exists {
		return fmt.Errorf("adapter already registered: %s", key)
	}
	r.formats[key] = a
	return nil
}

func (r *Registry) RegisterCodec(c CodecAdapter) error {
	key := c.Descriptor().ID.String()
	if _, exists := r.codecs[key]; exists {
		return fmt.Errorf("codec already registered: %s", key)
	}
	r.codecs[key] = c
	return nil
}

func (r *Registry) Format(id string) (FormatAdapter, error) {
	a, ok := r.formats[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownVersion, id)
	}
	return a, nil
}

func (r *Registry) Codec(id string) (CodecAdapter, error) {
	c, ok := r.codecs[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownCodec, id)
	}
	return c, nil
}

// DefaultRegistry contains exactly the formats and codecs promised by the task.
func DefaultRegistry() *Registry {
	r := NewRegistry()
	_ = r.RegisterFormat(NewMP4V1(FormatMP4))
	_ = r.RegisterFormat(NewMP4V1(FormatFMP4))
	_ = r.RegisterCodec(NewRGBA8V1())
	_ = r.RegisterCodec(NewPCMS16LEV1())
	return r
}
