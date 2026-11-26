package media

import (
	"io"
	"sort"

	"github.com/abema/go-mp4"
)

// TrackMetadata is the codec-neutral, canonicalized track description.
type TrackMetadata struct {
	TrackID      uint32 `json:"track_id"`
	Codec        string `json:"codec"`
	Timescale    uint32 `json:"timescale"`
	Duration     uint64 `json:"duration"`
	Width        uint16 `json:"width,omitempty"`
	Height       uint16 `json:"height,omitempty"`
	ChannelCount uint16 `json:"channel_count,omitempty"`
}

// NormalizedMetadata is the stable representation whose digest changes only
// when semantically relevant track metadata changes.
type NormalizedMetadata struct {
	MovieTimescale uint32          `json:"movie_timescale"`
	MovieDuration  uint64          `json:"movie_duration"`
	Tracks         []TrackMetadata `json:"tracks"`
}

// Digest canonicalizes and hashes normalized metadata.
func (m *NormalizedMetadata) Digest() (string, error) {
	if m == nil {
		return "", NewError(CatInvalidArgument, "", -1, -1, "nil metadata")
	}
	cp := *m
	cp.Tracks = append([]TrackMetadata(nil), m.Tracks...)
	sort.Slice(cp.Tracks, func(i, j int) bool { return cp.Tracks[i].TrackID < cp.Tracks[j].TrackID })
	d, _, err := CanonicalDigest(&cp)
	return d, err
}

// ParseMP4Metadata reads MP4 box/track metadata with github.com/abema/go-mp4
// and returns its normalized, content-digestible form.
func ParseMP4Metadata(r io.ReadSeeker) (*NormalizedMetadata, string, error) {
	info, err := mp4.Probe(r)
	if err != nil {
		return nil, "", NewError(CatInvalidEncoding, "mp4", -1, 0, "probe MP4: %v", err)
	}
	out := &NormalizedMetadata{MovieTimescale: info.Timescale, MovieDuration: info.Duration}
	for _, t := range info.Tracks {
		if t == nil {
			continue
		}
		tm := TrackMetadata{
			TrackID:   t.TrackID,
			Timescale: t.Timescale,
			Duration:  t.Duration,
			Codec:     codecName(t.Codec),
		}
		if t.AVC != nil {
			tm.Codec = "h264"
			tm.Width, tm.Height = t.AVC.Width, t.AVC.Height
		}
		if t.MP4A != nil {
			tm.Codec = "aac"
			tm.ChannelCount = t.MP4A.ChannelCount
		}
		out.Tracks = append(out.Tracks, tm)
	}
	sort.Slice(out.Tracks, func(i, j int) bool { return out.Tracks[i].TrackID < out.Tracks[j].TrackID })
	d, err := out.Digest()
	if err != nil {
		return nil, "", err
	}
	return out, d, nil
}

func codecName(c mp4.Codec) string {
	switch c {
	case mp4.CodecAVC1:
		return "h264"
	case mp4.CodecMP4A:
		return "aac"
	default:
		return "unknown"
	}
}
