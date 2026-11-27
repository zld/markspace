// MP4 sample-table support. The moov/trak/mdia tree is walked with go-mp4;
// the authoritative per-sample timing comes from mdhd.timescale + stts deltas
// and per-sample sizes from stsz. Colour metadata is taken from the colr box.
// Everything is converted to integer nanoseconds without float accumulation.
package audio

import (
	"bytes"
	"fmt"

	mp4 "github.com/abema/go-mp4"

	"mediasync/internal/model"
)

// MaxMP4Bytes bounds the local MP4 blobs the parser will touch.
const MaxMP4Bytes = 256 << 20 // 256 MiB

// MP4Sample is one expanded sample with exact nanosecond timing.
type MP4Sample struct {
	Index   int
	Size    uint32
	DeltaNS model.NS
}

// MP4Info is the extracted, validated view of one track's sample table.
type MP4Info struct {
	Timescale  uint32
	Channels   uint16
	SampleRate uint32 // Hz; the 16.16 fixed-point value is shifted to integer
	Color      *model.ColorMeta
	Samples    []MP4Sample
	Failures   []model.Failure
}

// stts/stsz records captured during the walk and reconciled afterwards.
type rawTable struct {
	tts []mp4.SttsEntry
	sz  []uint32
	// uniformSize is stsz.sampleSize when every sample shares one size.
	uniformSize uint32
	szCount     uint32
}

// ParseSampleTable walks an in-memory MP4 blob and returns the expanded sample
// table. Structural problems are returned as failures; an unreadable top-level
// box returns an error.
func ParseSampleTable(trackID string, data []byte) (*MP4Info, error) {
	if len(data) == 0 {
		return nil, model.NewTrackFailure(trackID, model.StageMP4, model.ErrMP4Box, "empty MP4 data")
	}
	if len(data) > MaxMP4Bytes {
		return nil, model.NewTrackFailure(trackID, model.StageMP4, model.ErrPacketTooLarge,
			"MP4 blob exceeds "+itoa(MaxMP4Bytes)+" bytes")
	}

	info := &MP4Info{}
	raw := &rawTable{}
	r := bytes.NewReader(data)

	handler := func(h *mp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type.String() {
		case "mdhd":
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			if mdhd, ok := box.(*mp4.Mdhd); ok {
				info.Timescale = mdhd.Timescale
			}
		case "stts":
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			if stts, ok := box.(*mp4.Stts); ok {
				raw.tts = append(raw.tts, stts.Entries...)
			}
		case "stsz":
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			if stsz, ok := box.(*mp4.Stsz); ok {
				raw.uniformSize = stsz.SampleSize
				raw.szCount = stsz.SampleCount
				raw.sz = append(raw.sz, stsz.EntrySize...)
			}
		case "colr":
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			if colr, ok := box.(*mp4.Colr); ok && string(colr.ColourType[:]) == "nclx" {
				info.Color = &model.ColorMeta{
					ColorPrimaries:          colr.ColourPrimaries,
					TransferCharacteristics: colr.TransferCharacteristics,
					MatrixCoefficients:      colr.MatrixCoefficients,
					FullRange:               colr.FullRangeFlag,
				}
			}
		case "mp4a", "Opus":
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			if ase, ok := box.(*mp4.AudioSampleEntry); ok {
				info.Channels = ase.ChannelCount
				info.SampleRate = ase.SampleRate >> 16 // 16.16 fixed point
			}
		}
		// Recurse into container boxes (moov, trak, mdia, minf, stbl, stsd…).
		return h.Expand()
	}

	if _, err := mp4.ReadBoxStructure(r, handler); err != nil {
		return nil, model.NewTrackFailure(trackID, model.StageMP4, model.ErrMP4Box,
			"malformed MP4 structure: "+err.Error())
	}

	expandSamples(trackID, info, raw)
	return info, nil
}

// expandSamples turns the run-length stts table into one timed record per
// sample and reconciles it against stsz. Mismatches are failures, never silent
// truncation.
func expandSamples(trackID string, info *MP4Info, raw *rawTable) {
	var ttsCount uint32
	for _, e := range raw.tts {
		ttsCount += e.SampleCount
	}
	if ttsCount != raw.szCount {
		info.Failures = append(info.Failures, model.NewTrackFailure(trackID,
			model.StageMP4, model.ErrMP4SampleCount, fmt.Sprintf(
				"stts sample count %d disagrees with stsz sample count %d",
				ttsCount, raw.szCount)))
	}

	info.Samples = make([]MP4Sample, 0, ttsCount)
	idx := 0
	for _, e := range raw.tts {
		for k := uint32(0); k < e.SampleCount; k++ {
			ns, f := SampleDurationNS(e.SampleDelta, info.Timescale, idx)
			if f != nil {
				f.TrackID = trackID
				info.Failures = append(info.Failures, *f)
				ns = 0
			}
			size := raw.uniformSize
			if size == 0 && idx < len(raw.sz) {
				size = raw.sz[idx]
			}
			info.Samples = append(info.Samples, MP4Sample{
				Index: idx, Size: size, DeltaNS: ns,
			})
			idx++
			if idx >= model.MaxPacketsPerTrack {
				info.Failures = append(info.Failures, model.NewTrackFailure(trackID,
					model.StageMP4, model.ErrTooManyPackets,
					"sample count exceeds parser bound"))
				return
			}
		}
	}
}
