package ingest

import (
	"encoding/binary"
	"fmt"

	"mediapipe/internal/fault"
	"mediapipe/internal/limits"
	"mediapipe/internal/model"
	"mediapipe/internal/mxfkv"
)

// ParseMXF parses a restricted-KLV MXF fixture into model tracks. Unknown
// keys are tolerated and reported with their offset.
func ParseMXF(path string, errs *fault.Batch) ([]*model.Track, error) {
	data, ferr := readInput(path)
	if ferr != nil {
		errs.Add(ferr)
		return nil, ferr
	}
	items := scanKLV(path, data, errs)
	if len(errs.Fatal()) > 0 {
		return nil, errs.Fatal()[0]
	}
	if len(items) > limits.MaxKLVElements {
		return nil, errs.Add(limits.Exceeded(path, "KLV element count",
			items[limits.MaxKLVElements].offset, len(items), limits.MaxKLVElements))
	}

	tr := &model.Track{
		ID:         path + ":mxf-track-0",
		Kind:       model.KindVideo,
		Container:  model.ContainerMXF,
		SourceFile: path,
	}
	var indexItem *klvItem
	essenceOffsets := map[int]int64{}
	essenceSize := map[int]int64{}
	frame := 0
	for i := range items {
		it := &items[i]
		switch it.key {
		case mxfkv.KeyHeaderPartition():
			tr.TrackOff = it.offset
		case mxfkv.KeyPreface():
			if len(it.value) < 8 {
				errs.Add(fault.New(fault.KindTruncated,
					"preface value shorter than 8 bytes", path, it.offset, true))
			}
		case mxfkv.KeyTrack():
			applyTrackSet(tr, it, path, errs)
		case mxfkv.KeyTimelineIndex():
			indexItem = it
		case mxfkv.KeyEssence():
			if frame < limits.MaxSamplesPerTrack {
				essenceOffsets[frame] = it.offset
				essenceSize[frame] = int64(len(it.value))
			}
			frame++
		default:
			errs.Add(fault.New(fault.KindUnknown,
				"unknown KLV structure preserved at offset",
				path, it.offset, true))
		}
	}
	if indexItem == nil {
		errs.Add(fault.New(fault.KindTruncated,
			"missing timeline index pack", path, tr.TrackOff, true))
		return []*model.Track{tr}, nil
	}
	tr.Samples = parseTimelineIndex(indexItem, tr.Timescale, path, errs,
		essenceOffsets, essenceSize)
	if frame > limits.MaxSamplesPerTrack {
		errs.Add(limits.Exceeded(path, "essence item count",
			indexItem.offset, frame, limits.MaxSamplesPerTrack))
	}
	tr.SortPresentation()
	return []*model.Track{tr}, nil
}

func applyTrackSet(tr *model.Track, it *klvItem, path string, errs *fault.Batch) {
	items := parseLocalSet(path, it.offset+16, it.value, errs)
	colorN := 0
	for _, li := range items {
		switch li.tag {
		case mxfkv.TagTrackKind:
			if len(li.value) >= 1 && li.value[0] == mxfkv.KindAudio {
				tr.Kind = model.KindAudio
			}
		case mxfkv.TagTimescale:
			if len(li.value) >= 4 {
				tr.Timescale = binary.BigEndian.Uint32(li.value)
			}
		case mxfkv.TagSampleCount:
			// Consumed during index parsing; bounds checked there.
		case mxfkv.TagRotation:
			if len(li.value) >= 4 {
				tr.Rotation = model.Rotation{
					Degrees: int(binary.BigEndian.Uint32(li.value)),
					SourceOffset: li.offset, SourceBox: "track-set",
				}
			}
		case mxfkv.TagCrop:
			if len(li.value) >= 32 {
				tr.Crop = &model.Crop{
					Left:   int64(binary.BigEndian.Uint64(li.value[0:])),
					Top:    int64(binary.BigEndian.Uint64(li.value[8:])),
					Right:  int64(binary.BigEndian.Uint64(li.value[16:])),
					Bottom: int64(binary.BigEndian.Uint64(li.value[24:])),
				}
			}
		case mxfkv.TagColor:
			if colorN >= limits.MaxColorProfiles {
				errs.Add(limits.Exceeded(path, "color profiles",
					li.offset, colorN+1, limits.MaxColorProfiles))
				continue
			}
			colorN++
			if len(li.value) >= 7 {
				tr.Color.Declarations = append(tr.Color.Declarations, model.ColorDecl{
					ColorType: "nclx",
					Primaries: binary.BigEndian.Uint16(li.value[0:]),
					Transfer:  binary.BigEndian.Uint16(li.value[2:]),
					Matrix:    binary.BigEndian.Uint16(li.value[4:]),
					FullRange: li.value[6] != 0,
					SourceBox: "track-set", SourceOff: li.offset,
				})
			}
		default:
			errs.Add(fault.New(fault.KindUnknown,
				fmt.Sprintf("unknown local tag 0x%04x", li.tag),
				path, li.offset, true))
		}
	}
}

// parseTimelineIndex decodes start time and one fixed-size record per frame.
func parseTimelineIndex(it *klvItem, timescale uint32, path string,
	errs *fault.Batch, offs, sizes map[int]int64) []model.Sample {
	if len(it.value) < 12 {
		errs.Add(fault.New(fault.KindTruncated,
			"timeline index header truncated", path, it.offset, true))
		return nil
	}
	startUnits := binary.BigEndian.Uint64(it.value[0:])
	count := binary.BigEndian.Uint32(it.value[8:])
	if startUnits > uint64(limits.MaxDurationNanos) {
		errs.Add(limits.Exceeded(path, "index start time", it.offset,
			startUnits, uint64(limits.MaxDurationNanos)))
	}
	if count > limits.MaxSamplesPerTrack {
		errs.Add(limits.Exceeded(path, "index record count",
			it.offset, count, limits.MaxSamplesPerTrack))
		count = limits.MaxSamplesPerTrack
	}
	recStart := 12
	if timescale == 0 {
		errs.Add(fault.New(fault.KindInvalidInput,
			"zero timescale in timeline index", path, it.offset, false))
		return nil
	}
	scale := func(units uint64) int64 {
		return int64(units) * 1_000_000_000 / int64(timescale)
	}
	out := make([]model.Sample, 0, count)
	prevEnd := int64(0)
	for f := uint32(0); f < count; f++ {
		lo := recStart + int(f)*mxfkv.IndexRecSize
		if lo+mxfkv.IndexRecSize > len(it.value) {
			errs.Add(fault.New(fault.KindTruncated,
				fmt.Sprintf("index record %d truncated", f),
				path, it.offset+int64(lo), true))
			break
		}
		rec := it.value[lo:]
		ptsU := binary.BigEndian.Uint64(rec[0:])
		durU := binary.BigEndian.Uint32(rec[8:])
		plen := int64(binary.BigEndian.Uint32(rec[12:]))
		key := rec[17] == 1
		pts := scale(ptsU)
		dur := scale(uint64(durU))
		if dur < 0 {
			dur = 0
		}
		s := model.Sample{
			DTS: pts, PTS: pts, DurationNanos: dur,
			PayloadLen: plen, PayloadOff: offs[int(f)],
			Keyframe:    key,
		}
		if pts > prevEnd {
			s.Missing = true
		}
		prevEnd = pts + dur
		if sz, ok := sizes[int(f)]; ok {
			s.PayloadLen = sz
		}
		out = append(out, s)
	}
	return out
}
