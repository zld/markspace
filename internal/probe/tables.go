package probe

import (
	"fmt"
	"sort"

	"mediaflow/internal/clock"
	"mediaflow/internal/fault"
)

func parseTracks(f interface {
	ReadAt([]byte, int64) (int, error)
}, moov *boxNode) ([]*Track, []fault.Interval) {
	var tracks []*Track
	var intervals []fault.Interval
	for i, trakBox := range findChildren(moov, "trak") {
		t := &Track{Number: i + 1, SyncSamples: map[int]bool{}}
		tkhd := findChild(trakBox, "tkhd")
		mdia := findChild(trakBox, "mdia")
		if tkhd == nil || mdia == nil {
			intervals = append(intervals, fault.Interval{
				Kind: "missing_track_boxes", Class: fault.ClassMissingTrack, TrackNumber: i + 1,
				Reject: true, Message: "trak is missing tkhd or mdia",
			})
			continue
		}
		parseTKHD(f, t, tkhd)
		parseMDHD(f, t, findChild(mdia, "mdhd"))
		parseHDLR(f, t, findChild(mdia, "hdlr"))
		if edts := findChild(trakBox, "edts"); edts != nil {
			if elst := findChild(edts, "elst"); elst != nil {
				edits, err := parseELST(f, elst)
				if err != nil {
					intervals = append(intervals, tableFault(t, err, "elst"))
				}
				t.Edits = edits
			}
		}
		stbl := findChild(findChild(mdia, "minf"), "stbl")
		if stbl == nil {
			intervals = append(intervals, fault.Interval{
				Kind: "missing_stbl", Class: fault.ClassMissingTrack, TrackID: t.ID, TrackNumber: t.Number,
				Reject: true, Message: "media information is missing stbl",
			})
			tracks = append(tracks, t)
			continue
		}
		if err := parseSampleTables(f, t, stbl); err != nil {
			intervals = append(intervals, tableFault(t, err, "sample table"))
		}
		t.Timeline = clock.Timeline{
			TrackID: t.ID, TrackNumber: t.Number, MediaTimescale: t.Timescale,
			MediaDuration: t.DurationTicks, FirstSampleTick: t.FirstTick, Edits: t.Edits,
		}
		intervals = append(intervals, t.Timeline.ValidateEdits()...)
		intervals = append(intervals, detectTimelineFacts(t)...)
		tracks = append(tracks, t)
	}
	return tracks, intervals
}

func parseTKHD(f interface {
	ReadAt([]byte, int64) (int, error)
}, t *Track, n *boxNode) {
	b, err := readNodePayload(f, n)
	if err != nil || len(b) < 84 {
		return
	}
	version, _ := fullBoxVersionFlags(b)
	if version == 1 {
		t.ID = u32(b, 20)
	} else {
		t.ID = u32(b, 12)
	}
	if len(b) >= 8 {
		t.Width = u32(b, len(b)-8) >> 16
		t.Height = u32(b, len(b)-4) >> 16
	}
}

func parseMDHD(f interface {
	ReadAt([]byte, int64) (int, error)
}, t *Track, n *boxNode) {
	if n == nil {
		return
	}
	b, err := readNodePayload(f, n)
	if err != nil || len(b) < 24 {
		return
	}
	version, _ := fullBoxVersionFlags(b)
	if version == 1 {
		t.Timescale = u32(b, 20)
		t.DurationTicks = int64(u64(b, 24))
	} else {
		t.Timescale = u32(b, 12)
		t.DurationTicks = int64(u32(b, 16))
	}
	if t.Timescale == 0 {
		// Detection is surfaced by detectTimelineFacts after track identity is known.
		t.Timescale = 0
	}
}

func parseHDLR(f interface {
	ReadAt([]byte, int64) (int, error)
}, t *Track, n *boxNode) {
	if n == nil {
		return
	}
	b, err := readNodePayload(f, n)
	if err != nil || len(b) < 12 {
		return
	}
	t.Handler = string(b[8:12])
	switch t.Handler {
	case "vide":
		t.Type = "video"
	case "soun":
		t.Type = "audio"
	default:
		t.Type = t.Handler
	}
}

func parseELST(f interface {
	ReadAt([]byte, int64) (int, error)
}, n *boxNode) ([]clock.Edit, error) {
	b, err := readNodePayload(f, n)
	if err != nil {
		return nil, err
	}
	if len(b) < 8 {
		return nil, fmt.Errorf("elst payload too short")
	}
	version, _ := fullBoxVersionFlags(b)
	count := int(u32(b, 4))
	off := 8
	out := make([]clock.Edit, 0, count)
	for i := 0; i < count; i++ {
		var e clock.Edit
		if version == 1 {
			if len(b) < off+20 {
				return nil, fmt.Errorf("elst v1 entry %d truncated", i)
			}
			e.Duration = int64(u64(b, off))
			e.MediaTime = int64(u64(b, off+8))
			e.MediaRateInt = int16(u16(b, off+16))
			e.MediaRateFrac = int16(u16(b, off+18))
			off += 20
		} else {
			if len(b) < off+12 {
				return nil, fmt.Errorf("elst v0 entry %d truncated", i)
			}
			e.Duration = int64(u32(b, off))
			e.MediaTime = int64(int32(u32(b, off+4)))
			e.MediaRateInt = int16(u16(b, off+8))
			e.MediaRateFrac = int16(u16(b, off+10))
			off += 12
		}
		out = append(out, e)
	}
	return out, nil
}

func tableFault(t *Track, err error, table string) fault.Interval {
	return fault.Interval{
		Kind: "invalid_sample_table", Class: fault.ClassUnsupported,
		TrackID: t.ID, TrackNumber: t.Number, Reject: true, Quarantine: true,
		Message: fmt.Sprintf("cannot parse %s: %v", table, err),
	}
}

func sortedSyncIndexes(m map[int]bool) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}
