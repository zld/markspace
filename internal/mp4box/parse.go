package mp4box

import (
	"bytes"
	"fmt"

	"github.com/Eyevinn/mp4ff/mp4"
)

// ParsedFile is the decoded sample-table view used to verify round-trips.
type ParsedFile struct {
	Table SampleTable
	Color *Color
}

// ParseFile decodes an MP4 byte stream and extracts the first video track's
// sample table plus any nclx color box. It is the read counterpart of
// BuildFile and performs no sample decoding.
func ParseFile(raw []byte) (ParsedFile, error) {
	f, err := mp4.DecodeFile(bytes.NewReader(raw))
	if err != nil {
		return ParsedFile{}, fmt.Errorf("mp4box parse: %w", err)
	}
	if f.Moov == nil || len(f.Moov.Traks) == 0 {
		return ParsedFile{}, fmt.Errorf("mp4box: no tracks in moov")
	}
	trak := f.Moov.Traks[0]
	stbl := trak.Mdia.Minf.Stbl
	out := ParsedFile{Table: SampleTable{
		TrackID:      trak.Tkhd.TrackID,
		Timescale:    trak.Mdia.Mdhd.Timescale,
		SampleSizes:  append([]uint32(nil), stbl.Stsz.SampleSize...),
		ChunkOffsets: append([]uint32(nil), stbl.Stco.ChunkOffset...),
	}}
	// stts is run-length encoded; expand to one delta per sample so the
	// normalized view is directly comparable to the build input.
	for i, delta := range stbl.Stts.SampleTimeDelta {
		count := stbl.Stts.SampleCount[i]
		for j := uint32(0); j < count; j++ {
			out.Table.SampleDeltas = append(out.Table.SampleDeltas, delta)
		}
	}
	if len(stbl.Stsc.Entries) > 0 {
		out.Table.SamplesPerChunk = stbl.Stsc.Entries[0].SamplesPerChunk
	}
	if f.Mdat != nil {
		out.Table.Payload = append([]byte(nil), f.Mdat.Data...)
	}
	if stbl.Stsd != nil && stbl.Stsd.AvcX != nil {
		for _, child := range stbl.Stsd.AvcX.Children {
			if colr, ok := child.(*mp4.ColrBox); ok && colr.ColorType == "nclx" {
				out.Color = &Color{
					Primaries: colr.ColorPrimaries,
					Transfer:  colr.TransferCharacteristics,
					Matrix:    colr.MatrixCoefficients,
					FullRange: colr.FullRangeFlag,
				}
				break
			}
		}
	}
	return out, nil
}

// VerifyRoundTrip builds, parses, and asserts the sample table survives
// serialization unchanged, returning the canonical hash of the bytes.
func VerifyRoundTrip(t SampleTable, col *Color) (parsed ParsedFile, raw []byte, err error) {
	raw, err = BuildFile(t, col)
	if err != nil {
		return ParsedFile{}, nil, err
	}
	parsed, err = ParseFile(raw)
	if err != nil {
		return ParsedFile{}, nil, err
	}
	if !tablesEqual(t, parsed.Table) {
		return parsed, raw, fmt.Errorf("mp4box: sample table changed across round-trip")
	}
	if col != nil && (parsed.Color == nil || *parsed.Color != *col) {
		return parsed, raw, fmt.Errorf("mp4box: colr box changed across round-trip: %+v vs %+v", parsed.Color, col)
	}
	return parsed, raw, nil
}

func tablesEqual(a, b SampleTable) bool {
	if a.TrackID != b.TrackID || a.Timescale != b.Timescale ||
		a.SamplesPerChunk != b.SamplesPerChunk || len(a.SampleSizes) != len(b.SampleSizes) ||
		len(a.SampleDeltas) != len(b.SampleDeltas) || len(a.ChunkOffsets) != len(b.ChunkOffsets) ||
		len(a.Payload) != len(b.Payload) {
		return false
	}
	for i := range a.SampleSizes {
		if a.SampleSizes[i] != b.SampleSizes[i] {
			return false
		}
	}
	for i := range a.SampleDeltas {
		if a.SampleDeltas[i] != b.SampleDeltas[i] {
			return false
		}
	}
	for i := range a.ChunkOffsets {
		if a.ChunkOffsets[i] != b.ChunkOffsets[i] {
			return false
		}
	}
	for i := range a.Payload {
		if a.Payload[i] != b.Payload[i] {
			return false
		}
	}
	return true
}
