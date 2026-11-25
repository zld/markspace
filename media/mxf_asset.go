package media

import (
	"github.com/zld/media-regress/internal/error"
)

// This file assembles MXF tracks from the parsed header partition.

// parseMXF builds an Asset from MXF bytes.
func parseMXF(data []byte, source, digest string) (*Asset, error) {
	part, err := parseMXFPartition(data)
	if err != nil {
		return nil, err
	}
	asset := &Asset{Format: "mxf", Source: source, Digest: digest}
	byID := map[uint32]*Track{}
	var order []uint32
	for _, e := range part.descriptors {
		fields, err := parseLocalSet(e.Value, e.ValueOff)
		if err != nil {
			return nil, err
		}
		track, desc, err := mxfDescription(fields, e)
		if err != nil {
			return nil, err
		}
		existing, ok := byID[track.ID]
		if !ok {
			byID[track.ID] = track
			order = append(order, track.ID)
		} else if existing.Kind != track.Kind {
			return nil, merr.New(merr.StageMetadata, merr.KindInconsistent, track.ID, e.Offset,
				"mxf: track %d declared with conflicting kinds", track.ID)
		}
		byID[track.ID].Descriptions = append(byID[track.ID].Descriptions, *desc)
	}

	for _, id := range order {
		byID[id].Normalize()
	}

	blocks, err := mxfIndex(part.index)
	if err != nil {
		return nil, err
	}
	framesSeen := map[uint32]int64{}
	wantIndex := 1
	for _, b := range blocks {
		t := byID[b.TrackID]
		if t == nil || t.Kind != KindAudio {
			return nil, merr.New(merr.StageMetadata, merr.KindInconsistent, b.TrackID, part.index.ValueOff,
				"mxf: index references unknown audio track %d", b.TrackID)
		}
		if b.Index != wantIndex {
			return nil, merr.New(merr.StageMetadata, merr.KindInconsistent, b.TrackID, part.index.ValueOff,
				"mxf: index block numbering jumps from %d to %d", wantIndex-1, b.Index)
		}
		wantIndex++
		if b.Frames < 0 {
			return nil, merr.New(merr.StageMetadata, merr.KindInconsistent, b.TrackID, part.index.ValueOff,
				"mxf: block %d declares negative frame count", b.Index)
		}
		end := b.Offset + int64(b.Length)
		if b.Offset < 0 || end > int64(len(data)) {
			return nil, merr.New(merr.StageMetadata, merr.KindTruncated, b.TrackID, b.Offset,
				"mxf: block %d payload overruns input", b.Index)
		}
		wantBytes := b.Frames * int64(t.Audio.Channels) * 2
		if b.Frames > 0 && wantBytes != int64(b.Length) {
			return nil, merr.New(merr.StageMetadata, merr.KindInconsistent, b.TrackID, b.Offset,
				"mxf: block %d length %d does not match %d frames of %d-channel s16", b.Index, b.Length, b.Frames, t.Audio.Channels)
		}
		t.Samples = append(t.Samples, Sample{Index: b.Index, Offset: b.Offset, Data: data[b.Offset:end], CRC32: b.CRC32, HasCRC: true})
		framesSeen[b.TrackID] += b.Frames
	}
	if len(blocks) == 0 {
		return nil, merr.New(merr.StageMetadata, merr.KindInconsistent, 0, part.index.ValueOff,
			"mxf: audio index is empty")
	}

	for _, id := range order {
		t := byID[id]
		t.Source = source
		if err := t.Validate(); err != nil {
			return nil, err
		}
		if t.Kind == KindAudio && framesSeen[t.ID] != t.Duration {
			return nil, merr.New(merr.StageMetadata, merr.KindInconsistent, t.ID, part.index.ValueOff,
				"mxf: audio index covers %d frames but track duration is %d", framesSeen[t.ID], t.Duration)
		}
		asset.Tracks = append(asset.Tracks, t)
	}
	for _, t := range asset.Tracks {
		asset.Conflicts += t.Conflicts
	}
	return asset, nil
}
