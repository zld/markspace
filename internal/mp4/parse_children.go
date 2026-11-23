package mp4

import (
	"encoding/binary"
	"fmt"

	"mediatool/internal/timeline"
)

// tfhd optional-field flag bits (ISO/IEC 14496-12 8.8.7).
const (
	tfhdDefaultBaseIsMoof    uint32 = 0x020000
	tfhdDefaultSampleDur     uint32 = 0x000008
	tfhdDefaultSampleSize    uint32 = 0x000010
	tfhdDefaultSampleFlags   uint32 = 0x000020
	tfhdDurationIsEmpty      uint32 = 0x010000
	tfhdDefaultBaseOffset    uint32 = 0x000001
	tfhdSampleDescriptionIdx uint32 = 0x000002
)

// trun optional-field flag bits (ISO/IEC 14496-12 8.8.8).
const (
	trunDataOffset       uint32 = 0x000001
	trunFirstSampleFlags uint32 = 0x000004
	trunSampleDuration   uint32 = 0x000100
	trunSampleSize       uint32 = 0x000200
	trunSampleFlags      uint32 = 0x000400
	trunSampleCTO        uint32 = 0x000800
)

func parseTfhd(body []byte, abs int64, run *TrackRun) *timeline.MediaError {
	if len(body) < 8 {
		return timeline.ErrorAt(timeline.ClassBoxTruncated, abs, "tfhd shorter than 8 bytes")
	}
	flags := binary.BigEndian.Uint32(body[0:4]) & 0x00ffffff
	run.TrackID = binary.BigEndian.Uint32(body[4:8])
	pos := 8
	readU32 := func(what string) (uint32, bool) {
		if pos+4 > len(body) {
			run.ParseErrors = append(run.ParseErrors,
				timeline.ErrorAt(timeline.ClassBoxTruncated, int64(abs)+int64(pos),
					"tfhd "+what+" truncated"))
			return 0, false
		}
		v := binary.BigEndian.Uint32(body[pos : pos+4])
		pos += 4
		return v, true
	}
	readU64 := func(what string) (uint64, bool) {
		if pos+8 > len(body) {
			run.ParseErrors = append(run.ParseErrors,
				timeline.ErrorAt(timeline.ClassBoxTruncated, int64(abs)+int64(pos),
					"tfhd "+what+" truncated"))
			return 0, false
		}
		v := binary.BigEndian.Uint64(body[pos : pos+8])
		pos += 8
		return v, true
	}
	if flags&tfhdDefaultBaseOffset != 0 {
		readU64("base-data-offset")
	}
	if flags&tfhdSampleDescriptionIdx != 0 {
		readU32("sample-description-index")
	}
	if flags&tfhdDefaultSampleDur != 0 {
		if v, ok := readU32("default-sample-duration"); ok {
			for i := range run.SampleDurations {
				if run.SampleDurations[i] == 0 {
					run.SampleDurations[i] = v
				}
			}
		}
	}
	if flags&tfhdDefaultSampleSize != 0 {
		readU32("default-sample-size")
	}
	if flags&tfhdDefaultSampleFlags != 0 {
		readU32("default-sample-flags")
	}
	return nil
}

func parseTfdt(body []byte, abs int64) (uint64, *timeline.MediaError) {
	if len(body) < 5 {
		return 0, timeline.ErrorAt(timeline.ClassBoxTruncated, abs, "tfdt header truncated")
	}
	version := body[0]
	switch version {
	case 0:
		if len(body) < 8 {
			return 0, timeline.ErrorAt(timeline.ClassBoxTruncated, abs, "tfdt v0 truncated")
		}
		return uint64(binary.BigEndian.Uint32(body[4:8])), nil
	case 1:
		if len(body) < 12 {
			return 0, timeline.ErrorAt(timeline.ClassBoxTruncated, abs, "tfdt v1 truncated")
		}
		return binary.BigEndian.Uint64(body[4:12]), nil
	default:
		return 0, timeline.ErrorAt(timeline.ClassFieldMissing, abs,
			fmt.Sprintf("unsupported tfdt version %d", version))
	}
}

func parseTrun(body []byte, abs int64, run *TrackRun) *timeline.MediaError {
	if len(body) < 8 {
		return timeline.ErrorAt(timeline.ClassBoxTruncated, abs, "trun shorter than 8 bytes")
	}
	version := body[0]
	flags := binary.BigEndian.Uint32(body[0:4]) & 0x00ffffff
	count := binary.BigEndian.Uint32(body[4:8])
	pos := 8
	if flags&trunDataOffset != 0 {
		if pos+4 > len(body) {
			return timeline.ErrorAt(timeline.ClassBoxTruncated, abs+int64(pos), "trun data-offset truncated")
		}
		run.DataOffset = int32(binary.BigEndian.Uint32(body[pos : pos+4]))
		run.HasDataOffset = true
		pos += 4
	}
	if flags&trunFirstSampleFlags != 0 {
		pos += 4
	}
	need := int(count)
	run.SampleDurations = growU32(run.SampleDurations, need)
	run.SampleSizes = growU32(run.SampleSizes, need)
	run.SampleFlags = growU32(run.SampleFlags, need)
	entryFields := 0
	for _, bit := range []uint32{trunSampleDuration, trunSampleSize, trunSampleFlags, trunSampleCTO} {
		if flags&bit != 0 {
			entryFields++
		}
	}
	entrySize := 4 * entryFields
	for i := 0; i < need; i++ {
		if pos+entrySize > len(body) {
			run.ParseErrors = append(run.ParseErrors,
				timeline.ErrorAt(timeline.ClassBoxTruncated, abs+int64(pos),
					fmt.Sprintf("trun entry %d truncated", i)))
			break
		}
		if flags&trunSampleDuration != 0 {
			run.SampleDurations[i] = binary.BigEndian.Uint32(body[pos : pos+4])
			pos += 4
		}
		if flags&trunSampleSize != 0 {
			run.SampleSizes[i] = binary.BigEndian.Uint32(body[pos : pos+4])
			pos += 4
		}
		if flags&trunSampleFlags != 0 {
			run.SampleFlags[i] = binary.BigEndian.Uint32(body[pos : pos+4])
			pos += 4
		}
		if flags&trunSampleCTO != 0 {
			pos += 4 // composition offsets are reserved for the timeline layer
			_ = version
		}
	}
	return nil
}

func growU32(s []uint32, need int) []uint32 {
	if cap(s) >= need {
		return s[:need]
	}
	out := make([]uint32, need)
	copy(out, s)
	return out
}
