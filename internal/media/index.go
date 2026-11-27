// Package media: reading the container index back from finished bytes.
package media

import (
	"io"
	"sort"

	"github.com/abema/go-mp4"

	"example.com/assetpack/internal/fault"
)

// ReadIndex parses a container and returns its sample index.  It uses
// go-mp4's box walker so the index is derived from bytes on disk, not from
// what the writer intended to write.
func ReadIndex(r io.ReadSeeker) (Index, error) {
	var ix Index
	var stts *mp4.Stts
	var stsz *mp4.Stsz
	var stco *mp4.Stco
	var stsdEntry *mp4.AudioSampleEntry
	_, err := mp4.ReadBoxStructure(r, func(h *mp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type {
		case mp4.BoxTypeMvhd():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			if mvhd, ok := box.(*mp4.Mvhd); ok {
				ix.Timescale = mvhd.Timescale
				ix.Duration = mvhd.GetDuration()
			}
			return nil, nil
		case mp4.BoxTypeMdhd():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			if mdhd, ok := box.(*mp4.Mdhd); ok {
				ix.Duration = mdhd.GetDuration()
			}
			return nil, nil
		case mp4.BoxTypeStts(), mp4.BoxTypeStsz(), mp4.BoxTypeStco(), mp4.BoxTypeIpcm():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			switch b := box.(type) {
			case *mp4.Stts:
				stts = b
			case *mp4.Stsz:
				stsz = b
			case *mp4.Stco:
				stco = b
			case *mp4.AudioSampleEntry:
				stsdEntry = b
			}
			return nil, nil
		}
		if h.BoxInfo.IsSupportedType() {
			return h.Expand()
		}
		return nil, nil
	})
	if err != nil {
		return ix, fault.New(fault.StageAudio, fault.KindMissing, "parse container index: %v", err)
	}
	if stts == nil || stsz == nil || stco == nil || stsdEntry == nil {
		return ix, fault.New(fault.StageAudio, fault.KindMissing,
			"container is missing stts/stsz/stco or the audio sample entry")
	}
	ix.Rate = int(stsdEntry.GetSampleRateInt())
	ix.Channels = int(stsdEntry.ChannelCount)
	ix.SampleBytes = ix.Channels * bitsPerSample / 8
	ix.Samples = int(stsz.SampleCount)
	ix.Chunks = len(stco.ChunkOffset)
	var frames uint64
	for _, e := range stts.Entries {
		frames += uint64(e.SampleCount) * uint64(e.SampleDelta)
	}
	ix.Frames = int64(frames)
	return ix, nil
}

// CanonicalIndex renders the stable text form used in reports.
func (ix Index) Canonical() string {
	return itoa(uint64(ix.Timescale)) + "/" + itoa(uint64(ix.Frames)) + "/" +
		itoa(uint64(ix.Samples)) + "/" + itoa(uint64(ix.Size))
}

func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}

// SortedNames returns map keys in order, used for deterministic output.
func SortedNames(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
