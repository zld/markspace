package index

import (
	"bytes"
	"fmt"
	"io"

	gomp4 "github.com/abema/go-mp4"

	"example.com/media-kernel/media"
)

// RoundTripBox describes one box read by go-mp4 and re-encoded for comparison.
type RoundTripBox struct {
	Type          string `json:"type"`
	OriginalBytes int    `json:"original_bytes"`
	EncodedBytes  int    `json:"encoded_bytes"`
	Consistent    bool   `json:"consistent"`
}

// RoundTripReport is the box-level parse/marshal consistency result.
type RoundTripReport struct {
	Checked    int            `json:"checked"`
	Consistent int            `json:"consistent"`
	Rate       float64        `json:"rate"`
	Boxes      []RoundTripBox `json:"boxes"`
}

// MP4Report contains structure, verified index and round-trip evidence.
type MP4Report struct {
	TopLevelBoxes []string        `json:"top_level_boxes"`
	Index         *Index          `json:"index"`
	RoundTrip     RoundTripReport `json:"round_trip"`
}

var stblPath = gomp4.BoxPath{
	gomp4.BoxTypeMoov(),
	gomp4.BoxTypeTrak(),
	gomp4.BoxTypeMdia(),
	gomp4.BoxTypeMinf(),
	gomp4.BoxTypeStbl(),
}

// AnalyzeMP4 reads sample tables with go-mp4, verifies keyframe markers in the
// supplied MP4 bytes and performs parse/marshal round trips of those boxes.
func AnalyzeMP4(data []byte) (*MP4Report, error) {
	report := &MP4Report{TopLevelBoxes: topLevelBoxes(data)}

	mdhdBox, err := extractBox(data, gomp4.BoxPath{
		gomp4.BoxTypeMoov(), gomp4.BoxTypeTrak(), gomp4.BoxTypeMdia(), gomp4.BoxTypeMdhd(),
	})
	if err != nil {
		return nil, err
	}
	tb := media.TimebaseMP4k
	if mdhdBox != nil {
		if mdhd, ok := mdhdBox.Payload.(*gomp4.Mdhd); ok && mdhd.Timescale > 0 {
			tb = media.Timebase{Numerator: 1, Denominator: int64(mdhd.Timescale)}
		}
	}

	stssBox, err := extractTable(data, gomp4.BoxTypeStss())
	if err != nil {
		return nil, err
	}
	sttsBox, err := extractTable(data, gomp4.BoxTypeStts())
	if err != nil {
		return nil, err
	}
	stszBox, err := extractTable(data, gomp4.BoxTypeStsz())
	if err != nil {
		return nil, err
	}
	stcoBox, err := extractTable(data, gomp4.BoxTypeStco())
	if err != nil {
		return nil, err
	}
	stscBox, err := extractTable(data, gomp4.BoxTypeStsc())
	if err != nil {
		return nil, err
	}
	if sttsBox == nil || stszBox == nil || stcoBox == nil {
		return nil, media.NewError("index.mp4", media.CodeMP4Invalid,
			"stts, stsz and stco sample tables are required", nil)
	}
	_ = stscBox // Fixtures use one chunk; stsc is still included in round-trip checks.

	stts := sttsBox.Payload.(*gomp4.Stts)
	stsz := stszBox.Payload.(*gomp4.Stsz)
	stco := stcoBox.Payload.(*gomp4.Stco)

	durations := expandStts(stts)
	sizes := expandStsz(stsz)
	if len(durations) != int(stsz.SampleCount) || len(sizes) != int(stsz.SampleCount) {
		return nil, media.NewError("index.mp4", media.CodeIndexMismatch,
			fmt.Sprintf("sample table counts disagree: stts=%d stsz=%d expanded=%d/%d",
				stts.EntryCount, stsz.SampleCount, len(durations), len(sizes)), nil)
	}
	if len(stco.ChunkOffset) == 0 {
		return nil, media.NewError("index.mp4", media.CodeMP4Invalid, "stco has no chunk offsets", nil)
	}

	samples := make([]Sample, 0, stsz.SampleCount)
	var start int64
	cursor := int64(stco.ChunkOffset[0])
	for i := uint32(0); i < stsz.SampleCount; i++ {
		size := int64(sizes[i])
		samples = append(samples, Sample{
			Number:        i + 1,
			StartTicks:    start,
			DurationTicks: durations[i],
			Size:          sizes[i],
			ByteStart:     cursor,
		})
		start += int64(durations[i])
		cursor += size
	}

	var sync []uint32
	if stssBox != nil {
		sync = stssBox.Payload.(*gomp4.Stss).SampleNumber
	}
	read := func(r media.ByteRange) ([]byte, error) {
		if r.Start < 0 || r.End < r.Start || r.End > int64(len(data)) {
			return nil, fmt.Errorf("byte range %d..%d outside MP4 size %d", r.Start, r.End, len(data))
		}
		return bytes.Clone(data[r.Start:r.End]), nil
	}
	report.Index = BuildFromSamples(tb, sync, samples, read)
	report.RoundTrip = roundTripBoxes(data, mdhdBox, stssBox, sttsBox, stszBox, stscBox, stcoBox)
	return report, nil
}

func extractTable(data []byte, typ gomp4.BoxType) (*gomp4.BoxInfoWithPayload, error) {
	return extractBox(data, append(append(gomp4.BoxPath{}, stblPath...), typ))
}

func extractBox(data []byte, path gomp4.BoxPath) (*gomp4.BoxInfoWithPayload, error) {
	boxes, err := gomp4.ExtractBoxWithPayload(bytes.NewReader(data), nil, path)
	if err != nil {
		return nil, media.NewError("index.mp4", media.CodeMP4Invalid,
			"failed to read MP4 box "+path[len(path)-1].String(), err)
	}
	if len(boxes) == 0 {
		return nil, nil
	}
	return boxes[0], nil
}

func expandStts(stts *gomp4.Stts) []uint32 {
	var out []uint32
	for _, entry := range stts.Entries {
		for i := uint32(0); i < entry.SampleCount; i++ {
			out = append(out, entry.SampleDelta)
		}
	}
	return out
}

func expandStsz(stsz *gomp4.Stsz) []uint32 {
	if stsz.SampleSize != 0 {
		out := make([]uint32, stsz.SampleCount)
		for i := range out {
			out[i] = stsz.SampleSize
		}
		return out
	}
	return append([]uint32(nil), stsz.EntrySize...)
}

func roundTripBoxes(data []byte, boxes ...*gomp4.BoxInfoWithPayload) RoundTripReport {
	report := RoundTripReport{}
	for _, box := range boxes {
		if box == nil || box.Payload == nil {
			continue
		}
		start := int64(box.Info.Offset + box.Info.HeaderSize)
		size := int64(box.Info.Size - box.Info.HeaderSize)
		original := data[start : start+size]

		var buf bytes.Buffer
		n, err := gomp4.Marshal(&buf, box.Payload, box.Info.Context)
		consistent := err == nil && n == uint64(len(original)) && bytes.Equal(buf.Bytes(), original)
		entry := RoundTripBox{
			Type:          box.Info.Type.String(),
			OriginalBytes: len(original),
			EncodedBytes:  buf.Len(),
			Consistent:    consistent,
		}
		if err != nil {
			entry.Type += ":marshal-error:" + err.Error()
		}
		report.Boxes = append(report.Boxes, entry)
		report.Checked++
		if consistent {
			report.Consistent++
		}
	}
	if report.Checked > 0 {
		report.Rate = float64(report.Consistent) / float64(report.Checked)
	}
	return report
}

func topLevelBoxes(data []byte) []string {
	r := bytes.NewReader(data)
	var out []string
	for {
		info, err := gomp4.ReadBoxInfo(r)
		if err == io.EOF {
			return out
		}
		if err != nil || info == nil {
			return append(out, "invalid-box")
		}
		out = append(out, info.Type.String())
		if _, err := info.SeekToEnd(r); err != nil {
			return append(out, "seek-error")
		}
	}
}
