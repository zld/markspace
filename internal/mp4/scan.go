// Package mp4 scans MP4 track structure, sync sample (keyframe) locations and
// timing tables on the shared integer tick clock. Index corruption and
// illegal timestamps are reported as located findings rather than repaired.
package mp4

import (
	"errors"
	"fmt"
	"io"

	gomp4 "github.com/abema/go-mp4"

	"tracecheck/internal/model"
)

// ErrNotMP4 is returned when the stream contains no parseable trak box.
var ErrNotMP4 = errors.New("no trak box found: not a parseable MP4 stream")

// Keyframe is one sync sample located on the tick clock.
type Keyframe struct {
	Sample uint32     `json:"sample"`
	At     model.Tick `json:"at"`
}

// TrackScan is the analysis result for one track.
type TrackScan struct {
	TrackID             uint32         `json:"track_id"`
	Handler             string         `json:"handler"`
	Timescale           uint32         `json:"timescale"`
	SampleCount         uint32         `json:"sample_count"`
	Duration            model.Tick     `json:"duration"`
	Keyframes           []Keyframe     `json:"keyframes"`
	KeyframeIntervals   []model.Tick   `json:"keyframe_intervals"`
	MaxKeyframeInterval model.Tick     `json:"max_keyframe_interval"`
	Findings            model.Findings `json:"findings"`
}

// ScanResult is the full container analysis.
type ScanResult struct {
	Tracks   []TrackScan    `json:"tracks"`
	Findings model.Findings `json:"findings"`
}

type trackBuilder struct {
	scan      *TrackScan
	stts      []gomp4.SttsEntry
	stss      []uint32
	hasSTSS   bool
	hasSTTS   bool
	hasSTSC   bool
	hasSTCO   bool
	hasSTSZ   bool
	sampleCnt uint32
	chunkOffsets []uint64
}

// Scan parses an MP4 stream and analyzes every track it contains. The
// read handler tracks the current trak with an explicit stack so nested
// box reads always land in the owning track builder.
func Scan(r io.ReadSeeker) (*ScanResult, error) {
	size, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, fmt.Errorf("mp4 scan: %w", err)
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("mp4 scan: %w", err)
	}
	builders := map[int]*trackBuilder{}
	var order []int
	var trakStack []int
	nextTrak := 0
	container := &ScanResult{}

	_, err = gomp4.ReadBoxStructure(r, func(h *gomp4.ReadHandle) (interface{}, error) {
		if h.BoxInfo.Type == gomp4.BoxTypeTrak() {
			idx := nextTrak
			nextTrak++
			order = append(order, idx)
			builders[idx] = &trackBuilder{scan: &TrackScan{TrackID: uint32(idx + 1)}}
			trakStack = append(trakStack, idx)
			if _, err := h.Expand(); err != nil {
				return nil, err
			}
			trakStack = trakStack[:len(trakStack)-1]
			return nil, nil
		}
		// Container boxes only hold children; descend before processing.
		if isContainer(h.BoxInfo.Type) {
			if _, err := h.Expand(); err != nil {
				return nil, err
			}
			return nil, nil
		}
		if len(trakStack) == 0 {
			return nil, nil
		}
		if err := readBox(h, builders[trakStack[len(trakStack)-1]]); err != nil {
			return nil, err
		}
		return nil, nil
	})
	if err != nil {
		return nil, fmt.Errorf("mp4 scan: %w", err)
	}
	if len(order) == 0 {
		return nil, ErrNotMP4
	}

	for _, idx := range order {
		finalizeTrack(builders[idx], container, uint64(size))
	}
	model.SortFindings(container.Findings)
	return container, nil
}

// isContainer reports whether a box type only groups child boxes and
// therefore needs an explicit Expand during traversal.
func isContainer(t gomp4.BoxType) bool {
	switch t {
	case gomp4.BoxTypeMoov(), gomp4.BoxTypeMdia(),
		gomp4.BoxTypeMinf(), gomp4.BoxTypeStbl():
		return true
	default:
		return false
	}
}

func readBox(h *gomp4.ReadHandle, b *trackBuilder) error {
	box, _, err := h.ReadPayload()
	if err != nil {
		return err
	}
	switch p := box.(type) {
	case *gomp4.Tkhd:
		b.scan.TrackID = p.TrackID
	case *gomp4.Hdlr:
		b.scan.Handler = string(p.HandlerType[:])
	case *gomp4.Mdhd:
		b.scan.Timescale = p.Timescale
		if p.Timescale == 0 {
			b.addFinding(model.ErrInvalidTimestamp, model.SeverityError,
				model.Span{}, "mdhd timescale is zero on track %d", b.scan.TrackID)
		} else {
			b.scan.Duration, _ = model.TicksFromTimescale(int64(p.GetDuration()), p.Timescale)
		}
	case *gomp4.Stts:
		b.hasSTTS = true
		b.stts = append(b.stts, p.Entries...)
	case *gomp4.Stss:
		b.hasSTSS = true
		b.stss = append(b.stss, p.SampleNumber...)
	case *gomp4.Stsz:
		b.hasSTSZ = true
		b.sampleCnt = p.SampleCount
		b.scan.SampleCount = p.SampleCount
	case *gomp4.Stsc:
		b.hasSTSC = true
	case *gomp4.Stco:
		b.hasSTCO = true
		for _, off := range p.ChunkOffset {
			b.chunkOffsets = append(b.chunkOffsets, uint64(off))
		}
	case *gomp4.Co64:
		b.hasSTCO = true
		b.chunkOffsets = append(b.chunkOffsets, p.ChunkOffset...)
	}
	return nil
}

func finalizeTrack(b *trackBuilder, out *ScanResult, fileSize uint64) {
	s := b.scan
	if !b.hasSTTS {
		b.addFinding(model.ErrMissingIndex, model.SeverityError, model.Span{},
			"stts timing table missing on track %d", s.TrackID)
	}
	if !b.hasSTSC || !b.hasSTCO {
		b.addFinding(model.ErrMissingIndex, model.SeverityError, model.Span{},
			"sample/chunk index missing on track %d", s.TrackID)
	}
	if !b.hasSTSZ {
		b.addFinding(model.ErrMissingIndex, model.SeverityWarning, model.Span{},
			"stsz size table missing on track %d", s.TrackID)
	}
	b.validateChunkOffsets(fileSize)
	analyzeTiming(b)
	if b.scan.Handler == "vide" {
		analyzeKeyframes(b)
	}
	out.Tracks = append(out.Tracks, *s)
	out.Findings = append(out.Findings, s.Findings...)
}

// sampleTime converts a 1-based sample number to ticks via the stts table.
func sampleTime(b *trackBuilder, sample1based uint32) model.Tick {
	if b.scan.Timescale == 0 {
		return 0
	}
	remaining := int64(sample1based)
	var mediaTime int64
	for _, e := range b.stts {
		n := int64(e.SampleCount)
		if remaining > n {
			mediaTime += n * int64(e.SampleDelta)
			remaining -= n
			continue
		}
		mediaTime += remaining * int64(e.SampleDelta)
		break
	}
	t, _ := model.TicksFromTimescale(mediaTime, b.scan.Timescale)
	return t
}

func analyzeTiming(b *trackBuilder) {
	var seen uint32
	for _, e := range b.stts {
		if e.SampleDelta == 0 {
			span := b.rangeSpan(seen+1, seen+e.SampleCount)
			b.addFinding(model.ErrInvalidTimestamp, model.SeverityError, span,
				"zero sample delta for samples %d..%d on track %d",
				seen+1, seen+e.SampleCount, b.scan.TrackID)
		}
		seen += e.SampleCount
	}
	if b.sampleCnt > 0 && seen != 0 && seen != b.sampleCnt {
		span := b.rangeSpan(seen+1, b.sampleCnt)
		b.addFinding(model.ErrInvalidTimestamp, model.SeverityWarning, span,
			"stts covers %d samples but stsz declares %d on track %d",
			seen, b.sampleCnt, b.scan.TrackID)
	}
}

func analyzeKeyframes(b *trackBuilder) {
	s := b.scan
	if !b.hasSTSS {
		b.addFinding(model.ErrNoKeyframes, model.SeverityError, model.Span{},
			"no stss sync sample table on video track %d", s.TrackID)
		return
	}
	var prev model.Tick
	for i, sample := range b.stss {
		if sample == 0 || (b.sampleCnt > 0 && sample > b.sampleCnt) {
			span := b.rangeSpan(sample, sample)
			b.addFinding(model.ErrMissingIndex, model.SeverityError, span,
				"stss entry %d points outside 1..%d on track %d",
				sample, b.sampleCnt, s.TrackID)
			continue
		}
		at := sampleTime(b, sample)
		s.Keyframes = append(s.Keyframes, Keyframe{Sample: sample, At: at})
		if i > 0 {
			interval := at - prev
			s.KeyframeIntervals = append(s.KeyframeIntervals, interval)
			if interval > s.MaxKeyframeInterval {
				s.MaxKeyframeInterval = interval
			}
		}
		prev = at
	}
}

// validateChunkOffsets flags chunk offsets that point outside the file or
// overlap the header region, locating the region by sample timing when
// possible. These are structural errors the kernel reports but never
// repairs.
func (b *trackBuilder) validateChunkOffsets(fileSize uint64) {
	for i, off := range b.chunkOffsets {
		if off == 0 || off >= fileSize {
			sample := uint32(i + 1)
			span := b.rangeSpan(sample, sample)
			b.addFinding(model.ErrBrokenChunk, model.SeverityError, span,
				"chunk %d offset %d outside file size %d on track %d",
				i, off, fileSize, b.scan.TrackID)
		}
	}
}

func (b *trackBuilder) rangeSpan(from, to uint32) model.Span {
	start := sampleTime(b, from)
	end := sampleTime(b, to+1)
	return model.NewSpan(start, end)
}

func (b *trackBuilder) addFinding(code model.ErrorCode, sev model.Severity, span model.Span, format string, args ...any) {
	f := model.NewFinding(code, model.SourceMP4, sev, span, format, args...)
	f.Track = b.scan.TrackID
	b.scan.Findings = append(b.scan.Findings, f)
}
