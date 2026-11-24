// Package adapter defines the container parser registry and the unified
// media model. Parsers are registered by major version; unknown major
// versions and unsupported tracks are reported with byte offsets and the
// failing file is skipped without aborting the batch.
package adapter

import (
	"context"
	"fmt"
	"io"
	"sync"

	"mediacheck/internal/clock"
	"mediacheck/internal/fault"
)

// Kind classifies a track.
type Kind int

const (
	KindOther Kind = iota
	KindVideo
	KindAudio
)

func (k Kind) String() string {
	switch k {
	case KindVideo:
		return "video"
	case KindAudio:
		return "audio"
	default:
		return "other"
	}
}

// Sample is one media sample with timestamps in track timescale ticks.
type Sample struct {
	DTS      int64 // decode timestamp, ticks
	CTS      int64 // composition timestamp, ticks
	Duration int64 // sample duration, ticks
	Size     uint32
	Keyframe bool
	Offset   uint64 // byte offset of sample data in the source file
}

// Track is one parsed media track.
type Track struct {
	ID        uint32
	Kind      Kind
	Timescale clock.Timescale
	Samples   []Sample
}

// DurationTicks returns the track duration in timescale ticks.
func (t *Track) DurationTicks() int64 {
	if len(t.Samples) == 0 {
		return 0
	}
	last := t.Samples[len(t.Samples)-1]
	return last.DTS + last.Duration
}

// Media is a parsed container.
type Media struct {
	Path          string
	Tracks        []*Track
	DurationNanos int64
	// Source reads sample payloads from the original file (bounded memory:
	// only the sample table is held, payload bytes are read on demand).
	Source io.ReaderAt
	Close  func() error
}

// FirstTrack returns the first track of the given kind, or nil.
func (m *Media) FirstTrack(kind Kind) *Track {
	for _, t := range m.Tracks {
		if t.Kind == kind {
			return t
		}
	}
	return nil
}

// Parser parses one container generation, identified by its major version.
type Parser interface {
	Major() int
	Parse(ctx context.Context, path string, fl *fault.List) (*Media, error)
}

// Registry maps parser major versions to parser implementations.
type Registry struct {
	mu      sync.RWMutex
	parsers map[int]Parser
}

func NewRegistry() *Registry {
	return &Registry{parsers: make(map[int]Parser)}
}

// Register adds a parser; registering the same major twice fails.
func (r *Registry) Register(p Parser) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.parsers[p.Major()]; dup {
		return fmt.Errorf("adapter: parser major %d already registered", p.Major())
	}
	r.parsers[p.Major()] = p
	return nil
}

// ByMajor returns the parser registered for a major version.
func (r *Registry) ByMajor(major int) (Parser, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.parsers[major]
	return p, ok
}

// Parse resolves the parser for major and runs it. An unknown major is
// recorded as a fault (with the given byte offset) and reported as error;
// callers are expected to skip the file and continue the batch.
func (r *Registry) Parse(ctx context.Context, major int, path string, fl *fault.List, majorOffset int64) (*Media, error) {
	p, ok := r.ByMajor(major)
	if !ok {
		fl.Add(fault.ErrUnknownMajor, majorOffset,
			"no parser registered for major version %d (file %s)", major, path)
		return nil, fmt.Errorf("adapter: unknown parser major %d", major)
	}
	return p.Parse(ctx, path, fl)
}
