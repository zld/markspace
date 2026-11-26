package demo

import (
	"bytes"

	"github.com/zld/medcheck/pkg/bitstream"
	"github.com/zld/medcheck/pkg/index"
	"github.com/zld/medcheck/pkg/media"
)

type analysis struct {
	fixture  *StreamFixture
	result   *bitstream.Result
	index    *index.Index
	parsed   *index.Index
	segments []index.Segment
	hashes   []string
}

func analyzeFixture(f *StreamFixture) (*analysis, error) {
	res, err := bitstream.Scan(bytes.NewReader(f.Data), f.Codec, f.TrackID)
	if err != nil {
		return nil, err
	}
	if len(res.Issues) != 0 {
		return nil, media.NewError(media.CatInvalidEncoding, f.TrackID, -1, -1, "clean fixture has scan issues: %+v", res.Issues)
	}
	idx, err := index.Build(f.TrackID, f.Codec, res, index.WithTimestamps(media.Clock90k, f.PTS90k))
	if err != nil {
		return nil, err
	}
	raw, err := idx.Marshal()
	if err != nil {
		return nil, err
	}
	parsed, err := index.Parse(raw)
	if err != nil {
		return nil, err
	}
	segs, err := parsed.Segments(int64(len(f.Data)))
	if err != nil {
		return nil, err
	}
	hashes := make([]string, 0, len(segs))
	for _, seg := range segs {
		hashes = append(hashes, index.HashSegmentBytes(f.Data, seg))
	}
	return &analysis{fixture: f, result: res, index: idx, parsed: parsed, segments: segs, hashes: hashes}, nil
}

func independentRepeat(seed int64) (*analysis, error) {
	f, err := GenerateStream(bitstream.CodecH264, seed)
	if err != nil {
		return nil, err
	}
	return analyzeFixture(f)
}

func indexHits(idx *index.Index) int {
	hits := 0
	for i, r := range idx.Records {
		probe := r.Offset + maxInt64(1, (r.End-r.Offset)/2)
		got, ok := idx.Floor(probe)
		if ok && got.Ordinal == i && got.Offset == r.Offset {
			hits++
		}
	}
	return hits
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
