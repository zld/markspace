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

	"github.com/pion/rtp"

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

// Validation is the deterministic outcome of index checking.
type Validation struct {
	Index   Index
	Aligned []Aligned
	Faults  []media.Fault
	Dropped int
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
			v.Faults = append(v.Faults, media.NewFault(media.KindIndexOutOfRange, ix.Layer,
				fmt.Sprintf("line %d: offset %d outside stream length %d", e.Line, e.Offset, streamLen),
				e.Offset, media.Unrecoverable))
			continue
		}
		if prevLine, dup := seenOffset[e.Offset]; dup {
			v.Dropped++
			v.Faults = append(v.Faults, media.NewFault(media.KindIndexDuplicate, ix.Layer,
				fmt.Sprintf("line %d: offset %d already used on line %d", e.Line, e.Offset, prevLine),
				e.Offset, media.Recoverable))
			continue
		}
		if havePTS && e.PTS <= lastPTS {
			v.Dropped++
			v.Faults = append(v.Faults, media.NewFault(media.KindIndexRegress, ix.Layer,
				fmt.Sprintf("line %d: pts %d does not advance past %d", e.Line, e.PTS, lastPTS),
				e.Offset, media.Recoverable).WithSpan(media.NewInterval(e.PTS, lastPTS, media.MustClock(ix.Timescale))))
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

// EventsToRTP carries one index event per RTP packet. Each packet is marked
// and self-delimiting so packet boundaries can be verified independently.
func EventsToRTP(ix Index, ssrc uint32, startSeq uint16) ([]*rtp.Packet, error) {
	if ix.Timescale == 0 {
		return nil, errors.New("index: timescale required for RTP carriage")
	}
	pkts := make([]*rtp.Packet, 0, len(ix.Entries))
	for i, e := range ix.Entries {
		payload := []byte(fmt.Sprintf("%s|%d|%d|%d", ix.Layer, ix.Timescale, e.PTS, e.Offset))
		pkts = append(pkts, &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    97,
				Marker:         true,
				SequenceNumber: startSeq + uint16(i),
				Timestamp:      uint32(e.PTS),
				SSRC:           ssrc,
			},
			Payload: payload,
		})
	}
	return pkts, nil
}

// EventsFromRTP validates packet boundaries and rebuilds the index. A payload
// that does not contain exactly one complete event, a missing marker bit, or a
// sequence gap is rejected rather than silently accepted.
func EventsFromRTP(pkts []*rtp.Packet) (Index, error) {
	var ix Index
	if len(pkts) == 0 {
		return ix, errors.New("index: no RTP packets")
	}
	for i, p := range pkts {
		if p == nil {
			return ix, fmt.Errorf("index: nil packet at %d", i)
		}
		if !p.Marker {
			return ix, fmt.Errorf("index: packet %d lacks the event marker bit", i)
		}
		if i > 0 && p.SequenceNumber != pkts[i-1].SequenceNumber+1 {
			return ix, fmt.Errorf("index: sequence gap at packet %d: %d -> %d", i, pkts[i-1].SequenceNumber, p.SequenceNumber)
		}
		parts := strings.Split(string(p.Payload), "|")
		if len(parts) != 4 {
			return ix, fmt.Errorf("index: packet %d payload has %d fields, want 4", i, len(parts))
		}
		ts, err := strconv.ParseUint(parts[1], 10, 32)
		if err != nil || ts == 0 {
			return ix, fmt.Errorf("index: packet %d invalid timescale", i)
		}
		pts, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			return ix, fmt.Errorf("index: packet %d invalid pts", i)
		}
		off, err := strconv.ParseInt(parts[3], 10, 64)
		if err != nil {
			return ix, fmt.Errorf("index: packet %d invalid offset", i)
		}
		if i == 0 {
			ix.Layer, ix.Timescale = parts[0], uint32(ts)
		} else if ix.Layer != parts[0] || ix.Timescale != uint32(ts) {
			return ix, fmt.Errorf("index: packet %d disagrees on layer/timescale", i)
		}
		ix.Entries = append(ix.Entries, Entry{PTS: pts, Offset: off})
	}
	return ix, nil
}
