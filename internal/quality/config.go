package quality

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"time"

	"archive-lab/internal/media"
)

// Request is the user's quality goal for one restoration run.
type Request struct {
	TargetBps int64
	MaxBps    int64
	MinPSNR   float64
	BudgetMs  int64
}

// Validate checks the request for internal contradictions and usage errors.
func (r Request) Validate() error {
	switch {
	case r.TargetBps <= 0:
		return media.Usage("--target-bps must be positive, got %d", r.TargetBps)
	case r.MaxBps <= 0:
		return media.Usage("--max-bps must be positive, got %d", r.MaxBps)
	case r.MinPSNR <= 0:
		return media.Usage("--min-psnr must be positive, got %g", r.MinPSNR)
	case r.BudgetMs <= 0:
		return media.Usage("--budget-ms must be positive, got %d", r.BudgetMs)
	case r.TargetBps > r.MaxBps:
		return media.New(media.StagePlan, "config", "target_bps %d exceeds max_bps %d: constraints are mutually exclusive", r.TargetBps, r.MaxBps)
	}
	return nil
}

// VideoSpec is the source geometry needed to translate bitrate into bytes.
type VideoSpec struct {
	Width  int
	Height int
	FPSNum int64
	FPSDen int64
}

// Valid reports whether the spec can be used for rate estimates.
func (v VideoSpec) Valid() bool {
	return v.Width > 0 && v.Height > 0 && v.FPSNum > 0 && v.FPSDen > 0
}

// FrameBytes returns the per-frame bitrate allocation in bytes.
func (v VideoSpec) FrameBytes(bps int64) int64 {
	if !v.Valid() {
		return 0
	}
	// bytes = bps * den / (8 * num), rounded half up.
	n := bps*v.FPSDen + 4*v.FPSNum
	n /= 8 * v.FPSNum
	if n < 1 {
		n = 1
	}
	return n
}

// Config is a fully resolved conversion configuration. QuantizerMilli is the
// rate-control anchor in hundredths of a quantiser step (100 = q1.00).
type Config struct {
	Codec          string  `json:"codec"`
	QuantizerMilli int     `json:"quantizer_milli"`
	TargetBps      int64   `json:"target_bps"`
	MaxBps         int64   `json:"max_bps"`
	MinPSNR        float64 `json:"min_psnr"`
	BudgetMs       int64   `json:"budget_ms"`
}

// Digest is the configuration half of the cache key.
func (c Config) Digest() string {
	h := sha256.New()
	fmt.Fprintf(h, "codec=%s\nquantizer_milli=%d\ntarget_bps=%d\nmax_bps=%d\nmin_psnr=%.4f\nbudget_ms=%d\n",
		c.Codec, c.QuantizerMilli, c.TargetBps, c.MaxBps, c.MinPSNR, c.BudgetMs)
	return hex.EncodeToString(h.Sum(nil))
}

// Plan is the evidence behind a configuration choice.
type Plan struct {
	Config       Config
	Request      Request
	Spec         VideoSpec
	Segments     []int
	Deferred     []int
	EstBps       int64
	EstPSNR      float64
	PSNRKnown    bool
	Unmet        []string
	Feasible     bool
	Reason       string
	Notes        []string
	SegmentNs    int64
	SampleFrames int
}

// Sample is the measurement input used to choose a configuration: the video
// geometry, its frame rate, and decoded luma frames from the first segment.
type Sample struct {
	Spec   VideoSpec
	Frames [][]byte
}

// Measurable reports whether a sample supports quality estimation.
func (s Sample) Measurable() bool {
	return s.Spec.Valid() && len(s.Frames) > 0 && len(s.Frames[0]) == s.Spec.Width*s.Spec.Height
}

// Choice is the measured per-frame rate-control decision for one frame group.
type Choice struct {
	Quantizers []int // per-frame quantiser in hundredths
	Payloads   [][]byte
	SSE        int64
	Bytes      int
}

// BaseQuantizer returns the representative quantiser for reporting and for
// the configuration digest. It is the most common choice in the schedule.
func (c Choice) BaseQuantizer() int {
	if len(c.Quantizers) == 0 {
		return 100
	}
	counts := make(map[int]int, 8)
	best, bestN := c.Quantizers[0], 0
	for _, q := range c.Quantizers {
		counts[q]++
		if counts[q] > bestN || (counts[q] == bestN && q < best) {
			best, bestN = q, counts[q]
		}
	}
	return best
}

// encodeFrames measures every frame at a single quantiser without retaining
// payloads; returns the byte counts and total squared error.
func encodeFrames(frames [][]byte, w, h, qMilli int) (sizes []int, sse int64, total int) {
	sizes = make([]int, len(frames))
	for i, f := range frames {
		n, frameSSE, err := QuantiseOnly(f, w, h, qMilli)
		if err != nil {
			return nil, 0, 0
		}
		sizes[i] = n
		sse += frameSSE
		total += n
	}
	return sizes, sse, total
}

// ChooseGroup performs rate control for one frame group. It first finds the
// single quantiser closest to the byte target, then mixes neighbouring
// quantisers per frame to land within 5% of the target without dropping
// frames. maxBytes (when positive) is a hard per-frame cap.
func ChooseGroup(frames [][]byte, w, h int, targetBytes, maxBytes int64) (Choice, error) {
	if len(frames) == 0 {
		return Choice{}, media.New(media.StagePlan, "rate-control", "no frames to encode")
	}
	if targetBytes < 1 {
		targetBytes = 1
	}
	lo, hi := 100, 25500
	bestQ, bestDist := 100, int64(math.MaxInt64)
	for lo <= hi {
		mid := (lo + hi) / 2
		_, _, total := encodeFrames(frames, w, h, mid)
		dist := targetBytes - int64(total)
		if dist < 0 {
			dist = -dist
		}
		if dist < bestDist {
			bestQ, bestDist = mid, dist
		}
		switch {
		case int64(total) > targetBytes:
			lo = mid + 1
		case int64(total) < targetBytes:
			hi = mid - 1
		default:
			lo, hi = mid+1, mid-1
		}
	}
	schedule := make([]int, len(frames))
	for i := range schedule {
		schedule[i] = bestQ
	}
	// Refine by mixing the quantiser with each neighbour, one frame at a
	// time. Each switch changes the aggregate by that frame's byte delta.
	for _, alt := range []int{bestQ - 1, bestQ + 1} {
		if alt < 100 || alt > 25500 {
			continue
		}
		altSizes, _, _ := encodeFrames(frames, w, h, alt)
		baseSizes, _, baseTotal := encodeFrames(frames, w, h, bestQ)
		type delta struct{ idx, diff int }
		var deltas []delta
		for i := range frames {
			if altSizes[i] != baseSizes[i] {
				deltas = append(deltas, delta{i, altSizes[i] - baseSizes[i]})
			}
		}
		// Prefer switches that move the total toward the target.
		if int64(baseTotal) < targetBytes {
			sort.SliceStable(deltas, func(a, b int) bool { return deltas[a].diff > deltas[b].diff })
		} else {
			sort.SliceStable(deltas, func(a, b int) bool { return deltas[a].diff < deltas[b].diff })
		}
		cur := int64(baseTotal)
		curDist := absI64(cur - targetBytes)
		for _, d := range deltas {
			next := cur + int64(d.diff)
			nextDist := absI64(next - targetBytes)
			if nextDist < curDist {
				schedule[d.idx] = alt
				cur, curDist = next, nextDist
			}
		}
	}
	// Materialise payloads for the final schedule.
	payloads := make([][]byte, len(frames))
	var sse int64
	total := 0
	for i, f := range frames {
		p, st, err := EncodeFrame(f, w, h, schedule[i])
		if err != nil {
			return Choice{}, err
		}
		payloads[i] = p
		sse += st.SSE
		total += st.Bytes
	}
	if maxBytes > 0 && int64(total) > maxBytes*int64(len(frames)) {
		// Cap exceeded even after refinement: coarsen the schedule until the
		// hard cap holds, or fail rather than exceed it silently.
		for pass := 0; pass < 16 && int64(total) > maxBytes*int64(len(frames)); pass++ {
			for i := range schedule {
				schedule[i] = schedule[i] * 5 / 4
				if schedule[i] > 25500 {
					schedule[i] = 25500
				}
			}
			sse, total = 0, 0
			for i, f := range frames {
				p, st, err := EncodeFrame(f, w, h, schedule[i])
				if err != nil {
					return Choice{}, err
				}
				payloads[i] = p
				sse += st.SSE
				total += st.Bytes
			}
		}
		if int64(total) > maxBytes*int64(len(frames)) {
			return Choice{}, media.New(media.StagePlan, "rate-control", "cannot satisfy max_bps cap %d, best %d", maxBytes, total/len(frames))
		}
	}
	return Choice{Quantizers: schedule, Payloads: payloads, SSE: sse, Bytes: total}, nil
}

func absI64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// EstimatePlan measures candidate quantisers on the sample and produces a
// plan that fits the target bitrate, cap and budget. When no sample is
// available the plan says so explicitly instead of inventing a PSNR.
func EstimatePlan(req Request, sample Sample, totalSegments int, segmentNs int64) (*Plan, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	if totalSegments <= 0 {
		return nil, media.New(media.StagePlan, "config", "manifest has no processable segments")
	}
	plan := &Plan{
		Request:   req,
		Spec:      sample.Spec,
		Segments:  allIndices(totalSegments),
		SegmentNs: segmentNs,
	}
	plan.Config = Config{
		Codec: CodecVersion, TargetBps: req.TargetBps, MaxBps: req.MaxBps,
		MinPSNR: req.MinPSNR, BudgetMs: req.BudgetMs,
	}

	if !sample.Measurable() {
		plan.Config.QuantizerMilli = 100
		plan.PSNRKnown = false
		plan.Unmet = append(plan.Unmet, "psnr_unmeasurable")
		plan.Notes = append(plan.Notes, "source is not an uncompressed luma stream; PSNR cannot be computed and is omitted")
		plan.Feasible = true
		return plan, nil
	}

	// Budget the sample itself first: measuring is part of the processing
	// cost, so a budget too small to cover the measured sample is reported.
	start := time.Now()
	groupTarget := sample.Spec.FrameBytes(req.TargetBps) * int64(len(sample.Frames))
	choice, err := ChooseGroup(sample.Frames, sample.Spec.Width, sample.Spec.Height, groupTarget, 0)
	if err != nil {
		return nil, err
	}
	elapsed := time.Since(start)
	plan.Config.QuantizerMilli = choice.BaseQuantizer()
	plan.SampleFrames = len(sample.Frames)
	if psnr, ok := PSNRFromSSE(choice.SSE, int64(sample.Spec.Width*sample.Spec.Height*len(sample.Frames))); ok {
		plan.PSNRKnown = true
		if math.IsInf(psnr, 1) {
			plan.EstPSNR = 99
		} else {
			plan.EstPSNR = math.Round(psnr*100) / 100
		}
	}
	sampleBps := estimateBps(int64(choice.Bytes), len(sample.Frames), sample.Spec)
	plan.EstBps = sampleBps
	if math.Abs(BitrateErrorPct(sampleBps, req.TargetBps)) > 5 {
		plan.Unmet = append(plan.Unmet, fmt.Sprintf("target_bps: measured sample rate %d bps deviates %.2f%% from %d bps",
			sampleBps, BitrateErrorPct(sampleBps, req.TargetBps), req.TargetBps))
	}
	if plan.PSNRKnown && plan.EstPSNR < req.MinPSNR {
		plan.Unmet = append(plan.Unmet, fmt.Sprintf("min_psnr: measured %.2f dB below %.2f dB at the requested rate", plan.EstPSNR, req.MinPSNR))
	}
	if sampleBps > req.MaxBps {
		plan.Unmet = append(plan.Unmet, fmt.Sprintf("max_bps: measured sample rate %d bps above cap %d bps", sampleBps, req.MaxBps))
	}

	// Budget accounting: extrapolate the measured per-frame measurement cost
	// over every source frame and adopt the longest segment prefix that fits.
	perFrame := elapsed / time.Duration(len(sample.Frames))
	fps := float64(sample.Spec.FPSNum) / float64(sample.Spec.FPSDen)
	framesPerSegment := int64(float64(segmentNs) / 1e9 * fps)
	if framesPerSegment < 1 {
		framesPerSegment = 1
	}
	budget := time.Duration(req.BudgetMs) * time.Millisecond
	var spend time.Duration
	adopted := 0
	for i := 0; i < totalSegments; i++ {
		cost := perFrame * time.Duration(framesPerSegment)
		if spend+cost > budget {
			break
		}
		spend += cost
		adopted++
	}
	if adopted == 0 {
		adopted = 1 // always process at least one segment; cost is reported
	}
	if adopted < totalSegments {
		plan.Segments = allIndices(adopted)
		plan.Deferred = allIndicesFrom(adopted, totalSegments)
		plan.Unmet = append(plan.Unmet, fmt.Sprintf("budget_ms: only %d of %d segments fit the %d ms budget", adopted, totalSegments, req.BudgetMs))
		plan.Notes = append(plan.Notes, "budget exhausted before the last segment; the report covers the adopted prefix")
	}
	plan.Feasible = len(plan.Unmet) == 0
	if !plan.Feasible {
		plan.Reason = "requested constraints cannot all be satisfied: " + joinUnmet(plan.Unmet)
	}
	return plan, nil
}

func estimateBps(bytes int64, frames int, spec VideoSpec) int64 {
	if frames <= 0 || !spec.Valid() {
		return 0
	}
	fpsNum := spec.FPSNum
	fpsDen := spec.FPSDen
	// bps = bytes*8 * fpsNum / (frames*fpsDen)
	return bytes * 8 * fpsNum / (int64(frames) * fpsDen)
}

func allIndices(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

func allIndicesFrom(start, n int) []int {
	out := make([]int, 0, n-start)
	for i := start; i < n; i++ {
		out = append(out, i)
	}
	return out
}

func joinUnmet(u []string) string {
	s := ""
	for i, v := range u {
		if i > 0 {
			s += "; "
		}
		s += v
	}
	return s
}
