// Package events defines the shared event, gap and error model used by the
// clock, RTP, metadata, timeline, render and manifest subsystems.
package events

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
)

// TrackID names an archive track. Track names are stable strings: they appear
// in manifests, diagnostics and state digests, so they must not be reordered
// casually.
type TrackID string

const (
	TrackVideo    TrackID = "video"
	TrackAudio    TrackID = "audio"
	TrackSubtitle TrackID = "subtitle"
	TrackChapter  TrackID = "chapter"
	TrackID3      TrackID = "id3"
	TrackEmsg     TrackID = "emsg"
	TrackSEI      TrackID = "sei"
	TrackRTP      TrackID = "rtp"
	TrackRTPSEI   TrackID = "rtp/sei"
	TrackRTPGap   TrackID = "rtp/gap"
)

// Kind is the semantic type of a timeline event.
type Kind string

const (
	KindSubtitle Kind = "subtitle"
	KindChapter  Kind = "chapter"
	KindID3      Kind = "id3"
	KindEmsg     Kind = "emsg"
	KindSEI      Kind = "sei"
	KindRTP      Kind = "rtp"
)

// Identity is the stable logical identity of an event. Re-delivery, replay and
// de-duplication all operate on Identity, never on arrival order.
type Identity struct {
	Track TrackID
	Kind  Kind
	Seq   uint64
}

// Event is one timed item on the synthetic multi-track timeline. At and
// Duration are integer nanoseconds. Source and ByteOffset are diagnostic only;
// they are deliberately excluded from the canonical state so that replaying a
// late event yields the same digest as processing the complete ordered input.
type Event struct {
	Track      TrackID
	Kind       Kind
	At         int64
	Duration   int64
	Seq        uint64
	Text       string
	Payload    []byte
	Source     string
	ByteOffset int64
}

// Identity returns the logical key used for de-duplication and replacement.
func (e Event) Identity() Identity {
	return Identity{Track: e.Track, Kind: e.Kind, Seq: e.Seq}
}

// Clone returns an event with its payload copied.
func (e Event) Clone() Event {
	if e.Payload != nil {
		e.Payload = bytes.Clone(e.Payload)
	}
	return e
}

// PayloadDigest returns the SHA-256 of the payload bytes.
func (e Event) PayloadDigest() [sha256.Size]byte {
	return sha256.Sum256(e.Payload)
}

// CompareEvents orders events canonically. It is a total order: every pair of
// logical events has a deterministic position, independent of arrival order.
func CompareEvents(a, b Event) int {
	if a.At != b.At {
		if a.At < b.At {
			return -1
		}
		return 1
	}
	if a.Track != b.Track {
		return compareString(string(a.Track), string(b.Track))
	}
	if a.Kind != b.Kind {
		return compareString(string(a.Kind), string(b.Kind))
	}
	if a.Seq != b.Seq {
		if a.Seq < b.Seq {
			return -1
		}
		return 1
	}
	if a.Text != b.Text {
		return compareString(a.Text, b.Text)
	}
	ah, bh := a.PayloadDigest(), b.PayloadDigest()
	return bytes.Compare(ah[:], bh[:])
}

func compareString(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// SortEvents sorts a copy of the events canonically. The input is not mutated.
func SortEvents(in []Event) []Event {
	out := append([]Event(nil), in...)
	sort.SliceStable(out, func(i, j int) bool { return CompareEvents(out[i], out[j]) < 0 })
	return out
}

// Gap is an explicit "data missing" marker. Gaps are never emitted as events:
// callers must not invent payloads for them. Filled is runtime bookkeeping and
// is not part of the canonical state digest.
type Gap struct {
	Track  TrackID
	Kind   Kind
	Seq    uint64
	At     int64
	Reason string
	Filled bool
}

// CompareGaps orders gap markers deterministically.
func CompareGaps(a, b Gap) int {
	if a.Track != b.Track {
		return compareString(string(a.Track), string(b.Track))
	}
	if a.Kind != b.Kind {
		return compareString(string(a.Kind), string(b.Kind))
	}
	if a.Seq != b.Seq {
		if a.Seq < b.Seq {
			return -1
		}
		return 1
	}
	if a.At != b.At {
		if a.At < b.At {
			return -1
		}
		return 1
	}
	return compareString(a.Reason, b.Reason)
}

// UnresolvedGaps filters out gaps that were filled by late data.
func UnresolvedGaps(in []Gap) []Gap {
	out := make([]Gap, 0, len(in))
	for _, g := range in {
		if !g.Filled {
			out = append(out, g)
		}
	}
	return out
}

// StateBytes serialises the final timeline state canonically: the watermark,
// every committed event and every unresolved gap. Diagnostic fields (payload
// storage aside from its digest, byte offsets, counters) are excluded so that
// states reached through different arrival orders compare equal.
func StateBytes(watermark int64, committed []Event, gaps []Gap) []byte {
	orderedEvents := SortEvents(committed)
	orderedGaps := append([]Gap(nil), UnresolvedGaps(gaps)...)
	sort.SliceStable(orderedGaps, func(i, j int) bool { return CompareGaps(orderedGaps[i], orderedGaps[j]) < 0 })

	var b bytes.Buffer
	b.WriteString("archive-state-v1\nwatermark=")
	b.WriteString(strconv.FormatInt(watermark, 10))
	b.WriteByte('\n')
	for _, e := range orderedEvents {
		AppendEventCanonical(&b, e)
	}
	for _, g := range orderedGaps {
		AppendGapCanonical(&b, g)
	}
	return b.Bytes()
}

// AppendEventCanonical appends one canonical event record to b.
func AppendEventCanonical(b *bytes.Buffer, e Event) {
	dh := e.PayloadDigest()
	b.WriteString("event|")
	b.WriteString(string(e.Track))
	b.WriteByte('|')
	b.WriteString(string(e.Kind))
	b.WriteByte('|')
	b.WriteString(strconv.FormatUint(e.Seq, 10))
	b.WriteByte('|')
	b.WriteString(strconv.FormatInt(e.At, 10))
	b.WriteByte('|')
	b.WriteString(strconv.FormatInt(e.Duration, 10))
	b.WriteByte('|')
	b.WriteString(strconv.Quote(e.Text))
	b.WriteByte('|')
	b.WriteString(hex.EncodeToString(dh[:]))
	b.WriteByte('\n')
}

// AppendGapCanonical appends one canonical unresolved-gap record to b.
func AppendGapCanonical(b *bytes.Buffer, g Gap) {
	b.WriteString("gap|")
	b.WriteString(string(g.Track))
	b.WriteByte('|')
	b.WriteString(string(g.Kind))
	b.WriteByte('|')
	b.WriteString(strconv.FormatUint(g.Seq, 10))
	b.WriteByte('|')
	b.WriteString(strconv.FormatInt(g.At, 10))
	b.WriteByte('|')
	b.WriteString(strconv.Quote(g.Reason))
	b.WriteByte('\n')
}

// Code classifies an archive error.
type Code string

const (
	CodeInvalidTimescale Code = "invalid_timescale"
	CodeTimeOutOfRange   Code = "time_out_of_range"
	CodeTruncated        Code = "truncated"
	CodeMalformed        Code = "malformed"
	CodeReplayWindow     Code = "replay_window_exceeded"
	CodeCapacity         Code = "capacity_exceeded"
	CodePath             Code = "path_rejected"
	CodeWrite            Code = "write_failed"
	CodeFixture          Code = "fixture_invalid"
	CodeInternal         Code = "internal"
)

// Error is the shared archive error. Every instance carries enough context to
// locate the failure: track, input sequence number and byte offset.
type Error struct {
	Code   Code
	Track  TrackID
	Seq    uint64
	Offset int64 // -1 when the source has no byte offset
	Op     string
	Detail string
	Err    error
}

// NewError constructs an archive error with a valid byte offset placeholder.
func NewError(code Code, track TrackID, seq uint64, offset int64, op, detail string, err error) *Error {
	return &Error{Code: code, Track: track, Seq: seq, Offset: offset, Op: op, Detail: detail, Err: err}
}

func (e *Error) Error() string {
	s := "code=" + string(e.Code)
	if e.Track != "" {
		s += " track=" + string(e.Track)
	}
	s += " seq=" + strconv.FormatUint(e.Seq, 10)
	if e.Offset >= 0 {
		s += " offset=" + strconv.FormatInt(e.Offset, 10)
	}
	if e.Op != "" {
		s += " op=" + e.Op
	}
	if e.Detail != "" {
		s += " detail=" + e.Detail
	}
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

// Unwrap exposes the underlying cause for errors.Is/As.
func (e *Error) Unwrap() error { return e.Err }

// Fatal reports whether the error makes any further deterministic output
// impossible. Truncation and replay-window overruns are recoverable: other
// tracks continue and missing data stays marked as a gap.
func (e *Error) Fatal() bool {
	switch e.Code {
	case CodeInvalidTimescale, CodeTimeOutOfRange, CodeCapacity, CodePath, CodeWrite, CodeFixture, CodeInternal:
		return true
	default:
		return false
	}
}

// AnyFatal reports whether any error in errs is fatal.
func AnyFatal(errs []*Error) bool {
	for _, e := range errs {
		if e != nil && e.Fatal() {
			return true
		}
	}
	return false
}

// FormatTrackSeq renders a compact location for terminal output.
func FormatTrackSeq(track TrackID, seq uint64, offset int64) string {
	return fmt.Sprintf("track=%s seq=%d offset=%d", track, seq, offset)
}
