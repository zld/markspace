// Package index loads external keyframe indexes, checks them item by item
// against a bitstream scan, and rebuilds a trustworthy index from the scan.
package index

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"mediadist/internal/codec"
	"mediadist/internal/merr"
)

// Entry is one external keyframe index row.
//
// Offsets are interpreted per sample: Sample identifies the sample ordinal
// within the stream (0-based, in decode order) and Offset/Size address bytes
// inside that sample's payload. Per-sample addressing keeps the index stable
// when transport loss shifts global byte positions, which is exactly the case
// the pipeline must detect rather than silently accept.
type Entry struct {
	Stream int   `json:"stream"`
	Sample int   `json:"sample"`
	Offset int64 `json:"offset"`
	Size   int64 `json:"size"`
	// Timestamp is the sample's RTP timestamp and is the stable identity used
	// for verification. Sample is informational: it shifts when an earlier
	// access unit is lost entirely, while the timestamp does not.
	Timestamp uint32 `json:"timestamp"`
	Keyframe  bool   `json:"keyframe"`
	// Sync marks a row the producer claims is an independent decodable start.
	Sync bool `json:"sync,omitempty"`
}

// Index is an ordered set of entries plus the format that produced it.
type Index struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}

// Limits bounds index loading.
type Limits struct {
	MaxEntries  int
	MaxBytes    int64
	MaxUnitSize int64
}

// DefaultLimits caps indexes at 100k entries and 8 MiB of JSON.
func DefaultLimits() Limits {
	return Limits{MaxEntries: 100_000, MaxBytes: 8 << 20, MaxUnitSize: 16 << 20}
}

func (l Limits) validated() Limits {
	if l.MaxEntries <= 0 {
		l.MaxEntries = DefaultLimits().MaxEntries
	}
	if l.MaxBytes <= 0 {
		l.MaxBytes = DefaultLimits().MaxBytes
	}
	if l.MaxUnitSize <= 0 {
		l.MaxUnitSize = DefaultLimits().MaxUnitSize
	}
	return l
}

// ErrIndexTooLarge is returned when the JSON body exceeds the byte limit.
var ErrIndexTooLarge = errors.New("index: body exceeds size limit")

// Load reads a JSON keyframe index from r, enforcing byte and entry limits.
// The decoder rejects unknown fields so a schema drift is visible instead of
// silently ignored.
func Load(r io.Reader, lim Limits) (*Index, error) {
	lim = lim.validated()
	body, err := io.ReadAll(io.LimitReader(r, lim.MaxBytes+1))
	if err != nil {
		return nil, merr.Wrap(merr.StageIndex, "read_failed", merr.NoSpan(0), false, err, "reading index")
	}
	if int64(len(body)) > lim.MaxBytes {
		return nil, merr.New(merr.StageIndex, "too_large", merr.NoSpan(0), false,
			"index body exceeds %d bytes", lim.MaxBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var idx Index
	if err := dec.Decode(&idx); err != nil {
		return nil, merr.Wrap(merr.StageIndex, "malformed", merr.NoSpan(0), false, err, "decoding index")
	}
	// Reject trailing data so a truncated or concatenated document cannot be
	// silently accepted.
	if _, err := dec.Token(); err != io.EOF {
		return nil, merr.New(merr.StageIndex, "trailing_data", merr.NoSpan(0), false,
			"index has trailing data after the JSON object")
	}
	if len(idx.Entries) > lim.MaxEntries {
		return nil, merr.New(merr.StageIndex, "entry_limit", merr.NoSpan(0), false,
			"%d entries exceed limit %d", len(idx.Entries), lim.MaxEntries)
	}
	for i, e := range idx.Entries {
		if e.Sample < 0 {
			return nil, merr.New(merr.StageIndex, "negative_field",
				merr.Span{Stream: e.Stream, Seq: -1, Offset: e.Offset, Length: e.Size}, false,
				"entry %d has negative sample ordinal", i)
		}
		if e.Offset < 0 || e.Size < 0 {
			return nil, merr.New(merr.StageIndex, "negative_field",
				merr.Span{Stream: e.Stream, Seq: -1, Offset: e.Offset, Length: e.Size}, false,
				"entry %d has negative offset or size", i)
		}
		if e.Size > lim.MaxUnitSize {
			return nil, merr.New(merr.StageIndex, "entry_too_large",
				merr.Span{Stream: e.Stream, Seq: -1, Offset: e.Offset, Length: e.Size}, false,
				"entry %d size %d exceeds limit %d", i, e.Size, lim.MaxUnitSize)
		}
	}
	return &idx, nil
}

// MismatchKind classifies one failed comparison.
type MismatchKind string

const (
	// MismatchOffset points past the end of the scanned stream.
	MismatchOffset MismatchKind = "offset_out_of_range"
	// MismatchNotKey means the index called a unit a keyframe but the scan
	// found no keyframe covering that offset.
	MismatchNotKey MismatchKind = "marked_keyframe_but_not"
	// MismatchMissingKey means the scan found a keyframe the index omitted.
	MismatchMissingKey MismatchKind = "keyframe_missing_from_index"
	// MismatchInvalidAddress means offset+size is inconsistent with any unit.
	MismatchInvalidAddress MismatchKind = "invalid_address"
)

// Mismatch is one failed index-versus-scan comparison.
type Mismatch struct {
	Kind   MismatchKind `json:"kind"`
	Entry  Entry        `json:"entry"`
	Detail string       `json:"detail"`
	Span   merr.Span    `json:"span"`
}

// Verification is the outcome of checking an index against scans.
type Verification struct {
	Total       int        `json:"total"`
	Matched     int        `json:"matched"`
	Mismatches  []Mismatch `json:"mismatches"`
	// Rebuilt holds a scan-derived index that replaces the external one when
	// any mismatch occurred.
	Rebuilt *Index `json:"rebuilt,omitempty"`
	// RebuiltReason explains why rebuilding happened.
	RebuiltReason string `json:"rebuilt_reason,omitempty"`
}

// HitRate is matched/total in [0,1]; 1 when total is zero.
func (v Verification) HitRate() float64 {
	if v.Total == 0 {
		return 1
	}
	return float64(v.Matched) / float64(v.Total)
}

// SampleScan is one demuxed sample's scan result, payload length and stable
// timestamp identity.
type SampleScan struct {
	Stream    int
	Sample    int
	Timestamp uint32
	Length    int64
	Result    codec.ScanResult
}

// Verify compares every index entry against the per-sample scans. Rows are
// matched by (stream, timestamp), the stable sample identity, so an entirely
// lost access unit shifts ordinals without invalidating every later row.
func Verify(idx *Index, scans []SampleScan) Verification {
	v := Verification{Total: len(idx.Entries)}
	type key struct {
		stream int
		ts     uint32
	}
	byKey := make(map[key]SampleScan, len(scans))
	for _, s := range scans {
		byKey[key{s.Stream, s.Timestamp}] = s
	}
	type keyAddr struct {
		k      key
		offset int64
	}
	indexedKeys := make(map[keyAddr]struct{})
	for _, e := range idx.Entries {
		if e.Keyframe || e.Sync {
			indexedKeys[keyAddr{key{e.Stream, e.Timestamp}, e.Offset}] = struct{}{}
		}
	}

	for _, e := range idx.Entries {
		scan, ok := byKey[key{e.Stream, e.Timestamp}]
		if !ok {
			v.Mismatches = append(v.Mismatches, Mismatch{
				Kind: MismatchInvalidAddress, Entry: e,
				Detail: fmt.Sprintf("sample with timestamp %d of stream %d is absent",
					e.Timestamp, e.Stream),
				Span: merr.OffsetSpan(e.Stream, e.Offset, e.Size),
			})
			continue
		}
		if e.Offset >= scan.Length {
			v.Mismatches = append(v.Mismatches, Mismatch{
				Kind: MismatchOffset, Entry: e,
				Detail: fmt.Sprintf("offset %d past sample length %d", e.Offset, scan.Length),
				Span:   merr.OffsetSpan(e.Stream, e.Offset, e.Size),
			})
			continue
		}
		if e.Offset+e.Size > scan.Length {
			v.Mismatches = append(v.Mismatches, Mismatch{
				Kind: MismatchInvalidAddress, Entry: e,
				Detail: fmt.Sprintf("range %d..%d exceeds sample length %d", e.Offset, e.Offset+e.Size, scan.Length),
				Span:   merr.OffsetSpan(e.Stream, e.Offset, e.Size),
			})
			continue
		}
		unit, found := unitAt(scan.Result.Units, e.Offset)
		if !found {
			v.Mismatches = append(v.Mismatches, Mismatch{
				Kind: MismatchInvalidAddress, Entry: e,
				Detail: fmt.Sprintf("offset %d is not inside any scanned unit", e.Offset),
				Span:   merr.OffsetSpan(e.Stream, e.Offset, e.Size),
			})
			continue
		}
		claimsKey := e.Keyframe || e.Sync
		if claimsKey != unit.Keyframe {
			kind := MismatchNotKey
			detail := fmt.Sprintf("index keyframe=%t but scan unit type %d keyframe=%t",
				claimsKey, unit.Type, unit.Keyframe)
			if !claimsKey && unit.Keyframe {
				kind = MismatchMissingKey
			}
			v.Mismatches = append(v.Mismatches, Mismatch{
				Kind: kind, Entry: e, Detail: detail,
				Span: merr.OffsetSpan(e.Stream, e.Offset, unit.HeaderLen+unit.Size),
			})
			continue
		}
		v.Matched++
	}

	// Recall is checked only for streams the index claims to describe. A row
	// whose sample is genuinely absent (lost media) is already a mismatch, so
	// recall does not double-report it.
	indexedStreams := make(map[int]struct{}, len(byKey))
	for _, e := range idx.Entries {
		indexedStreams[e.Stream] = struct{}{}
	}
	for _, s := range scans {
		if _, scoped := indexedStreams[s.Stream]; !scoped {
			continue
		}
		for _, ui := range s.Result.Keyframes {
			u := s.Result.Units[ui]
			if _, ok := indexedKeys[keyAddr{key{s.Stream, s.Timestamp}, u.Offset}]; ok {
				continue
			}
			v.Mismatches = append(v.Mismatches, Mismatch{
				Kind: MismatchMissingKey,
				Entry: Entry{Stream: s.Stream, Sample: s.Sample, Timestamp: s.Timestamp,
					Offset: u.Offset, Size: u.HeaderLen + u.Size, Keyframe: true},
				Detail: fmt.Sprintf("sample at timestamp %d has a scanned keyframe at offset %d absent from index",
					s.Timestamp, u.Offset),
				Span: u.Span(),
			})
		}
	}
	sort.SliceStable(v.Mismatches, func(i, j int) bool {
		a, b := v.Mismatches[i], v.Mismatches[j]
		if a.Entry.Stream != b.Entry.Stream {
			return a.Entry.Stream < b.Entry.Stream
		}
		if a.Entry.Timestamp != b.Entry.Timestamp {
			return a.Entry.Timestamp < b.Entry.Timestamp
		}
		return a.Entry.Offset < b.Entry.Offset
	})
	return v
}

// unitAt finds the scanned unit whose byte range contains offset.
func unitAt(units []codec.Unit, offset int64) (codec.Unit, bool) {
	i := sort.Search(len(units), func(i int) bool { return units[i].End() > offset })
	if i < len(units) && units[i].Offset <= offset && offset < units[i].End() {
		return units[i], true
	}
	return codec.Unit{}, false
}

// Rebuild derives an index from per-sample scan results. Only keyframe units
// become rows, ordered by stream, timestamp and offset, which makes the
// rebuilt index deterministic and independent of sample ordinals.
func Rebuild(scans []SampleScan) *Index {
	idx := &Index{Version: 1}
	for _, s := range scans {
		for _, ui := range s.Result.Keyframes {
			u := s.Result.Units[ui]
			idx.Entries = append(idx.Entries, Entry{
				Stream:   s.Stream,
				Sample:   s.Sample,
				Timestamp: s.Timestamp,
				Offset:   u.Offset,
				Size:     u.HeaderLen + u.Size,
				Keyframe: true,
				Sync:     true,
			})
		}
	}
	sort.SliceStable(idx.Entries, func(i, j int) bool {
		a, b := idx.Entries[i], idx.Entries[j]
		if a.Stream != b.Stream {
			return a.Stream < b.Stream
		}
		if a.Timestamp != b.Timestamp {
			return a.Timestamp < b.Timestamp
		}
		return a.Offset < b.Offset
	})
	return idx
}

// CheckAndRebuild verifies idx and, when any mismatch exists, returns a rebuilt
// index with a reason. The original index is left untouched.
func CheckAndRebuild(idx *Index, scans []SampleScan) (Verification, *Index) {
	v := Verify(idx, scans)
	if len(v.Mismatches) == 0 {
		return v, idx
	}
	v.Rebuilt = Rebuild(scans)
	v.RebuiltReason = fmt.Sprintf("%d mismatch(es) against %d scanned units", len(v.Mismatches), countUnits(scans))
	return v, v.Rebuilt
}

func countUnits(scans []SampleScan) int {
	n := 0
	for _, s := range scans {
		n += len(s.Result.Units)
	}
	return n
}

// Errors converts mismatches into recoverable structured errors so they flow
// through the same reporting channel as every other stage.
func (v Verification) Errors() []*merr.Error {
	out := make([]*merr.Error, 0, len(v.Mismatches))
	for _, m := range v.Mismatches {
		out = append(out, merr.New(merr.StageIndex, string(m.Kind), m.Span, true,
			"%s", m.Detail))
	}
	return out
}
