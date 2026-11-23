// Package index parses, validates and aligns keyframe indexes against actual
// elementary-stream boundaries, including RTP carriage of index events.
package index

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"archivecheck/internal/media"
)

// Entry is one keyframe index record.
type Entry struct {
	PTS    int64
	Offset int64
	Line   int
}

// Index is a parsed keyframe index for one representation layer.
type Index struct {
	Layer     string
	Timescale uint32
	Entries   []Entry
}

// ClockMillis converts a PTS on this index's timescale to milliseconds.
func (ix Index) ClockMillis(pts int64) int64 {
	if ix.Timescale == 0 {
		return 0
	}
	ts := int64(ix.Timescale)
	if pts >= 0 {
		return (pts*1000 + ts/2) / ts
	}
	return -((-pts*1000 + ts/2) / ts)
}

// Parse reads the line-oriented archive index format:
//
//	archive-keyframe-index v1
//	layer=high
//	timescale=60000
//	entry pts=0 offset=0
func Parse(b []byte) (Index, error) {
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 4096), 1<<20)
	var ix Index
	line := 0
	seenHeader := false
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		switch {
		case !seenHeader:
			if text != "archive-keyframe-index v1" {
				return Index{}, fmt.Errorf("index: line %d: unsupported header %q", line, text)
			}
			seenHeader = true
		case strings.HasPrefix(text, "layer="):
			ix.Layer = strings.TrimPrefix(text, "layer=")
		case strings.HasPrefix(text, "timescale="):
			v, err := strconv.ParseUint(strings.TrimPrefix(text, "timescale="), 10, 32)
			if err != nil || v == 0 {
				return Index{}, fmt.Errorf("index: line %d: invalid timescale", line)
			}
			ix.Timescale = uint32(v)
		case strings.HasPrefix(text, "entry "):
			e, err := parseEntry(strings.TrimPrefix(text, "entry "))
			if err != nil {
				return Index{}, fmt.Errorf("index: line %d: %w", line, err)
			}
			e.Line = line
			ix.Entries = append(ix.Entries, e)
		default:
			return Index{}, fmt.Errorf("index: line %d: unexpected field %q", line, text)
		}
	}
	if err := sc.Err(); err != nil {
		return Index{}, err
	}
	if !seenHeader {
		return Index{}, errors.New("index: missing header")
	}
	if ix.Timescale == 0 {
		return Index{}, errors.New("index: missing timescale")
	}
	if len(ix.Entries) == 0 {
		return Index{}, errors.New("index: no entries")
	}
	return ix, nil
}

func parseEntry(fields string) (Entry, error) {
	var e Entry
	var havePTS, haveOff bool
	for _, f := range strings.Fields(fields) {
		k, v, ok := strings.Cut(f, "=")
		if !ok {
			return Entry{}, fmt.Errorf("malformed field %q", f)
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return Entry{}, fmt.Errorf("invalid number in %q", f)
		}
		switch k {
		case "pts":
			e.PTS, havePTS = n, true
		case "offset":
			e.Offset, haveOff = n, true
		default:
			return Entry{}, fmt.Errorf("unknown field %q", k)
		}
	}
	if !havePTS || !haveOff {
		return Entry{}, errors.New("entry requires pts and offset")
	}
	return e, nil
}

// ValidationOptions tunes offset alignment.
type ValidationOptions struct {
	// DriftTolerance is the largest byte distance accepted as an alignment
	// drift rather than a bounding error.
	DriftTolerance int64
}

// Aligned is an index entry that survived validation and was snapped onto a
// real keyframe boundary.
type Aligned struct {
	Entry
	Boundary int64 // actual keyframe boundary the entry resolved to
	Drift    int64 // signed byte drift from the recorded offset
}

// Rejected is one index entry that failed validation, kept so downstream
// stages can attribute the coverage hole to its actual cause.
type Rejected struct {
	Entry  Entry
	Kind   media.FaultKind
	Detail string
}

// Validation is the deterministic outcome of index checking.
type Validation struct {
	Index    Index
	Aligned  []Aligned
	Rejected []Rejected
	Faults   []media.Fault
	Dropped  int
}

// Validate compares index entries with the actual stream length and the
// sorted keyframe boundaries discovered by the codec parser. Out-of-range
// offsets, duplicate offsets and time regressions are always reported; nothing
// is silently skipped.
func Validate(ix Index, streamLen int64, boundaries []int64, opts ValidationOptions) Validation {
	if opts.DriftTolerance <= 0 {
		opts.DriftTolerance = 4
	}
	v := Validation{Index: ix}
	seenOffset := map[int64]int{}
	var lastPTS int64
	havePTS := false
	for _, e := range ix.Entries {
		if e.Offset < 0 || e.Offset >= streamLen {
			v.Dropped++
			detail := fmt.Sprintf("line %d: offset %d outside stream length %d", e.Line, e.Offset, streamLen)
			v.Faults = append(v.Faults, media.NewFault(media.KindIndexOutOfRange, ix.Layer, detail, e.Offset, media.Unrecoverable))
			v.Rejected = append(v.Rejected, Rejected{Entry: e, Kind: media.KindIndexOutOfRange, Detail: detail})
			continue
		}
		if prevLine, dup := seenOffset[e.Offset]; dup {
			v.Dropped++
			detail := fmt.Sprintf("line %d: offset %d already used on line %d", e.Line, e.Offset, prevLine)
			v.Faults = append(v.Faults, media.NewFault(media.KindIndexDuplicate, ix.Layer, detail, e.Offset, media.Recoverable))
			v.Rejected = append(v.Rejected, Rejected{Entry: e, Kind: media.KindIndexDuplicate, Detail: detail})
			continue
		}
		if havePTS && e.PTS <= lastPTS {
			v.Dropped++
			spanStart, spanEnd := e.PTS, lastPTS
			if spanEnd < spanStart {
				spanStart, spanEnd = spanEnd, spanStart
			}
			detail := fmt.Sprintf("line %d: pts %d does not advance past %d", e.Line, e.PTS, lastPTS)
			v.Faults = append(v.Faults, media.NewFault(media.KindIndexRegress, ix.Layer, detail,
				e.Offset, media.Recoverable).WithSpan(media.NewInterval(spanStart, spanEnd, media.MustClock(ix.Timescale))))
			v.Rejected = append(v.Rejected, Rejected{Entry: e, Kind: media.KindIndexRegress, Detail: detail})
			continue
		}
		boundary, drift, ok := nearest(boundaries, e.Offset)
		if !ok || drift > opts.DriftTolerance {
			v.Dropped++
			kind := media.KindBoundaryDrift
			detail := fmt.Sprintf("line %d: offset %d does not match any keyframe boundary", e.Line, e.Offset)
			if ok {
				detail = fmt.Sprintf("line %d: offset %d drifts %d bytes from boundary %d", e.Line, e.Offset, drift, boundary)
			}
			v.Faults = append(v.Faults, media.NewFault(kind, ix.Layer, detail, e.Offset, media.Recoverable))
			v.Rejected = append(v.Rejected, Rejected{Entry: e, Kind: kind, Detail: detail})
			continue
		}
		if drift != 0 {
			v.Faults = append(v.Faults, media.NewFault(media.KindBoundaryDrift, ix.Layer,
				fmt.Sprintf("line %d: offset %d aligned to boundary %d (%+d bytes)", e.Line, e.Offset, boundary, boundary-e.Offset),
				e.Offset, media.Informational))
		}
		seenOffset[e.Offset] = e.Line
		lastPTS, havePTS = e.PTS, true
		v.Aligned = append(v.Aligned, Aligned{Entry: e, Boundary: boundary, Drift: boundary - e.Offset})
	}
	return v
}

func nearest(boundaries []int64, off int64) (int64, int64, bool) {
	if len(boundaries) == 0 {
		return 0, 0, false
	}
	best := boundaries[0]
	bestD := abs(boundaries[0] - off)
	for _, b := range boundaries[1:] {
		if d := abs(b - off); d < bestD {
			best, bestD = b, d
		}
	}
	return best, bestD, true
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// Format renders an index in the archive format, deterministically.
func Format(ix Index) []byte {
	var b strings.Builder
	b.WriteString("archive-keyframe-index v1\n")
	if ix.Layer != "" {
		fmt.Fprintf(&b, "layer=%s\n", ix.Layer)
	}
	fmt.Fprintf(&b, "timescale=%d\n", ix.Timescale)
	for _, e := range ix.Entries {
		fmt.Fprintf(&b, "entry pts=%d offset=%d\n", e.PTS, e.Offset)
	}
	return []byte(b.String())
}
