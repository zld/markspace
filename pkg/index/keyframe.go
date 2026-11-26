// Package index builds keyframe indexes, maps stream offsets to container
// offsets, and defines deterministic keyframe-aligned segment boundaries.
package index

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"sort"

	"github.com/zld/medcheck/pkg/bitstream"
	"github.com/zld/medcheck/pkg/media"
)

// FormatVersion is the JSON index schema version.
const FormatVersion = "medcheck-index/v1"

// Record is one keyframe index entry.
type Record struct {
	Ordinal      int             `json:"ordinal"`
	UnitIndex    int             `json:"unit_index"`
	TrackID      string          `json:"track_id"`
	Codec        bitstream.Codec `json:"codec"`
	Keyframe     bool            `json:"keyframe"`
	Kind         string          `json:"kind"`
	Offset       int64           `json:"offset"`
	HeaderOffset int64           `json:"header_offset"`
	End          int64           `json:"end"`
	PTS          int64           `json:"pts"`
	TimeBase     media.Rational  `json:"time_base"`
	PTSUS        int64           `json:"pts_us"`
}

// Index is the round-trippable keyframe index.
type Index struct {
	Version  string          `json:"version"`
	TrackID  string          `json:"track_id"`
	Codec    bitstream.Codec `json:"codec"`
	TimeBase media.Rational  `json:"time_base"`
	Records  []Record        `json:"records"`
}

// BuildOption configures timestamp attachment.
type BuildOption func(*buildConfig)

type buildConfig struct {
	timebase media.Rational
	pts      []int64
}

// WithTimestamps supplies raw PTS values in tb for keyframes in stream order.
func WithTimestamps(tb media.Rational, pts []int64) BuildOption {
	return func(c *buildConfig) { c.timebase = tb; c.pts = pts }
}

// Build creates an index from complete keyframe units.
func Build(trackID string, codec bitstream.Codec, res *bitstream.Result, opts ...BuildOption) (*Index, error) {
	cfg := buildConfig{timebase: media.Microsecond}
	for _, op := range opts {
		op(&cfg)
	}
	if err := cfg.timebase.Validate(); err != nil {
		return nil, err
	}
	idx := &Index{Version: FormatVersion, TrackID: trackID, Codec: codec, TimeBase: cfg.timebase}
	kfs := res.Keyframes()
	for i, u := range kfs {
		rec := Record{
			Ordinal: i, UnitIndex: u.Index, TrackID: trackID, Codec: codec,
			Keyframe: true, Kind: u.Kind, Offset: u.Offset, HeaderOffset: u.HeaderOffset, End: u.End,
			TimeBase: cfg.timebase, PTSUS: 0,
		}
		if i < len(cfg.pts) {
			rec.PTS = cfg.pts[i]
			us, err := media.ToMicroseconds(rec.PTS, cfg.timebase)
			if err != nil {
				return nil, err
			}
			rec.PTSUS = us
		}
		idx.Records = append(idx.Records, rec)
	}
	return idx, nil
}

// Len returns record count.
func (idx *Index) Len() int { return len(idx.Records) }

// Floor returns the keyframe that contains offset: the latest keyframe whose
// start offset is less than or equal to offset.
func (idx *Index) Floor(offset int64) (Record, bool) {
	if idx == nil || len(idx.Records) == 0 {
		return Record{}, false
	}
	i := sort.Search(len(idx.Records), func(i int) bool { return idx.Records[i].Offset > offset }) - 1
	if i < 0 {
		return Record{}, false
	}
	return idx.Records[i], true
}

// AtOrdinal returns the nth keyframe.
func (idx *Index) AtOrdinal(n int) (Record, bool) {
	if n < 0 || n >= len(idx.Records) {
		return Record{}, false
	}
	return idx.Records[n], true
}

// Marshal encodes canonical, indented JSON.
func (idx *Index) Marshal() ([]byte, error) {
	b, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return nil, media.NewError(media.CatInvalidEncoding, idx.TrackID, -1, -1, "encode index: %v", err)
	}
	return b, nil
}

// Parse decodes and validates an index.
func Parse(b []byte) (*Index, error) {
	var idx Index
	if err := json.Unmarshal(b, &idx); err != nil {
		return nil, media.NewError(media.CatInvalidEncoding, "", -1, -1, "parse index: %v", err)
	}
	if idx.Version != FormatVersion {
		return nil, media.NewError(media.CatInvalidEncoding, idx.TrackID, -1, -1, "unsupported index version %q", idx.Version)
	}
	for i := range idx.Records {
		r := &idx.Records[i]
		if !r.Keyframe || r.Ordinal != i || r.Offset < 0 || r.End < r.Offset {
			return nil, media.NewError(media.CatInvalidEncoding, idx.TrackID, int64(i), r.Offset, "invalid index record %d", i)
		}
	}
	return &idx, nil
}

// Segment is a half-open byte interval beginning at a keyframe.
type Segment struct {
	Ordinal         int      `json:"ordinal"`
	Offset          int64    `json:"offset"`
	End             int64    `json:"end"`
	Length          int64    `json:"length"`
	Keyframe        Record   `json:"keyframe"`
	SHA256          string   `json:"sha256,omitempty"`
	Corrupt         bool     `json:"corrupt,omitempty"`
	IssueCategories []string `json:"issue_categories,omitempty"`
}

// Segments computes deterministic keyframe boundaries up to streamSize.
func (idx *Index) Segments(streamSize int64) ([]Segment, error) {
	if streamSize < 0 {
		return nil, media.NewError(media.CatInvalidArgument, idx.TrackID, -1, streamSize, "negative stream size")
	}
	out := make([]Segment, 0, len(idx.Records))
	for i, r := range idx.Records {
		end := streamSize
		if i+1 < len(idx.Records) {
			end = idx.Records[i+1].Offset
		}
		if end < r.Offset || end > streamSize {
			return nil, media.NewError(media.CatInvalidEncoding, idx.TrackID, int64(i), r.Offset, "non-monotonic segment boundary")
		}
		out = append(out, Segment{Ordinal: i, Offset: r.Offset, End: end, Length: end - r.Offset, Keyframe: r})
	}
	return out, nil
}

// HashSegment hashes exactly the segment byte range from r.
func HashSegment(r io.ReaderAt, seg Segment) (string, error) {
	h := sha256.New()
	sr := io.NewSectionReader(r, seg.Offset, seg.Length)
	if _, err := io.Copy(h, sr); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// HashSegmentBytes is the byte-slice convenience form.
func HashSegmentBytes(data []byte, seg Segment) string {
	s, _ := HashSegment(bytes.NewReader(data), seg)
	return s
}
