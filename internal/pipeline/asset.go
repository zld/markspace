package pipeline

import (
	"context"
	"fmt"
	"math"

	"mediaarchive/internal/align"
	"mediaarchive/internal/clock"
	"mediaarchive/internal/diag"
	"mediaarchive/internal/stream"
	"mediaarchive/internal/subtitle"
)

// assetResult is the per-asset contribution to the manifest.
type assetResult struct {
	Asset             string
	Rows              []Row
	Segments          []Segment
	Faults            []*diag.Fault
	Keyframes         int
	Samples           int64
	SampleBytes       int64
	Cues              int
	TargetBps         int64
	MediaDurationMs   float64
	MaxGapMs          float64
	AlignmentErrorMs  float64
	DriftPPM          float64
	HasDrift          bool
	Skipped           int64
	HasTruncation     bool
	TruncationStartMs float64
	FileSize          int64
	SourceSize        int64
}

// analyzeAsset processes one asset: MP4 structure, subtitles, RTP continuity,
// VFR intervals, drift and damage classification. Inputs are opened read-only
// and never executed.
func analyzeAsset(ctx context.Context, a AssetInput, opts Options) (*assetResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, ErrCanceled(0)
	}
	r := &assetResult{Asset: a.Asset, TargetBps: a.TargetBps}
	// Per-asset rate wins over the run-wide rate so one invocation can cover
	// mixed 25/29.97/50 holdings.
	fps := a.FPS
	if fps == "" {
		fps = opts.FPS
	}
	if fps == "" {
		fps = "25"
	}
	if _, err := clock.ParseRate(fps); err != nil {
		f := mustFault(err, diag.CodeAmbiguousDF)
		r.Faults = append(r.Faults, f)
		return nil, f
	}
	// A bare 30000/1001 rate is ambiguous for SMPTE labels, but only matters
	// when a label is actually interpreted. Ambiguity is therefore raised
	// lazily at the label's byte offset instead of failing every asset that
	// merely happens to be 29.97. Assets may still pre-declare a family.

	if a.MP4 != "" {
		if err := analyzeMP4Part(ctx, a, r, fps); err != nil {
			return nil, err
		}
	}
	if a.Subtitle != "" {
		if err := analyzeSubtitlePart(ctx, a, r, opts, fps); err != nil {
			return nil, err
		}
	}
	if a.RTP != "" {
		if err := analyzeRTPPart(ctx, a, r); err != nil {
			return nil, err
		}
	}
	if r.SourceSize == 0 {
		r.SourceSize = r.FileSize
	}
	r.Segments = toSegments(stream.Classify(r.Faults, stream.ClassifyOptions{
		Source: a.Asset, TimeKnown: r.MediaDurationMs > 0,
	}))
	// A truncated capture is unreliable from the cut to the declared end, so
	// collapse the MP4 segments into one window rather than citing instants.
	r.Segments = widenTruncation(r.Segments, r.HasTruncation, r.TruncationStartMs, r.MediaDurationMs, r.FileSize)
	if opts.Mode == ModeStrict && len(r.Faults) > 0 {
		return r, firstFault(r.Faults)
	}
	r.AlignmentErrorMs = maxFloat(r.AlignmentErrorMs, rowAlignmentError(r.Rows))
	kept := filterRows(r.Rows, r.Segments, opts.Mode)
	r.Skipped += int64(len(r.Rows) - len(kept))
	r.Rows = kept
	return r, nil
}

func analyzeMP4Part(ctx context.Context, a AssetInput, r *assetResult, fps string) error {
	an, err := stream.AnalyzeMP4(a.MP4)
	if err != nil {
		f := diag.New(diag.CodeMP4Structure, 0, "open %s: %v", a.MP4, err)
		r.Faults = append(r.Faults, f)
		return nil
	}
	for _, f := range an.Faults {
		// Structural MP4 faults carry byte offsets; give the tail damage a
		// media window so row filtering and alignment stay meaningful.
		if !f.HasMedia && an.HasTruncation {
			f.WithMedia(an.TruncationStartMs)
		}
	}
	r.Faults = append(r.Faults, an.Faults...)
	r.MediaDurationMs = an.DurationMs
	r.SampleBytes = an.MdatBytes
	r.HasTruncation, r.TruncationStartMs = an.HasTruncation, an.TruncationStartMs
	r.FileSize = an.Size
	r.SourceSize = an.Size
	if an.Video == nil {
		return nil
	}
	v := an.Video
	r.Keyframes = len(v.SyncSamples)
	r.Samples = int64(len(v.Samples))
	r.MaxGapMs = v.MaxKeyframeGapMs()
	if err := ctx.Err(); err != nil {
		return ErrCanceled(0)
	}
	for _, s := range v.Samples {
		if !s.Sync {
			continue
		}
		exact := clock.FromTicks(s.DTS, int64(v.Timescale))
		if s.Truncated {
			// The sample is cited by the table but its bytes are missing: record
			// an offset-bearing fault and do not fabricate a manifest row.
			f := diag.New(diag.CodeMP4Truncated, s.Offset,
				"keyframe sample %d declares %d bytes past end of file", s.Index, s.Size)
			f.WithMedia(exact.Millis())
			r.Faults = append(r.Faults, f)
			continue
		}
		r.Rows = append(r.Rows, Row{
			Asset: a.Asset, StartMs: exact.Millis(), StartUs: exact.Ticks(1_000_000),
			Seq: int64(s.Index), Stage: "frame", Kind: "keyframe",
			Payload: fmt.Sprintf("offset=%d,size=%d", s.Offset, s.Size),
		})
	}
	if v.Timescale > 0 && len(v.Samples) > 1 {
		dts := make([]int64, len(v.Samples))
		for i, s := range v.Samples {
			dts[i] = s.DTS
		}
		_, vfaults, verr := align.DeriveIntervals(dts, 0, align.VFROptions{Timescale: int64(v.Timescale)})
		if verr != nil {
			if f, ok := diag.As(verr); ok {
				r.Faults = append(r.Faults, f)
			}
		}
		// Align reports anomalies by sample index; the stream view reports the
		// same anomalies with sample byte offsets. Keep only the byte-locatable
		// form to avoid double-counting one physical defect.
		for _, f := range vfaults {
			if f.Code != diag.CodeVFRGap {
				r.Faults = append(r.Faults, f)
			}
		}
		r.Faults = append(r.Faults, v.DetectVFR(0)...)
		// Drift is measured from keyframe cadence against the declared rate.
		// A robust median of per-GOP ratios keeps isolated VFR holds from
		// masquerading as clock drift.
		if ppm, ok := keyframeDriftPPM(v, fps); ok {
			r.DriftPPM, r.HasDrift = ppm, true
		}
		// Cross-stage alignment: every manifest row must still agree with its
		// exact source time after rational-to-float conversion.
		r.AlignmentErrorMs = maxFloat(r.AlignmentErrorMs, rowAlignmentError(r.Rows))
	}
	return nil
}

// keyframeDriftPPM measures clock drift as the median relative deviation of
// real inter-keyframe spans from the span implied by the declared frame rate.
func keyframeDriftPPM(v *stream.Track, fps string) (float64, bool) {
	rate, err := clock.ParseRate(fps)
	if err != nil || rate.Float() <= 0 || v.Timescale == 0 {
		return 0, false
	}
	nominalMs := 1000 / rate.Float()
	var ratios []float64
	prev := int64(-1)
	for _, s := range v.Samples {
		if !s.Sync {
			continue
		}
		if prev >= 0 {
			gapMs := float64(s.DTS-prev) * 1000 / float64(v.Timescale)
			// Consume as many whole frames as the gap spans, so a 30-frame GOP
			// is compared to 30 nominal frames and a 24-frame GOP to 24.
			frames := math.Max(1, math.Round(gapMs/nominalMs))
			ratios = append(ratios, gapMs/(frames*nominalMs)-1)
		}
		prev = s.DTS
	}
	if len(ratios) < 2 {
		return 0, false
	}
	for i := 1; i < len(ratios); i++ {
		for j := i; j > 0 && ratios[j] < ratios[j-1]; j-- {
			ratios[j], ratios[j-1] = ratios[j-1], ratios[j]
		}
	}
	med := ratios[len(ratios)/2]
	return med * 1e6, true
}

// rowAlignmentError is the worst gap between a row's float media time and its
// exact microsecond time. It is computed after every source is merged into the
// asset so subtitle and RTP rows are covered too.
func rowAlignmentError(rows []Row) float64 {
	worst := 0.0
	for _, row := range rows {
		d := math.Abs(row.StartMs - float64(row.StartUs)/1000)
		if d > worst {
			worst = d
		}
	}
	return worst
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func analyzeSubtitlePart(ctx context.Context, a AssetInput, r *assetResult, opts Options, fps string) error {
	res, err := subtitle.ReadFile(a.Subtitle)
	if err != nil {
		f := diag.New(diag.CodeTruncated, 0, "open %s: %v", a.Subtitle, err)
		r.Faults = append(r.Faults, f)
		return nil
	}
	r.Faults = append(r.Faults, res.Faults...)
	r.Cues = len(res.Cues)
	if r.SourceSize == 0 {
		r.SourceSize = res.Bytes
	}
	for i, c := range res.Cues {
		if err := ctx.Err(); err != nil {
			return ErrCanceled(c.Offset)
		}
		media := c.Start.Millis() + a.SRTOffsetMs
		// Timecode labels inside cue text are validated against an explicit
		// label family. A bare 30000/1001 rate is ambiguous and must not be
		// silently treated as drop-frame or non-drop-frame.
		if labels := findTimecodeLabels(c.Text); len(labels) > 0 {
			format, ferr := subtitleLabelFormat(a.LabelFormat, fps, c.Offset)
			if ferr != nil {
				r.Faults = append(r.Faults, mustFault(ferr, diag.CodeAmbiguousDF))
			} else {
				for _, label := range labels {
					if _, err := clock.ParseTimecode(label, format); err != nil {
						f := mustFault(err, diag.CodeInvalidTime).AtOffset(c.Offset)
						r.Faults = append(r.Faults, f)
						break
					}
				}
			}
		}
		// A cue whose media time is outside the video duration is a mapping
		// fault with a byte offset; this connects subtitle and damage stages.
		if r.MediaDurationMs > 0 && media > r.MediaDurationMs {
			r.Faults = append(r.Faults, diag.New(diag.CodeInvalidTime, c.Offset,
				"cue %d starts at %.3f ms beyond media duration %.3f ms", i+1, media, r.MediaDurationMs).WithMedia(media))
			continue
		}
		r.Rows = append(r.Rows, Row{
			Asset: a.Asset, StartMs: media, StartUs: int64(c.Start.Micros() + a.SRTOffsetMs*1000 + 0.5),
			Seq: int64(c.Index), Stage: "subtitle", Kind: "cue",
			Payload: fmt.Sprintf("end=%.3f,%q", c.End.Millis()+a.SRTOffsetMs, oneLine(c.Text)),
		})
	}
	return nil
}

func analyzeRTPPart(ctx context.Context, a AssetInput, r *assetResult) error {
	// Streaming reader: memory stays bounded for million-packet logs.
	res, err := stream.AnalyzeRTPFile(ctx, a.RTP, stream.RTPOptions{ClockRate: 90000})
	if err != nil {
		f := mustFault(err, diag.CodeRTPSegmentGap)
		r.Faults = append(r.Faults, f)
		return nil
	}
	ra := res.Analysis
	r.Faults = append(r.Faults, ra.Faults...)
	dur := ra.DurationMs()
	if dur > r.MediaDurationMs {
		r.MediaDurationMs = dur
	}
	// Map RTP timestamps onto the unified base by unfolding 32-bit wraps and
	// dividing by the RTP clock rate, not by interpolating packet indices.
	// Packet index is only a fallback when a fault has no timestamp.
	times := RTPMediaTimes(res.Samples)
	for i := range ra.Faults {
		f := ra.Faults[i]
		if f.HasMedia || ra.Packets == 0 {
			continue
		}
		f.WithMedia(float64(f.Offset) / float64(ra.Packets) * dur)
	}
	// One manifest row per retained sample, plus a terminal row so the
	// timeline closes at the true final packet.
	for i, p := range res.Samples {
		if err := ctx.Err(); err != nil {
			return ErrCanceled(p.Offset)
		}
		tsMs := times[i]
		r.Rows = append(r.Rows, Row{
			Asset: a.Asset, StartMs: tsMs, StartUs: int64(tsMs*1000 + 0.5),
			Seq: p.Offset, Stage: "rtp", Kind: "packet", Payload: fmt.Sprintf("seq=%d", p.Seq),
		})
	}
	return nil
}

// RTPMediaTimes converts a packet sequence's raw 32-bit timestamps into
// milliseconds on the unified base. Wraps add 2^32 ticks; a backwards step
// that is not a wrap is clamped to keep the row timeline monotone, and the
// analyzer has already recorded the rollback as a fault.
func RTPMediaTimes(packets []stream.Packet) []float64 {
	out := make([]float64, len(packets))
	if len(packets) == 0 {
		return out
	}
	const clockRate = 90000.0
	var high uint64
	prev := packets[0].Timestamp
	base := uint64(prev)
	for i, p := range packets {
		if i > 0 && p.Timestamp < prev && uint64(prev)-uint64(p.Timestamp) > 1<<31 {
			high += 1 << 32
		}
		prev = p.Timestamp
		unwrapped := high + uint64(p.Timestamp)
		if unwrapped < base {
			unwrapped = base
		}
		out[i] = float64(unwrapped-base) * 1000 / clockRate
	}
	return out
}

func mustFault(err error, fallback string) *diag.Fault {
	if f, ok := diag.As(err); ok {
		return f
	}
	return diag.New(fallback, 0, "%v", err)
}

// filterRows removes rows inside unrecoverable segments in recover mode.
func filterRows(rows []Row, segs []Segment, mode Mode) []Row {
	if mode != ModeRecover || len(segs) == 0 {
		return rows
	}
	var out []Row
	for _, row := range rows {
		drop := false
		for _, s := range segs {
			if s.Source != row.Asset || s.Recoverable {
				continue
			}
			if row.StartMs >= s.StartMs && row.StartMs < s.EndMs {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, row)
		}
	}
	return out
}

func toSegments(in []stream.Segment) []Segment {
	out := make([]Segment, 0, len(in))
	for _, s := range in {
		out = append(out, Segment{
			Source: s.Source, Code: s.Code, StartMs: s.StartMs, EndMs: s.EndMs,
			StartOffset: s.StartOffset, EndOffset: s.EndOffset,
			FaultCount: s.FaultCount, Recoverable: s.Recoverable, Detail: s.Detail,
		})
	}
	return out
}

func oneLine(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] == '\n' || b[i] == '\r' || b[i] == '|' {
			b[i] = ' '
		}
	}
	return string(b)
}

// subtitleLabelFormat resolves the SMPTE family used for labels embedded in a
// subtitle. An explicit per-asset declaration wins; otherwise the rate must be
// unambiguous, or the caller gets an offset-bearing CodeAmbiguousDF fault.
func subtitleLabelFormat(declared, fps string, offset int64) (clock.Format, error) {
	if declared != "" {
		f, err := clock.ParseFormat(declared)
		if err != nil {
			return 0, diag.New(diag.CodeAmbiguousDF, offset, "declared label format %q: %v", declared, err)
		}
		return f, nil
	}
	f, err := clock.FormatFromRate(fps, offset)
	if err != nil {
		return 0, err
	}
	return f, nil
}

// findTimecodeLabels extracts HH:MM:SS:FF / HH:MM:SS;FF tokens from cue text.
func findTimecodeLabels(text string) []string {
	var out []string
	for i := 0; i+11 <= len(text); i++ {
		seg := text[i : i+11]
		if !isDigit(seg[0]) || !isDigit(seg[1]) || seg[2] != ':' ||
			!isDigit(seg[3]) || !isDigit(seg[4]) || seg[5] != ':' ||
			!isDigit(seg[6]) || !isDigit(seg[7]) || (seg[8] != ':' && seg[8] != ';') ||
			!isDigit(seg[9]) || !isDigit(seg[10]) {
			continue
		}
		if i+11 < len(text) && isDigit(text[i+11]) {
			continue
		}
		out = append(out, seg)
		i += 10
	}
	return out
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// widenTruncation replaces per-fault MP4 segments with one tail segment that
// starts at the first missing sample and ends at the declared media duration.
// `stream.Classify` guarantees non-overlap per family, so collapsing is safe.
func widenTruncation(segs []Segment, has bool, startMs, endMs float64, fileSize int64) []Segment {
	if !has {
		return segs
	}
	var out []Segment
	var tail *Segment
	for i := range segs {
		s := segs[i]
		if s.Code != "mp4" || s.Recoverable {
			out = append(out, s)
			continue
		}
		if tail == nil {
			cp := s
			cp.StartMs, cp.EndMs = startMs, endMs
			if cp.EndMs < cp.StartMs {
				cp.EndMs = cp.StartMs
			}
			cp.FaultCount = s.FaultCount
			tail = &cp
			out = append(out, cp)
			continue
		}
		if s.StartMs < tail.StartMs {
			tail.StartMs = s.StartMs
		}
		if s.EndMs > tail.EndMs {
			tail.EndMs = s.EndMs
		}
		tail.FaultCount += s.FaultCount
		tail.StartOffset = minInt64(tail.StartOffset, s.StartOffset)
		tail.EndOffset = maxInt64(tail.EndOffset, s.EndOffset)
		out[len(out)-1] = *tail
	}
	// Damage is everything from the first absent byte to EOF when the file
	// size is known, because no later byte can be recovered.
	if tail != nil && fileSize > 0 {
		if tail.EndOffset > fileSize {
			tail.EndOffset = fileSize
		}
		for i := range out {
			if out[i].Code == "mp4" && !out[i].Recoverable && out[i].StartMs == tail.StartMs {
				out[i].EndOffset = tail.EndOffset - 1
			}
		}
	}
	return out
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
