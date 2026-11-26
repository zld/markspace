package index

import (
	"encoding/json"

	"github.com/zld/medcheck/pkg/media"
)

// OffsetEntry maps one keyframe between elementary-stream and container space.
type OffsetEntry struct {
	Ordinal         int   `json:"ordinal"`
	PTSUS           int64 `json:"pts_us"`
	StreamOffset    int64 `json:"stream_offset"`
	ContainerOffset int64 `json:"container_offset"`
	SampleSize      int64 `json:"sample_size"`
}

// OffsetMapping is invalidated by container changes even when the stream parse
// cache remains valid.
type OffsetMapping struct {
	Version         string        `json:"version"`
	TrackID         string        `json:"track_id"`
	StreamSHA256    string        `json:"stream_sha256"`
	ContainerSHA256 string        `json:"container_sha256"`
	Entries         []OffsetEntry `json:"entries"`
}

// BuildOffsetMapping applies a constant container header/payload delta to
// stream offsets. Real MP4 parsers can supply sample tables instead; this
// captures the mapping and its content identity.
func BuildOffsetMapping(idx *Index, streamDigest, containerDigest string, containerDelta int64) *OffsetMapping {
	m := &OffsetMapping{
		Version: "medcheck-offset-map/v1", TrackID: idx.TrackID,
		StreamSHA256: streamDigest, ContainerSHA256: containerDigest,
	}
	for _, r := range idx.Records {
		m.Entries = append(m.Entries, OffsetEntry{
			Ordinal: r.Ordinal, PTSUS: r.PTSUS, StreamOffset: r.Offset,
			ContainerOffset: r.Offset + containerDelta, SampleSize: r.End - r.Offset,
		})
	}
	return m
}

// Marshal encodes the mapping.
func (m *OffsetMapping) Marshal() ([]byte, error) {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, media.NewError(media.CatInvalidEncoding, m.TrackID, -1, -1, "encode offset map: %v", err)
	}
	return b, nil
}

// ParseOffsetMapping round-trips and validates identity fields.
func ParseOffsetMapping(b []byte) (*OffsetMapping, error) {
	var m OffsetMapping
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, media.NewError(media.CatInvalidEncoding, "", -1, -1, "parse offset map: %v", err)
	}
	if m.Version != "medcheck-offset-map/v1" || m.StreamSHA256 == "" {
		return nil, media.NewError(media.CatInvalidEncoding, m.TrackID, -1, -1, "invalid offset mapping envelope")
	}
	for i, e := range m.Entries {
		if e.Ordinal != i || e.StreamOffset < 0 || e.ContainerOffset < 0 {
			return nil, media.NewError(media.CatInvalidEncoding, m.TrackID, int64(i), e.StreamOffset, "invalid offset entry %d", i)
		}
	}
	return &m, nil
}
