package stream

import (
	"fmt"
	"os"
	"sort"

	"mediasync/internal/clock"
	"mediasync/internal/issue"
)

// Keyframe is one sync sample with decode and presentation time.
type Keyframe struct {
	Index    int
	DTS      clock.Tick
	PTS      clock.Tick
	Offset   int64
	Size     uint32
	Duration clock.Tick // actual interval to the next keyframe
}

// TrackInfo summarises the video track's media time base.
type TrackInfo struct {
	TrackID     uint32
	Timescale   uint32
	SampleCount int
	Duration    clock.Tick
	Handler     string
	Width       uint16
	Height      uint16
}

// Interval is one keyframe gap measured by actual decode time.
type Interval struct {
	From, To int
	DTSGap   clock.Tick
	PTSGap   clock.Tick
	Samples  int
}

// MP4Result is the structural analysis of one MP4 input.
type MP4Result struct {
	Input       string
	Track       TrackInfo
	Keyframes   []Keyframe
	Frames      int
	Damaged     []issue.ByteRange
	Issues      issue.List
	Intervals   []Interval
	MedianGap   clock.Tick
	MinGap      clock.Tick
	MaxGap      clock.Tick
	Timescale   uint32
	FirstOffset int64
	// VFR reports that sample durations are not uniform. Intervals are always
	// computed from actual decode time in that case.
	VFR       bool
	VFRReason string
}

func toTick(delta uint64, timescale uint32) clock.Tick {
	if timescale == 0 {
		return 0
	}
	return clock.Tick((delta*90000 + uint64(timescale)/2) / uint64(timescale))
}

// ParseMP4 analyses the first video track. Structural damage is reported with
// the first damaged byte offset; nothing is repaired.
func ParseMP4(path string) (*MP4Result, error) { return ParseMP4As(path, path) }

// ParseMP4As is ParseMP4 with a stable label for reports. The label must not
// contain run-specific data (temporary directories, timestamps).
func ParseMP4As(path, label string) (*MP4Result, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	fileSize := stat.Size()
	res := &MP4Result{Input: label, FirstOffset: -1}

	scanIssues, scanDamage := scanBoxStructure(f, label, fileSize)
	res.Issues.Add(scanIssues...)
	res.Damaged = append(res.Damaged, scanDamage...)

	traks := collectTracks(f, label, res)
	if len(traks) == 0 {
		if len(res.Issues) == 0 {
			res.Issues.Add(issue.New(issue.UnsupportedInput, label, "no MP4 track found"))
		}
		return res, nil
	}
	video := pickVideoTrack(traks)
	if video == nil {
		res.Issues.Add(issue.New(issue.UnsupportedInput, label, "no video track (handler != vide)"))
		return res, nil
	}
	if video.mdhd == nil || video.stts == nil {
		res.Issues.Add(issue.New(issue.MP4Structure, label, "video track missing mdhd or stts").
			WithByte(video.trakOffset))
		return res, nil
	}
	res.Track.TrackID = video.trackID
	res.Track.Timescale = video.mdhd.Timescale
	res.Timescale = video.mdhd.Timescale
	res.Track.Handler = "vide"
	if video.hdlr != nil {
		res.Track.Handler = string(video.hdlr.HandlerType[:])
	}
	if video.mdhd.GetVersion() == 1 {
		res.Track.Duration = toTick(video.mdhd.DurationV1, video.mdhd.Timescale)
	} else {
		res.Track.Duration = toTick(uint64(video.mdhd.DurationV0), video.mdhd.Timescale)
	}
	if video.tkhd != nil {
		res.Track.Width = uint16(video.tkhd.Width >> 16)
		res.Track.Height = uint16(video.tkhd.Height >> 16)
	}

	samples, tableIssues := expandSamples(video, fileSize)
	stampInput(tableIssues, label)
	for i := range tableIssues {
		if tableIssues[i].Loc.ByteOffset >= 0 && tableIssues[i].Loc.ByteOffset < fileSize {
			res.Damaged = append(res.Damaged, issue.ByteRange{
				Input: label, Start: tableIssues[i].Loc.ByteOffset, End: fileSize,
				Categories: []issue.Category{tableIssues[i].Category},
			})
		}
	}
	res.Issues.Add(tableIssues...)
	res.Damaged = clampRanges(issue.MergeRanges(res.Damaged, 4), fileSize)
	res.Frames = len(samples)
	res.Track.SampleCount = len(samples)
	buildKeyframes(res, video, samples)
	detectVFR(res, video)
	normalizeMP4(res, fileSize)
	if len(res.Issues) > 0 {
		if first, ok := res.Issues.First(); ok && first.Loc.ByteOffset >= 0 {
			res.FirstOffset = first.Loc.ByteOffset
		}
	}
	return res, nil
}

// clampRanges trims ranges to the file extent and drops empty ones.
func clampRanges(in []issue.ByteRange, fileSize int64) []issue.ByteRange {
	out := in[:0]
	for _, r := range in {
		if r.Start < 0 {
			r.Start = 0
		}
		if r.End > fileSize {
			r.End = fileSize
		}
		if r.End < r.Start {
			r.End = r.Start
		}
		if r.End == r.Start && r.Start >= fileSize {
			continue
		}
		out = append(out, r)
	}
	return out
}

// detectVFR compares consecutive sample deltas. Different deltas mean a
// variable frame rate; interval statistics must then come from actual decode
// time, which buildKeyframes already guarantees.
func detectVFR(res *MP4Result, t *trakData) {
	if t.stts == nil || len(t.stts.Entries) == 0 {
		return
	}
	first := t.stts.Entries[0].SampleDelta
	for _, e := range t.stts.Entries[1:] {
		if e.SampleDelta != first {
			res.VFR = true
			res.VFRReason = fmt.Sprintf("sample deltas vary (%d vs %d)", first, e.SampleDelta)
			return
		}
	}
}

// normalize walks the merge again after keyframe construction.
func normalizeMP4(res *MP4Result, fileSize int64) {
	res.Damaged = clampRanges(issue.MergeRanges(res.Damaged, 4), fileSize)
	res.Issues = res.Issues.Sorted()
}

func buildKeyframes(res *MP4Result, t *trakData, samples []sample) {
	syncSet := map[int]bool{}
	if t.stss != nil {
		for _, n := range t.stss.SampleNumber {
			syncSet[int(n)] = true
		}
	} else {
		for i := 1; i <= len(samples); i++ {
			syncSet[i] = true
		}
	}
	for i, s := range samples {
		if !syncSet[i+1] {
			continue
		}
		res.Keyframes = append(res.Keyframes, Keyframe{
			Index: i + 1, DTS: s.dts, PTS: s.pts, Offset: s.offset, Size: s.size,
		})
	}
	for i := 0; i+1 < len(res.Keyframes); i++ {
		a, b := res.Keyframes[i], res.Keyframes[i+1]
		gap := b.DTS - a.DTS
		res.Keyframes[i].Duration = gap
		iv := Interval{From: a.Index, To: b.Index, DTSGap: gap, PTSGap: b.PTS - a.PTS, Samples: b.Index - a.Index}
		res.Intervals = append(res.Intervals, iv)
		if i == 0 || gap < res.MinGap {
			res.MinGap = gap
		}
		if gap > res.MaxGap {
			res.MaxGap = gap
		}
	}
	if len(res.Intervals) > 0 {
		gaps := make([]clock.Tick, len(res.Intervals))
		for i, iv := range res.Intervals {
			gaps[i] = iv.DTSGap
		}
		sort.Slice(gaps, func(i, j int) bool { return gaps[i] < gaps[j] })
		res.MedianGap = gaps[len(gaps)/2]
	}
}
