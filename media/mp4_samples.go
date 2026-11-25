package media

import (
	"encoding/binary"
	"fmt"

	"github.com/abema/go-mp4"

	"github.com/zld/media-regress/internal/error"
	"github.com/zld/media-regress/internal/mp4ext"
)

// parseMP4AudioSamples expands stts/stsc/stsz/stco into per-block payloads and
// validates the sample timing table against the track duration.
func parseMP4AudioSamples(data []byte, track *Track, stbl *boxNode) error {
	sttsNode := stbl.find(mp4.BoxTypeStts())
	stscNode := stbl.find(mp4.BoxTypeStsc())
	stszNode := stbl.find(mp4.BoxTypeStsz())
	stcoNode := stbl.find(mp4.BoxTypeStco())
	for _, n := range []*boxNode{sttsNode, stscNode, stszNode, stcoNode} {
		if n == nil {
			return merr.New(merr.StageMetadata, merr.KindParse, track.ID, stbl.offset,
				"mp4: audio stbl is missing a timing/size/offset table")
		}
	}
	if err := checkSTTSPayloadSize(data, sttsNode, track.ID); err != nil {
		return err
	}
	sttsBox, err := decodeNode(data, sttsNode, mp4.Context{})
	if err != nil {
		return err
	}
	stscBox, err := decodeNode(data, stscNode, mp4.Context{})
	if err != nil {
		return err
	}
	stszBox, err := decodeNode(data, stszNode, mp4.Context{})
	if err != nil {
		return err
	}
	stcoBox, err := decodeNode(data, stcoNode, mp4.Context{})
	if err != nil {
		return err
	}
	stts := sttsBox.(*mp4.Stts)
	stsc := stscBox.(*mp4.Stsc)
	stsz := stszBox.(*mp4.Stsz)
	stco := stcoBox.(*mp4.Stco)

	sizes, err := expandSTSZ(stsz, track)
	if err != nil {
		return err
	}
	if err := checkSTTS(stts, track, int64(len(sizes)), sttsNode.offset); err != nil {
		return err
	}
	if err != nil {
		return err
	}
	offsets, err := expandSTSC(stsc, stco, len(sizes), track)
	if err != nil {
		return err
	}
	var crcs []uint32
	if bcrcNode := stbl.find(mp4ext.BoxTypeBcrc()); bcrcNode != nil {
		box, err := decodeNode(data, bcrcNode, mp4.Context{})
		if err != nil {
			return err
		}
		b := box.(*mp4ext.Bcrc)
		if len(b.CRC32) != len(sizes) {
			return merr.New(merr.StageMetadata, merr.KindInconsistent, track.ID, bcrcNode.offset,
				"mp4: bcrc lists %d checksums for %d samples", len(b.CRC32), len(sizes))
		}
		crcs = b.CRC32
	}

	for i := 0; i < len(sizes); i++ {
		off, size := offsets[i], int64(sizes[i])
		if off < 0 || off+size > int64(len(data)) {
			return merr.New(merr.StageMetadata, merr.KindTruncated, track.ID, off,
				"mp4: audio sample %d overruns input (%d bytes at %d)", i+1, size, off)
		}
		s := Sample{Index: i + 1, Offset: off, Data: data[off : off+size]}
		if len(crcs) == len(sizes) {
			s.CRC32, s.HasCRC = crcs[i], true
		}
		track.Samples = append(track.Samples, s)
	}
	return nil
}

// checkSTTSPayloadSize rejects an stts whose declared entry count cannot fit
// in its payload before go-mp4's bit reader reports a generic overrun. This
// is the truncated-timestamp path and names the track and offset precisely.
func checkSTTSPayloadSize(data []byte, node *boxNode, trackID uint32) error {
	ps := node.payloadStart()
	if node.payloadSize() < 8 {
		return merr.New(merr.StageProbe, merr.KindTimestamp, trackID, ps,
			"mp4: stts payload is %d bytes, shorter than the box header", node.payloadSize())
	}
	entries := binary.BigEndian.Uint32(data[ps+4:])
	want := int64(8) + int64(entries)*8
	if want > node.payloadSize() {
		return merr.New(merr.StageProbe, merr.KindTimestamp, trackID, ps,
			"mp4: stts declares %d entries needing %d bytes but payload is %d",
			entries, want, node.payloadSize())
	}
	return nil
}

// checkSTTS validates the timing table structurally and against the mdhd
// duration: sample counts must equal the declared entry counts and the total
// tick span must match the track duration exactly. A table that declares more
// entries than it holds is reported as truncation.
func checkSTTS(stts *mp4.Stts, track *Track, samples int64, offset int64) error {
	if stts.EntryCount != uint32(len(stts.Entries)) {
		return merr.New(merr.StageProbe, merr.KindTimestamp, track.ID, offset,
			"mp4: stts declares %d entries but holds %d", stts.EntryCount, len(stts.Entries))
	}
	var count, ticks int64
	for _, e := range stts.Entries {
		if e.SampleDelta == 0 {
			return merr.New(merr.StageMetadata, merr.KindInconsistent, track.ID, offset,
				"mp4: stts entry has zero sample delta")
		}
		count += int64(e.SampleCount)
		ticks += int64(e.SampleCount) * int64(e.SampleDelta)
	}
	if count != samples {
		return merr.New(merr.StageMetadata, merr.KindInconsistent, track.ID, offset,
			"mp4: stts covers %d samples but %d are stored", count, samples)
	}
	if ticks != track.Duration {
		return merr.New(merr.StageMetadata, merr.KindInconsistent, track.ID, offset,
			"mp4: stts covers %d ticks but mdhd duration is %d", ticks, track.Duration)
	}
	return nil
}

func expandSTSZ(stsz *mp4.Stsz, track *Track) ([]uint32, error) {
	if stsz.SampleSize != 0 {
		out := make([]uint32, stsz.SampleCount)
		for i := range out {
			out[i] = stsz.SampleSize
		}
		if stsz.SampleSize == 0 && stsz.SampleCount > 0 {
			return nil, merr.New(merr.StageMetadata, merr.KindInconsistent, track.ID, 0,
				"mp4: stsz has zero sample size")
		}
		return out, nil
	}
	if uint32(len(stsz.EntrySize)) != stsz.SampleCount {
		return nil, merr.New(merr.StageMetadata, merr.KindTruncated, track.ID, 0,
			"mp4: stsz lists %d sizes but declares %d samples", len(stsz.EntrySize), stsz.SampleCount)
	}
	for i, s := range stsz.EntrySize {
		if s == 0 {
			return nil, merr.New(merr.StageMetadata, merr.KindInconsistent, track.ID, 0,
				"mp4: stsz sample %d has zero size", i+1)
		}
	}
	return stsz.EntrySize, nil
}

// expandSTSC maps sample sizes to absolute chunk offsets. This profile emits
// one sample per chunk; anything else is reported rather than interpreted.
func expandSTSC(stsc *mp4.Stsc, stco *mp4.Stco, sampleCount int, track *Track) ([]int64, error) {
	if int(stco.EntryCount) != len(stco.ChunkOffset) {
		return nil, merr.New(merr.StageMetadata, merr.KindTruncated, track.ID, 0,
			"mp4: stco declares %d chunks but holds %d", stco.EntryCount, len(stco.ChunkOffset))
	}
	if len(stsc.Entries) != 1 || stsc.Entries[0].SamplesPerChunk != 1 || stsc.Entries[0].FirstChunk != 1 {
		return nil, merr.New(merr.StageMetadata, merr.KindParse, track.ID, 0,
			"mp4: only one-sample-per-chunk layouts are supported")
	}
	if len(stco.ChunkOffset) != sampleCount {
		return nil, merr.New(merr.StageMetadata, merr.KindInconsistent, track.ID, 0,
			"mp4: %d chunks for %d samples", len(stco.ChunkOffset), sampleCount)
	}
	out := make([]int64, sampleCount)
	for i, off := range stco.ChunkOffset {
		out[i] = int64(off)
	}
	return out, nil
}

// layoutForChannels maps a channel count to the canonical layout name.
func layoutForChannels(ch uint16) string {
	switch ch {
	case 1:
		return "mono"
	case 2:
		return "stereo"
	case 6:
		return "5.1"
	case 8:
		return "7.1"
	default:
		return fmt.Sprintf("%dch", ch)
	}
}
