// Package plan defines the on-disk JSON segment index consumed by dashgen and
// performs structural validation of representations and segment entries.
package plan

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"dashgen/internal/timebase"
)

// Limits enforced on every input plan.
const (
	MaxRepresentations = 32
	MaxSegmentsPerRep  = 100000
)

// Segment is one encoded media fragment described in its representation's
// native timescale ticks.
type Segment struct {
	Number        int64 `json:"number"`
	StartTicks    int64 `json:"start_ticks"`
	DurationTicks int64 `json:"duration_ticks"`
}

// Representation is one encoding rendition (bitrate ladder rung).
type Representation struct {
	ID          string    `json:"id"`
	BandwidthBPS int64    `json:"bandwidth_bps"`
	Timescale   int64     `json:"timescale"`
	StartTicks  int64     `json:"start_ticks"`
	Segments    []Segment `json:"segments"`
}

// Plan is the full segment index for a media presentation.
type Plan struct {
	Representations []Representation `json:"representations"`
}

// ReadFile loads and validates a plan from path.
func ReadFile(path string) (*Plan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Parse decodes JSON data and validates it structurally. Unknown JSON fields
// are rejected so typos cannot be silently ignored.
func Parse(data []byte) (*Plan, error) {
	var p Plan
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("plan: decode: %w", err)
	}
	if dec.More() {
		return nil, fmt.Errorf("plan: decode: unexpected trailing JSON values")
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

// Validate checks representation limits, unique IDs, positive timescales and
// per-segment numbering, durations and integer overflow. Timeline ordering,
// gaps and cross-representation alignment are checked elsewhere.
func (p *Plan) Validate() error {
	n := len(p.Representations)
	if n == 0 {
		return fmt.Errorf("plan: no representations")
	}
	if n > MaxRepresentations {
		return fmt.Errorf("plan: %d representations exceed limit %d", n, MaxRepresentations)
	}
	seenIDs := make(map[string]bool, n)
	for i := range p.Representations {
		rep := &p.Representations[i]
		if rep.ID == "" {
			return fmt.Errorf("plan: representation[%d] has empty id", i)
		}
		if seenIDs[rep.ID] {
			return fmt.Errorf("plan: duplicate representation id %q", rep.ID)
		}
		seenIDs[rep.ID] = true
		if rep.Timescale <= 0 {
			return fmt.Errorf("plan: representation %q: timescale must be positive, got %d", rep.ID, rep.Timescale)
		}
		if rep.BandwidthBPS <= 0 {
			return fmt.Errorf("plan: representation %q: bandwidth_bps must be positive", rep.ID)
		}
		if rep.StartTicks < 0 {
			return fmt.Errorf("plan: representation %q: start_ticks must not be negative", rep.ID)
		}
		if err := validateSegments(rep); err != nil {
			return err
		}
	}
	return nil
}

func validateSegments(rep *Representation) error {
	n := len(rep.Segments)
	if n == 0 {
		return fmt.Errorf("plan: representation %q: no segments", rep.ID)
	}
	if n > MaxSegmentsPerRep {
		return fmt.Errorf("plan: representation %q: %d segments exceed limit %d", rep.ID, n, MaxSegmentsPerRep)
	}
	seenNumbers := make(map[int64]bool, n)
	var prevNumber int64
	for i := range rep.Segments {
		seg := &rep.Segments[i]
		if seg.Number < 1 {
			return fmt.Errorf("plan: %q segment index %d: number must be >= 1, got %d", rep.ID, i, seg.Number)
		}
		if seenNumbers[seg.Number] {
			return fmt.Errorf("plan: %q: duplicate segment number %d", rep.ID, seg.Number)
		}
		seenNumbers[seg.Number] = true
		if i > 0 && seg.Number <= prevNumber {
			return fmt.Errorf("plan: %q: segment numbers out of order at number %d", rep.ID, seg.Number)
		}
		prevNumber = seg.Number
		if seg.StartTicks < 0 {
			return fmt.Errorf("plan: %q segment %d: negative start_ticks", rep.ID, seg.Number)
		}
		if seg.DurationTicks <= 0 {
			return fmt.Errorf("plan: %q segment %d: duration must be positive, got %d", rep.ID, seg.Number, seg.DurationTicks)
		}
		if _, ok := timebase.Add64(seg.StartTicks, seg.DurationTicks); !ok {
			return fmt.Errorf("plan: %q segment %d: start+duration overflows int64", rep.ID, seg.Number)
		}
	}
	return nil
}
