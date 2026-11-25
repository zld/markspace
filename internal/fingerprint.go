package kernel

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// SHA256Hex returns the lowercase hex SHA-256 digest of data.
func SHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// SourceDigest hashes canonical generated source bytes.
func SourceDigest(canonical []byte) string {
	return SHA256Hex(canonical)
}

type canonicalSample struct {
	Index       uint64 `json:"index"`
	PTS90k      uint64 `json:"pts_90k"`
	Duration90k uint64 `json:"duration_90k"`
	Flags       uint16 `json:"flags"`
	DataSHA256  string `json:"data_sha256"`
}

type canonicalTrack struct {
	TrackID string            `json:"track_id"`
	PID     uint16            `json:"pid"`
	Kind    TrackKind         `json:"kind"`
	Samples []canonicalSample `json:"samples"`
}

type canonicalSegment struct {
	Index    int              `json:"index"`
	ID       string           `json:"id"`
	Start90k uint64           `json:"start_90k"`
	End90k   uint64           `json:"end_90k"`
	EventIDs []string         `json:"event_ids"`
	GapIDs   []string         `json:"gap_ids"`
	Tracks   []canonicalTrack `json:"tracks"`
}

// SegmentFingerprint returns the stable timeline-and-content fingerprint.
// Delivery metadata and the fingerprint field itself are excluded.
func SegmentFingerprint(seg *Segment) (string, error) {
	if seg == nil {
		return "", &MediaError{Stage: "fingerprint", Code: "nil_segment", Message: "segment is required", Recoverable: false}
	}
	c := canonicalSegment{
		Index: seg.Index, ID: seg.ID,
		Start90k: seg.Time.Start90k, End90k: seg.Time.End90k,
		EventIDs: append([]string(nil), seg.EventIDs...),
		GapIDs:   append([]string(nil), seg.GapIDs...),
	}
	for _, tr := range seg.Tracks {
		ct := canonicalTrack{TrackID: tr.TrackID, PID: tr.PID, Kind: tr.Kind}
		for _, s := range tr.Samples {
			ct.Samples = append(ct.Samples, canonicalSample{
				Index: s.Index, PTS90k: s.PTS90k, Duration90k: s.Duration90k,
				Flags: s.Flags, DataSHA256: SHA256Hex(s.Data),
			})
		}
		c.Tracks = append(c.Tracks, ct)
	}
	b, err := json.Marshal(c)
	if err != nil {
		return "", &MediaError{Stage: "fingerprint", Code: "canonical_encode", Err: err, Recoverable: false}
	}
	return SHA256Hex(b), nil
}

// DuplicateGroup reports equal fingerprints at their shared-clock ranges.
type DuplicateGroup struct {
	Fingerprint string      `json:"fingerprint"`
	Indexes     []int       `json:"indexes"`
	Ranges      []TimeRange `json:"ranges"`
}

// FindDuplicateSegments groups segments with identical canonical fingerprints.
func FindDuplicateSegments(segments []*Segment) ([]DuplicateGroup, error) {
	groups := map[string]*DuplicateGroup{}
	var order []string
	for _, seg := range segments {
		fp, err := SegmentFingerprint(seg)
		if err != nil {
			return nil, err
		}
		seg.ContentHash = fp
		g := groups[fp]
		if g == nil {
			g = &DuplicateGroup{Fingerprint: fp}
			groups[fp] = g
			order = append(order, fp)
		}
		g.Indexes = append(g.Indexes, seg.Index)
		g.Ranges = append(g.Ranges, seg.Time)
	}
	sort.Strings(order)
	var out []DuplicateGroup
	for _, fp := range order {
		g := groups[fp]
		if len(g.Indexes) > 1 {
			out = append(out, *g)
		}
	}
	return out, nil
}
