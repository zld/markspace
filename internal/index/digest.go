// Package index builds the content, envelope and event digests plus the
// sample/keyframe index used to plan review clips. Sample bytes are read
// with hard per-box and total byte limits.
package index

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	"replaypipe/internal/metadata"
	"replaypipe/internal/timebase"
)

// Limits bounds parser resource use. Zero values pick safe defaults.
type Limits struct {
	MaxBoxBytes   uint64
	MaxTotalBytes uint64
}

// DefaultLimits is used when the caller does not override them.
func DefaultLimits() Limits {
	return Limits{MaxBoxBytes: 64 << 20, MaxTotalBytes: 512 << 20}
}

func (l Limits) box() uint64 {
	if l.MaxBoxBytes == 0 {
		return DefaultLimits().MaxBoxBytes
	}
	return l.MaxBoxBytes
}

func (l Limits) total() uint64 {
	if l.MaxTotalBytes == 0 {
		return DefaultLimits().MaxTotalBytes
	}
	return l.MaxTotalBytes
}

// Sample is one media sample with its location, decode timeline and payload
// digest. DTS and Duration are in the owning track's timescale.
type Sample struct {
	TrackID  uint32 `json:"track_id"`
	Index    int    `json:"index"`
	Offset   int64  `json:"offset"`
	Size     uint32 `json:"size"`
	DTS      int64  `json:"dts"`
	Duration int64  `json:"duration"`
	CTO      int64  `json:"cto"`
	Sync     bool   `json:"sync"`
	Digest   string `json:"digest"`
}

// DTSNS is the decode timestamp in nanoseconds.
func (s Sample) DTSNS(b timebase.Base) int64 {
	ns, err := b.ToNanos(s.DTS, s.TrackID, s.Offset)
	if err != nil {
		return 0
	}
	return ns
}

// DurationNS is the sample duration in nanoseconds.
func (s Sample) DurationNS(b timebase.Base) int64 {
	ns, err := b.ToNanos(s.Duration, s.TrackID, s.Offset)
	if err != nil {
		return 0
	}
	return ns
}

// TrackKind identifies the media handler of a track.
type TrackKind string

const (
	TrackVideo    TrackKind = "video"
	TrackAudio    TrackKind = "audio"
	TrackSubtitle TrackKind = "subtitle"
	TrackOther    TrackKind = "other"
)

// Track is a parsed media track with a complete sample table.
type Track struct {
	ID         uint32       `json:"id"`
	Timescale  uint32       `json:"timescale"`
	Handler    [4]byte      `json:"-"`
	Kind       TrackKind    `json:"kind"`
	Codec      [4]byte      `json:"-"`
	Width      uint16       `json:"width"`
	Height     uint16       `json:"height"`
	SampleRate uint32       `json:"sample_rate,omitempty"`
	Duration   int64        `json:"duration_ticks"`
	Samples    []Sample     `json:"samples"`
	Base       timebase.Base `json:"-"`
}

// StringCodec renders the sample-entry fourcc.
func (t *Track) StringCodec() string { return string(t.Codec[:]) }

// StringHandler renders the mdia handler fourcc.
func (t *Track) StringHandler() string { return string(t.Handler[:]) }

// FrameRate returns duration-weighted average frames per second.
func (t *Track) FrameRate() float64 {
	if t.Timescale == 0 || t.Duration <= 0 || len(t.Samples) == 0 {
		return 0
	}
	return float64(len(t.Samples)) * float64(t.Timescale) / float64(t.Duration)
}

// DurationNS is the track duration in nanoseconds.
func (t *Track) DurationNS() int64 { return int64(float64(t.Duration) * 1e9 / float64(t.Timescale)) }

// BoxRecord is one visited box in file order, used for the envelope digest.
type BoxRecord struct {
	Depth      int    `json:"depth"`
	Path       string `json:"path"`
	Offset     int64  `json:"offset"`
	Size       uint64 `json:"size"`
	HeaderSize uint64 `json:"header_size"`
	PayloadSum string `json:"payload_sum,omitempty"`
}

// Media is a fully parsed MP4/CMAF presentation.
type Media struct {
	Path           string        `json:"path"`
	FileSize       int64         `json:"file_size"`
	Fragmented     bool          `json:"fragmented"`
	Tracks         []*Track      `json:"tracks"`
	Boxes          []BoxRecord   `json:"boxes"`
	ContentDigest  string        `json:"content_digest"`
	EnvelopeDigest string        `json:"envelope_digest"`
	EventDigest    string        `json:"event_digest,omitempty"`
	Events         []metadata.Event `json:"events,omitempty"`
	Warnings       []string      `json:"warnings,omitempty"`
}

// VideoTrack returns the first video track, if any.
func (m *Media) VideoTrack() *Track {
	for _, t := range m.Tracks {
		if t.Kind == TrackVideo {
			return t
		}
	}
	return nil
}

// AudioTrack returns the first audio track, if any.
func (m *Media) AudioTrack() *Track {
	for _, t := range m.Tracks {
		if t.Kind == TrackAudio {
			return t
		}
	}
	return nil
}

// DurationNS is the longest track duration.
func (m *Media) DurationNS() int64 {
	var max int64
	for _, t := range m.Tracks {
		if d := t.DurationNS(); d > max {
			max = d
		}
	}
	return max
}

// SetEvents attaches parsed events and computes the event digest.
func (m *Media) SetEvents(events []metadata.Event) {
	m.Events = events
	m.EventDigest = EventDigest(events)
}

// EventDigest hashes canonical event payloads in timeline order. Paths and
// source offsets never participate, so re-muxing cannot change event identity.
func EventDigest(events []metadata.Event) string {
	sorted := append([]metadata.Event(nil), events...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].StartNS != sorted[j].StartNS {
			return sorted[i].StartNS < sorted[j].StartNS
		}
		return sorted[i].ID < sorted[j].ID
	})
	h := sha256.New()
	for _, e := range sorted {
		h.Write(e.Canonical())
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// digestSamples computes the content digest: ordered track/sample identity,
// decode timeline, keyframe flags and payload bytes. Container offsets are
// intentionally excluded.
func digestSamples(tracks []*Track) string {
	h := sha256.New()
	for _, t := range tracks {
		fmt.Fprintf(h, "track:%d:%s:%d:%d:%d:%d\x00", t.ID, t.Kind, t.Timescale, t.Width, t.Height, len(t.Samples))
		for _, s := range t.Samples {
			fmt.Fprintf(h, "%d:%d:%d:%d:%t:%s\x00", s.Index, s.DTS, s.Duration, s.CTO, s.Sync, s.Digest)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// digestEnvelope hashes the box tree layout. Payload digests of non-media
// boxes are included so re-muxing or metadata edits are visible even when
// sample bytes are untouched.
func digestEnvelope(boxes []BoxRecord) string {
	h := sha256.New()
	for _, b := range boxes {
		fmt.Fprintf(h, "%d:%s:%d:%d:%d:%s\x00", b.Depth, b.Path, b.Offset, b.Size, b.HeaderSize, b.PayloadSum)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// VerifyRoundTrip re-parses a written file and compares the content digest.
// It returns the parsed media for callers that need the full tree.
func VerifyRoundTrip(path string, limits Limits) (*Media, bool, error) {
	m, err := ParseFile(path, limits)
	if err != nil {
		return nil, false, err
	}
	return m, true, nil
}

// trex values are stored on the parser, declared here to keep the model file
// free of fragment bookkeeping.
