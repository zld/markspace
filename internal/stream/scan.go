// Package stream maps WAV/ABLK sample blocks onto the shared integer clock,
// localizes corrupt ranges and computes keyframe interval statistics.
package stream

import (
	"assetcheck/internal/errmodel"
	"assetcheck/internal/mediaclock"
	"assetcheck/internal/wav"
)

// Scan budget defaults. These bound CPU/memory work; generated fixtures are
// tiny but the kernel must state explicit limits.
const (
	DefaultMaxScanFrames = int64(50_000_000)
	DefaultMaxRanges     = 10_000
	FallbackBlockFrames  = int64(1024)
)

// Budget caps scanning work. Zero fields select defaults.
type Budget struct {
	MaxScanFrames int64 `json:"max-scan-frames"`
	MaxRanges     int   `json:"max-ranges"`
}

func (b Budget) withDefaults() Budget {
	if b.MaxScanFrames <= 0 {
		b.MaxScanFrames = DefaultMaxScanFrames
	}
	if b.MaxRanges <= 0 {
		b.MaxRanges = DefaultMaxRanges
	}
	return b
}

// BlockVerdict is the integrity/geometry result for one block.
type BlockVerdict struct {
	Index      int                `json:"index"`
	Range      mediaclock.Range   `json:"range"`
	ByteOffset int64              `json:"byte-offset"`
	Frames     int64              `json:"frames"`
	Keyframe   bool               `json:"keyframe"`
	State      string             `json:"state"` // ok | corrupt | truncated | duplicate
	Code       errmodel.Code      `json:"code,omitempty"`
	ExpectedCRC uint32            `json:"expected-crc,omitempty"`
	ActualCRC   uint32             `json:"actual-crc,omitempty"`
}

// Report is the deterministic analysis output.
type Report struct {
	SampleRate  int                 `json:"sample-rate"`
	Channels    int                 `json:"channels"`
	Clock       *mediaclock.Clock   `json:"-"`
	TotalFrames int64               `json:"total-frames"`
	PresentFrames int64             `json:"present-frames"`

	Blocks    []BlockVerdict      `json:"blocks"`
	Keyframes []int64             `json:"keyframes"`
	Interval  IntervalStats       `json:"interval"`

	CorruptRanges []mediaclock.Range `json:"corrupt-ranges"`
	CorruptFrames int64              `json:"corrupt-frames"`
	CorruptRatio  float64            `json:"corrupt-ratio"`

	Findings             []*errmodel.Error `json:"findings,omitempty"`
	DuplicateBoundaries  []int64           `json:"duplicate-boundaries,omitempty"`

	Budget         Budget `json:"budget"`
	BudgetExceeded bool   `json:"budget-exceeded"`
	LastFrame      int64  `json:"last-frame"`
}

// Scan analyzes a parsed WAV using its optional ABLK integrity table.
func Scan(w *wav.Wav, data []byte, budget Budget) (*Report, error) {
	budget = budget.withDefaults()
	clock, err := mediaclock.New(w.Config.SampleRate)
	if err != nil {
		return nil, err
	}
	align := int64(w.Config.BlockAlign)
	declaredFrames := FrameCeilDiv(w.DeclaredDataSize, align)
	totalFrames := declaredFrames
	if w.ValidFrames > totalFrames {
		totalFrames = w.ValidFrames
	}
	limit := totalFrames
	if limit > budget.MaxScanFrames {
		limit = budget.MaxScanFrames
	}

	r := &Report{
		SampleRate:    w.Config.SampleRate,
		Channels:      w.Config.Channels,
		Clock:         clock,
		TotalFrames:   totalFrames,
		PresentFrames: w.ValidFrames,
		Budget:        budget,
		LastFrame:     limit - 1,
	}
	if totalFrames > budget.MaxScanFrames {
		r.BudgetExceeded = true
		r.Findings = append(r.Findings, errmodel.New(errmodel.ErrScanBudget,
			"scan budget %d frames exceeded; processed through frame %d of %d",
			budget.MaxScanFrames, r.LastFrame, totalFrames))
	}

	table := w.ParseBlockTable(data)
	r.Findings = append(r.Findings, w.Findings...)

	var rawRanges []mediaclock.Range
	addRange := func(rg mediaclock.Range) {
		if len(rawRanges) >= budget.MaxRanges {
			if !r.BudgetExceeded {
				r.BudgetExceeded = true
				r.Findings = append(r.Findings, errmodel.New(errmodel.ErrScanBudget,
					"corrupt-range budget %d reached; remaining ranges not retained", budget.MaxRanges))
			}
			return
		}
		rawRanges = append(rawRanges, rg)
	}

	if table != nil {
		seen := map[int64]bool{}
		for _, e := range table.Entries {
			start := e.StartFrame
			count := int64(e.FrameCount)
			if start >= limit {
				break
			}
			end := start + count
			bv := BlockVerdict{
				Index: int(e.Index), ByteOffset: w.DataOffset + start*align,
				Range: mediaclock.Range{Start: start, End: end}, Frames: count,
				Keyframe: e.Keyframe(), ExpectedCRC: e.CRC32,
			}
			if seen[start] {
				r.DuplicateBoundaries = append(r.DuplicateBoundaries, start)
				r.Findings = append(r.Findings, errmodel.New(errmodel.ErrDuplicateBoundary,
					"duplicate time boundary at frame %d (block %d)", start, e.Index).AtFrame(start))
				bv.State = "duplicate"
				bv.Code = errmodel.ErrDuplicateBoundary
			}
			seen[start] = true

			byteStart := start * align
			byteEnd := byteStart + count*align
			complete := byteStart >= 0 && byteEnd <= int64(len(w.FramedPCM))
			switch {
			case !complete:
				bv.State = "truncated"
				bv.Code = errmodel.ErrTruncatedBlock
				r.Findings = append(r.Findings, errmodel.New(errmodel.ErrTruncatedBlock,
					"block %d at frame %d is truncated: need %d PCM bytes, %d available",
					e.Index, start, count*align, max64(0, int64(len(w.FramedPCM))-byteStart)).
					AtOffset(bv.ByteOffset).AtFrame(start))
				addRange(mediaclock.Range{Start: start, End: end})
			default:
				actual := wav.CRCBlock(w.FramedPCM[byteStart:byteEnd])
				bv.ActualCRC = actual
				if actual != e.CRC32 {
					bv.State = "corrupt"
					bv.Code = errmodel.ErrBlockChecksum
					r.Findings = append(r.Findings, errmodel.New(errmodel.ErrBlockChecksum,
						"block %d CRC mismatch at frame %d: table=%08x actual=%08x",
						e.Index, start, e.CRC32, actual).AtOffset(bv.ByteOffset).AtFrame(start))
					addRange(mediaclock.Range{Start: start, End: end})
				} else if bv.State != "duplicate" {
					bv.State = "ok"
				}
			}
			r.Blocks = append(r.Blocks, bv)
			if bv.Keyframe && !r.isDuplicateStart(start) {
				r.Keyframes = append(r.Keyframes, start)
			}
		}
	} else {
		// No integrity table: synthesize uniform random-access boundaries.
		for start := int64(0); start < limit; start += FallbackBlockFrames {
			count := FallbackBlockFrames
			if start+count > totalFrames {
				count = totalFrames - start
			}
			r.Blocks = append(r.Blocks, BlockVerdict{
				Index: len(r.Blocks), Frames: count, Keyframe: true, State: "ok",
				Range:      mediaclock.Range{Start: start, End: start + count},
				ByteOffset: w.DataOffset + start*align,
			})
			r.Keyframes = append(r.Keyframes, start)
		}
		// Structural truncation still maps to an exact missing-frame range.
		if w.DataTruncated || len(w.TailBytes) > 0 {
			addRange(mediaclock.Range{Start: w.ValidFrames, End: totalFrames})
		}
	}

	r.CorruptRanges = mediaclock.MergeUnions(rawRanges)
	r.CorruptFrames = CorruptFrames(r.CorruptRanges)
	if r.CorruptFrames > totalFrames && totalFrames > 0 {
		r.CorruptFrames = totalFrames
	}
	r.CorruptRatio = CorruptRatio(r.CorruptFrames, totalFrames)
	r.Interval = ComputeIntervals(r.Keyframes)
	return r, nil
}

func (r *Report) isDuplicateStart(f int64) bool {
	for _, d := range r.DuplicateBoundaries {
		if d == f {
			return true
		}
	}
	return false
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
