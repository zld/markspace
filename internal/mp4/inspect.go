package mp4

import (
	"fmt"
	"os"

	"mediascan/internal/clock"
	"mediascan/internal/model"
)

// Sample is one parsed media sample with source and unified timestamps.
type Sample struct {
	Index         int64
	Offset        int64
	Size          uint32
	Delta         uint32
	TimeSource    int64
	TimeUnified   int64
	DurationTicks int64
	Sync          bool
}

// Track is a progressive sample track.
type Track struct {
	ID          uint32
	Timescale   uint32
	Samples     []Sample
	SyncIndices []int64 // zero-based
}

// FragmentInfo is one movie fragment (moof/traf/tfdt/trun).
type FragmentInfo struct {
	Sequence      uint32
	BoxOffset     int64
	BaseDecodeRaw uint64
	Samples       []Sample
}

// Result is the inspection output.
type Result struct {
	Segment   string
	FileSize  int64
	Kind      string // progressive | fragmented | initialization | unknown
	Timescale uint32
	Tracks    []Track
	Fragments []FragmentInfo
	Faults    []model.Fault
	Headers   []BoxHeader
}

// Options controls InspectWith.
type Options struct {
	Segment           string
	FallbackTimescale uint32
}

// Inspect opens and analyses one MP4 file.
func Inspect(path string) (*Result, error) {
	return InspectWith(path, Options{})
}

// InspectWith analyses one MP4 file with options. A missing file returns a
// Go error (unrecoverable); structural problems become Result.Faults.
func InspectWith(path string, opts Options) (*Result, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := fi.Size()

	headers, faults := scanBoxes(f, size, opts.Segment)
	res := &Result{
		Segment:  opts.Segment,
		FileSize: size,
		Headers:  headers,
		Faults:   faults,
	}

	hasMoov := findHeader(headers, "moov") != nil
	hasMoof := len(findAllHeaders(headers, "moof")) > 0
	hasMvex := findHeader(headers, "mvex") != nil

	switch {
	case hasMoof:
		res.Kind = "fragmented"
	case hasMoov && hasMvex:
		res.Kind = "initialization"
	case hasMoov:
		res.Kind = "progressive"
	default:
		res.Kind = "unknown"
	}

	ts, tsFault := readTimescale(f, headers, opts.Segment)
	if tsFault != nil {
		res.Faults = append(res.Faults, *tsFault)
	}
	if ts == 0 && opts.FallbackTimescale != 0 {
		ts = opts.FallbackTimescale
	}
	res.Timescale = ts

	switch res.Kind {
	case "progressive":
		track, tfaults := buildProgressiveTrack(f, headers, ts, opts.Segment)
		res.Faults = append(res.Faults, tfaults...)
		if track != nil {
			res.Tracks = []Track{*track}
			enrichTruncation(res, track)
		}
	case "fragmented":
		frags, ffaults := buildFragments(f, headers, ts, opts.Segment)
		res.Faults = append(res.Faults, ffaults...)
		res.Fragments = frags
	case "initialization":
		// timescale/stsd are the necessary init data; ts==0 already faulted.
	default:
		flt := model.NewFault(model.CodeMissingInit, opts.Segment,
			fmt.Sprintf("no moov or moof boxes found in %d-byte file", size))
		flt.OffsetBytes = 0
		res.Faults = append(res.Faults, flt)
	}

	if res.Kind == "fragmented" && !hasMoov && opts.FallbackTimescale == 0 && ts == 0 {
		flt := model.NewFault(model.CodeMissingInit, opts.Segment,
			"fragmented segment has no moov and no initialization timescale")
		moof := findHeader(headers, "moof")
		if moof != nil {
			flt.BoxOffset = moof.Offset
		}
		res.Faults = append(res.Faults, flt)
	}

	return res, nil
}

// toUnified converts source ticks, recording a fault on zero timescale.
func toUnified(ticks int64, ts uint32, segment string) int64 {
	if ts == 0 {
		return 0
	}
	v, err := clock.ToUnified(ticks, ts)
	if err != nil {
		return 0
	}
	return v
}

// enrichTruncation attaches the damaged sample index/time to the EOF fault.
func enrichTruncation(res *Result, tr *Track) {
	damaged := int64(-1)
	for i := range tr.Samples {
		s := &tr.Samples[i]
		end := s.Offset + int64(s.Size)
		if s.Offset > res.FileSize || end > res.FileSize {
			damaged = int64(i)
			break
		}
	}
	if damaged < 0 {
		return
	}
	s := tr.Samples[damaged]
	startU := s.TimeUnified
	endU := startU + s.DurationTicks
	if s.DurationTicks == 0 {
		endU = startU
	}

	// Prefer an existing truncation fault (its offset is exact EOF).
	idx := -1
	for i := range res.Faults {
		if res.Faults[i].Code == model.CodeTruncatedBox {
			idx = i
			break
		}
	}
	if idx < 0 {
		f := model.NewFault(model.CodeTruncatedBox, res.Segment,
			fmt.Sprintf("sample %d data extends past end of file", damaged))
		f.OffsetBytes = res.FileSize
		res.Faults = append(res.Faults, f)
		idx = len(res.Faults) - 1
	}
	res.Faults[idx].SampleIndex = damaged
	res.Faults[idx].TimeStart = startU
	res.Faults[idx].TimeEnd = endU
	if res.Faults[idx].Message == "" {
		res.Faults[idx].Message = fmt.Sprintf("sample %d truncated", damaged)
	}
}
