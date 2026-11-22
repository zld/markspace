package input

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"strings"

	"dashcheck/internal/model"
)

// Load reads and strictly validates an encoding-rend description file.
// Unknown JSON fields are rejected so a typo can never silently drop
// metadata from the timeline.
func Load(path string) (*model.Description, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, model.Errorf(model.CategoryInput, model.CodeInvalidValue,
			"cannot read input file %q: %v", path, err)
	}
	return Parse(raw)
}

// Parse validates raw JSON bytes; it is split from Load for tests.
func Parse(raw []byte) (*model.Description, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var desc model.Description
	if err := dec.Decode(&desc); err != nil {
		return nil, model.Errorf(model.CategoryInput, model.CodeInvalidValue,
			"invalid JSON: %v", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return nil, model.Errorf(model.CategoryInput, model.CodeInvalidValue,
				"invalid JSON: multiple top-level documents")
		}
		return nil, model.Errorf(model.CategoryInput, model.CodeInvalidValue,
			"invalid JSON: %v", err)
	}
	if err := validate(&desc); err != nil {
		return nil, err
	}
	return &desc, nil
}

func validate(d *model.Description) error {
	if strings.TrimSpace(d.ContentID) == "" {
		return missing("content_id")
	}
	if len(d.Representations) == 0 {
		return missing("representations")
	}
	seenID := make(map[string]bool, len(d.Representations))
	for i := range d.Representations {
		r := &d.Representations[i]
		label := fmt.Sprintf("representations[%d]", i)
		if strings.TrimSpace(r.ID) == "" {
			return missing(label + ".id")
		}
		if seenID[r.ID] {
			return model.Errorf(model.CategoryInput, model.CodeDuplicate,
				"duplicate representation id %q", r.ID)
		}
		seenID[r.ID] = true
		if r.Bandwidth <= 0 {
			return model.Errorf(model.CategoryInput, model.CodeInvalidValue,
				"representation %q: bandwidth_bps must be positive, got %d",
				r.ID, r.Bandwidth)
		}
		if r.Width <= 0 || r.Height <= 0 {
			return model.Errorf(model.CategoryInput, model.CodeInvalidValue,
				"representation %q: width/height must be positive, got %dx%d",
				r.ID, r.Width, r.Height)
		}
		if r.Timescale <= 0 {
			return model.Errorf(model.CategoryInput, model.CodeInvalidValue,
				"representation %q: timescale must be positive, got %d",
				r.ID, r.Timescale)
		}
		if err := validateSegments(r); err != nil {
			return err
		}
	}
	return nil
}

func validateSegments(r *model.RepresentationInput) error {
	if len(r.Segments) == 0 {
		return model.Errorf(model.CategoryInput, model.CodeMissingField,
			"representation %q: segments must not be empty", r.ID)
	}
	seenNum := make(map[int64]bool, len(r.Segments))
	var prevStart int64
	for i, s := range r.Segments {
		label := fmt.Sprintf("representation %q segment[%d]", r.ID, i)
		if s.Duration <= 0 {
			return model.Errorf(model.CategoryInput, model.CodeInvalidValue,
				"%s: duration_tick must be positive, got %d", label, s.Duration)
		}
		if s.StartTick < 0 {
			return model.Errorf(model.CategoryInput, model.CodeInvalidValue,
				"%s: start_tick must not be negative, got %d", label, s.StartTick)
		}
		if i > 0 && s.StartTick <= prevStart {
			return model.Errorf(model.CategoryInput, model.CodeNotMonotonic,
				"%s: start_tick %d must be strictly greater than previous %d",
				label, s.StartTick, prevStart)
		}
		if seenNum[s.Number] {
			return model.Errorf(model.CategoryInput, model.CodeDuplicate,
				"%s: segment number %d repeats", label, s.Number)
		}
		seenNum[s.Number] = true
		// Detect end = start + duration overflow before any alignment.
		if s.StartTick > math.MaxInt64-s.Duration {
			return model.Errorf(model.CategoryAlignment, model.CodeOverflow,
				"%s: start_tick+duration_tick overflows int64", label)
		}
		prevStart = s.StartTick
	}
	return nil
}

func missing(field string) error {
	return model.Errorf(model.CategoryInput, model.CodeMissingField,
		"missing or empty required field %q", field)
}
