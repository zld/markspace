// Package index parses and validates external keyframe index files. An
// index entry is a (frame, byte offset, type) triple. Invalid entries are
// retained with an explicit reason and never silently rebound to a
// neighbouring frame.
package index

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"

	"mediad/internal/errors"
	"mediad/internal/stream"
)

// RawEntry is one line of the index file before semantic validation.
type RawEntry struct {
	Line  int    `json:"line"`
	Frame uint64 `json:"frame"`
	Off   int64  `json:"offset"`
	Type  string `json:"type"`
}

// Entry is a validated or explicitly invalidated index record.
type Entry struct {
	Raw    RawEntry `json:"raw"`
	Valid  bool     `json:"valid"`
	Reason string   `json:"reason,omitempty"`
}

// Index is the full parsed and validated index.
type Index struct {
	Entries      []Entry `json:"entries"`
	ValidCount   int     `json:"valid_count"`
	InvalidCount int     `json:"invalid_count"`
}

// allowedTypes lists the keyframe type labels accepted per codec.
var allowedTypes = map[stream.Codec]map[string]bool{
	stream.H264: {"IDR": true},
	stream.HEVC: {"IDR_W_RADL": true, "IDR_N_LP": true, "CRA": true},
	stream.AV1:  {"KEY": true},
}

// Parse reads the line-oriented index format:
//
//	# comment lines and blank lines are ignored
//	frame offset type
//
// Malformed lines are returned as entries whose validation will fail with a
// precise reason; parsing itself only fails on unrecoverable I/O errs.
func Parse(r io.Reader) ([]RawEntry, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var raws []RawEntry
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			raws = append(raws, RawEntry{Line: lineNo, Type: strings.Join(fields, " ")})
			continue
		}
		frame, errF := strconv.ParseUint(fields[0], 10, 64)
		off, errO := strconv.ParseInt(fields[1], 10, 64)
		raws = append(raws, RawEntry{
			Line:  lineNo,
			Frame: frame,
			Off:   off,
			Type:  fields[2],
		})
		if errF != nil || errO != nil {
			// Keep the raw text marker so Validate reports a numeric error.
			raws[len(raws)-1].Type = fields[2] + "|bad-number"
		}
	}
	if err := sc.Err(); err != nil {
		return nil, errs.New(errs.Fatal, errs.CodeIOFailure, "index scan: %v", err)
	}
	return raws, nil
}

// Validate applies the three structural rules: offsets must be within the
// stream range, strictly monotonic against the last *valid* entry, and the
// type label must be a known keyframe type for the codec.
func Validate(codec stream.Codec, raws []RawEntry, streamSize int64) (*Index, error) {
	types, ok := allowedTypes[codec]
	if !ok {
		return nil, errs.New(errs.Fatal, errs.CodeWrongCodecType,
			"no keyframe type table for codec %q", codec)
	}
	idx := &Index{}
	var lastValidOff int64 = -1
	for _, raw := range raws {
		e := Entry{Raw: raw, Valid: true}
		switch {
		case strings.HasSuffix(raw.Type, "|bad-number"):
			e.Valid = false
			e.Reason = "frame/offset fields are not numeric"
			e.Raw.Type = strings.TrimSuffix(e.Raw.Type, "|bad-number")
		case !types[raw.Type]:
			e.Valid = false
			e.Reason = fmt.Sprintf("type %q is not a %s keyframe type", raw.Type, codec)
		case raw.Off < 0 || raw.Off >= streamSize:
			e.Valid = false
			e.Reason = fmt.Sprintf("offset %d outside stream range [0,%d)", raw.Off, streamSize)
		case raw.Off <= lastValidOff:
			e.Valid = false
			e.Reason = fmt.Sprintf("offset %d not strictly greater than previous valid offset %d",
				raw.Off, lastValidOff)
		}
		if e.Valid {
			lastValidOff = raw.Off
			idx.ValidCount++
		} else {
			idx.InvalidCount++
		}
		idx.Entries = append(idx.Entries, e)
	}
	return idx, nil
}

// ValidOffsets returns the offsets of valid entries in file order.
func (i *Index) ValidOffsets() []int64 {
	out := make([]int64, 0, i.ValidCount)
	for _, e := range i.Entries {
		if e.Valid {
			out = append(out, e.Raw.Off)
		}
	}
	return out
}
