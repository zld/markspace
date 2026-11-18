// Package input reads and structurally validates local JSON encoding metadata.
// It never fills in missing fragments: incomplete or malformed input is an
// error, not an implicit rendition.
package input

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"dashforge/internal/model"
)

// Error describes one rejected field, scoped to a rendition when applicable.
// Field is the metadata JSON field name; Rendition is the rendition id, or
// "<rendition[N]>" when the id itself is missing.
type Error struct {
	Rendition string `json:"rendition,omitempty"`
	Field     string `json:"field"`
	Message   string `json:"message"`
}

func (e Error) String() string {
	if e.Rendition == "" {
		return fmt.Sprintf("%s: %s", e.Field, e.Message)
	}
	return fmt.Sprintf("rendition %q: %s: %s", e.Rendition, e.Field, e.Message)
}

// Errors is the deterministic, sorted aggregate of all input problems found.
type Errors []Error

func (es Errors) Error() string {
	parts := make([]string, len(es))
	for i, e := range es {
		parts[i] = e.String()
	}
	return strings.Join(parts, "; ")
}

// supportedCodecs is the whitelist of codec identifier families. RFC 6381
// codec strings carry parameters after '.' (e.g. "avc1.640028"), so the check
// is a family-prefix match rather than exact equality.
var supportedCodecs = []string{"avc1", "hev1", "hvc1", "av01", "mp4a"}

// Load reads metadata from path and runs structural validation. Read failures
// (missing file, permissions) are returned as ordinary errors; semantically
// invalid metadata is returned as Errors so the CLI can map exit codes.
func Load(path string, maxReps, maxSegments int) (*model.Metadata, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open metadata: %w", err)
	}
	defer f.Close()
	return Decode(f, maxReps, maxSegments)
}

// Decode parses and validates metadata from a reader. Truncated JSON, trailing
// garbage and unknown fields are all rejected explicitly.
func Decode(r io.Reader, maxReps, maxSegments int) (*model.Metadata, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()

	var meta model.Metadata
	if err := dec.Decode(&meta); err != nil {
		return nil, Errors{{Field: "file", Message: "invalid JSON: " + err.Error()}}
	}
	// Reject documents containing a second JSON value or trailing tokens.
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return nil, Errors{{Field: "file", Message: "unexpected data after JSON document"}}
		}
		return nil, Errors{{Field: "file", Message: "invalid JSON: " + err.Error()}}
	}
	if err := Validate(&meta, maxReps, maxSegments); err != nil {
		return nil, err
	}
	return &meta, nil
}

// Validate checks every structural rule that can be decided from the metadata
// alone. Cross-rendition timeline alignment lives in the validate package.
func Validate(meta *model.Metadata, maxReps, maxSegments int) error {
	var errs Errors
	if len(meta.Renditions) == 0 {
		errs = append(errs, Error{Field: "renditions", Message: "at least one rendition is required"})
	}
	if len(meta.Renditions) > maxReps {
		errs = append(errs, Error{
			Field:   "renditions",
			Message: fmt.Sprintf("count %d exceeds limit %d", len(meta.Renditions), maxReps),
		})
	}

	seenID := make(map[string]bool)
	totalSegments := 0
	for i := range meta.Renditions {
		rd := &meta.Renditions[i]
		label := rd.ID
		if label == "" {
			label = fmt.Sprintf("<rendition[%d]>", i)
		}
		add := func(field, msg string) {
			errs = append(errs, Error{Rendition: label, Field: field, Message: msg})
		}

		if rd.ID == "" {
			add("id", "missing or empty")
		} else if seenID[rd.ID] {
			add("id", "duplicate rendition id")
		} else {
			seenID[rd.ID] = true
		}
		if rd.Width <= 0 {
			add("width", "must be a positive integer")
		}
		if rd.Height <= 0 {
			add("height", "must be a positive integer")
		}
		if rd.Bandwidth <= 0 {
			add("bandwidth_bps", "must be a positive integer")
		}
		if rd.Codec == "" {
			add("codec", "missing or empty")
		} else if !codecSupported(rd.Codec) {
			add("codec", fmt.Sprintf("unsupported codec identifier %q", rd.Codec))
		}
		if rd.FrameRate == "" {
			add("frame_rate", "missing or empty")
		}
		if rd.StartOffsetMs < 0 {
			add("start_offset_ms", "must not be negative")
		}
		if rd.SegmentDurationMs <= 0 {
			add("segment_duration_ms", "must be a positive integer")
		}
		if rd.Media == "" {
			add("media", "missing segment template")
		}
		if rd.SegmentRoot == "" {
			add("segment_root", "missing segment root directory")
		}
		if len(rd.Segments) == 0 {
			add("segments", "at least one segment is required")
		}
		if len(rd.Segments) > maxSegments {
			add("segments", fmt.Sprintf("count %d exceeds limit %d", len(rd.Segments), maxSegments))
		}
		totalSegments += len(rd.Segments)

		seenNumber := make(map[int]bool, len(rd.Segments))
		for _, seg := range rd.Segments {
			if seg.Number <= 0 {
				add("segments.number", fmt.Sprintf("segment number must be positive, got %d", seg.Number))
			} else if seenNumber[seg.Number] {
				add("segments.number", fmt.Sprintf("duplicate segment number %d", seg.Number))
			} else {
				seenNumber[seg.Number] = true
			}
			if seg.StartMs < 0 {
				add("segments.start_ms", fmt.Sprintf("segment %d: start must not be negative", seg.Number))
			}
			if seg.DurationMs <= 0 {
				add("segments.duration_ms", fmt.Sprintf("segment %d: duration must be positive", seg.Number))
			}
		}
	}
	if totalSegments > maxSegments {
		errs = append(errs, Error{
			Field:   "segments",
			Message: fmt.Sprintf("total count %d exceeds limit %d", totalSegments, maxSegments),
		})
	}

	sort.SliceStable(errs, func(i, j int) bool {
		if errs[i].Rendition != errs[j].Rendition {
			return errs[i].Rendition < errs[j].Rendition
		}
		return errs[i].Field < errs[j].Field
	})
	if len(errs) > 0 {
		return errs
	}
	return nil
}

func codecSupported(codec string) bool {
	family := codec
	if i := strings.IndexByte(codec, '.'); i >= 0 {
		family = codec[:i]
	}
	for _, c := range supportedCodecs {
		if family == c {
			return true
		}
	}
	return false
}
