package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"archivecheck/internal/asset"
	"archivecheck/internal/fault"
	"archivecheck/internal/manifest"
	"archivecheck/internal/pipeline"
	"archivecheck/internal/stream"
	"archivecheck/internal/subtitle"
	"archivecheck/internal/timebase"
	"archivecheck/internal/timecode"
)

// collectStructure analyses the MP4 and RTP sources and records tracks,
// damage spans and time ranges.
func collectStructure(res *analysis, byName map[string]*sample) error {
	for _, name := range []string{"clip-valid.mp4", "clip-truncated.mp4"} {
		src := byName["samples/"+name].source
		st := stream.AnalyzeMP4(src)
		res.mp4[name] = st
		for _, span := range st.Damaged {
			res.damage = append(res.damage, manifest.Damage{
				Source: src.Name, Start: span.Start, End: span.End, Kind: span.Kind, Detail: span.Detail,
			})
		}
		for i, tr := range st.Tracks {
			res.tracks = append(res.tracks, manifest.TrackRef{
				Source: src.Name, Index: i, ID: tr.ID, Handler: tr.Handler, Timescale: tr.Timescale,
				DurationNanos: tr.DurationNanos, SampleCount: tr.SampleCount, SyncSamples: tr.SyncSamples,
				IntervalNanos: tr.Interval,
			})
			if len(tr.Keyframes) > 0 {
				res.ranges = append(res.ranges, manifest.TimeRange{
					Source: src.Name, Track: "track-" + fmt.Sprint(tr.ID),
					Start: tr.Keyframes[0].Time, End: tr.Keyframes[0].Time + tr.DurationNanos,
				})
			}
		}
	}
	rtpSrc := byName["samples/capture-loss.rtp"].source
	res.rtp = stream.AnalyzeRTP(rtpSrc)
	for _, span := range res.rtp.Damaged {
		res.damage = append(res.damage, manifest.Damage{
			Source: rtpSrc.Name, Start: span.Start, End: span.End, Kind: span.Kind, Detail: span.Detail,
		})
	}
	for _, gap := range res.rtp.Gaps {
		res.damage = append(res.damage, manifest.Damage{
			Source: rtpSrc.Name, Start: gap.Offset, End: gap.Offset + 4, Kind: "sequence-gap",
			Detail: fmt.Sprintf("expected %d, got %d, missing %d", gap.Expected, gap.Got, gap.Missing),
		})
	}
	if res.rtp.Duration > 0 {
		res.ranges = append(res.ranges, manifest.TimeRange{
			Source: rtpSrc.Name, Start: res.rtp.FirstTime, End: res.rtp.LastTime,
		})
	}
	return nil
}

// collectSubtitles parses both subtitle sources, recording cue ranges and
// every recoverable problem as a damaged byte interval.
func collectSubtitles(res *analysis, byName map[string]*sample) {
	res.srt = subtitle.Parse(byName["samples/subs-recoverable.srt"].source.Name, byName["samples/subs-recoverable.srt"].source.Bytes())
	res.vtt = subtitle.Parse(byName["samples/subs-valid.vtt"].source.Name, byName["samples/subs-valid.vtt"].source.Bytes())
	for _, tr := range []*subtitle.Track{res.srt, res.vtt} {
		if rr, ok := cueRange(tr); ok {
			res.ranges = append(res.ranges, rr)
		}
		for _, p := range tr.Problems {
			fe := faultOf(p.Err)
			res.damage = append(res.damage, manifest.Damage{
				Source: tr.Source, Start: p.Offset, End: p.End, Kind: fe.Kind, Detail: fe.Detail,
			})
		}
	}
}

// collectTimecodes parses each row of the generated timecode table with the
// multi-format parser, keeping successes and rejections apart.
func collectTimecodes(res *analysis, byName map[string]*sample) {
	codes := byName["samples/timecodes.txt"].source
	var offset int64
	for _, line := range strings.Split(string(codes.Bytes()), "\n") {
		lineOffset := offset
		offset += int64(len(line)) + 1
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		parts := strings.SplitN(trimmed, "|", 3)
		if len(parts) != 3 {
			res.bad = append(res.bad, newFault(lineOffset, "malformed timecode row"))
			continue
		}
		rate, ok := timecode.RateFor(parts[1])
		if !ok {
			res.bad = append(res.bad, newFault(lineOffset, "unknown rate "+parts[1]))
			continue
		}
		v, err := timecode.ParseAt(parts[2], rate, lineOffset+int64(strings.Index(line, parts[2])))
		if err != nil {
			res.bad = append(res.bad, err)
			continue
		}
		res.codes = append(res.codes, v)
	}
}

// buildArchive writes the derivative store and the manifest, then rebuilds
// from the same sources to report reuse. It returns the manifest and the
// counts of the repeat run.
func buildArchive(out io.Writer, dir string, samples []*sample, res *analysis, cfg pipeline.Config) (*manifest.Manifest, manifest.Counts, error) {
	first, err := assemble(dir, samples, res, cfg)
	if err != nil {
		return nil, manifest.Counts{}, err
	}
	sum, err := first.Checksum()
	if err != nil {
		return nil, manifest.Counts{}, err
	}
	fmt.Fprintln(out, "== manifest ==============================================")
	fmt.Fprintf(out, "  %s\n", first.Summary())
	fmt.Fprintf(out, "  counts: added=%d reused=%d changed=%d invalidated=%d\n",
		first.Counts.Added, first.Counts.Reused, first.Counts.Changed, first.Counts.Invalidated)
	fmt.Fprintf(out, "  checksum: %s\n", sum)
	for _, d := range first.Derivatives {
		fmt.Fprintf(out, "  derivative layer=%d kind=%-18s digest=%s deps=%d\n", d.Layer, d.Kind, short(d.Digest), len(d.Deps))
	}

	repeatManifest, err := assemble(dir, samples, res, cfg)
	if err != nil {
		return nil, manifest.Counts{}, err
	}
	fmt.Fprintf(out, "  repeat run: added=%d reused=%d changed=%d invalidated=%d\n",
		repeatManifest.Counts.Added, repeatManifest.Counts.Reused, repeatManifest.Counts.Changed, repeatManifest.Counts.Invalidated)
	repeatSum, err := repeatManifest.Checksum()
	if err != nil {
		return nil, manifest.Counts{}, err
	}
	fmt.Fprintf(out, "  repeat checksum: %s\n\n", repeatSum)
	if repeatSum != sum {
		return nil, manifest.Counts{}, fmt.Errorf("manifest checksum changed between identical runs")
	}
	// The repeat run rewrote manifest.json with reuse counts; verify that copy.
	return repeatManifest, repeatManifest.Counts, nil
}

// assemble runs one complete build into dir.
func assemble(dir string, samples []*sample, res *analysis, cfg pipeline.Config) (*manifest.Manifest, error) {
	b := manifest.NewBuilder(dir)
	for _, s := range samples {
		if err := b.AddSource(s.source); err != nil {
			return nil, err
		}
	}
	for _, t := range res.tracks {
		b.AddTrack(t)
	}
	for _, d := range res.damage {
		b.AddDamage(d)
	}
	for _, r := range res.ranges {
		b.AddTimeRange(r)
	}

	// Layer 1: one normalised artefact per source.
	var layer1 []string
	for _, s := range samples {
		kind, payload := layerOne(s, res)
		deriv, err := b.AddDerivative(manifest.DerivativeSpec{
			Kind: kind, Layer: 1, Data: payload, Deps: []string{s.source.Digest},
		})
		if err != nil {
			return nil, err
		}
		layer1 = append(layer1, deriv.Digest)
	}
	// Layer 2: one archive bundle over every layer-1 artefact.
	bundle := map[string]any{
		"chunkSize": cfg.ChunkSize,
		"queueSize": cfg.QueueSize,
		"tracks":    len(res.tracks),
		"damage":    len(res.damage),
		"ranges":    len(res.ranges),
		"codes":     len(res.codes),
		"rejected":  len(res.bad),
	}
	payload, err := json.Marshal(bundle)
	if err != nil {
		return nil, err
	}
	if _, err := b.AddDerivative(manifest.DerivativeSpec{Kind: "archive-bundle", Layer: 2, Data: payload, Deps: layer1}); err != nil {
		return nil, err
	}
	return b.Build()
}

// layerOne renders the normalised artefact for a source.
func layerOne(s *sample, res *analysis) (string, []byte) {
	name := filepath.Base(s.source.Name)
	switch {
	case strings.HasSuffix(name, ".mp4"):
		st := res.mp4[name]
		return "stream-structure", mustJSON(map[string]any{
			"source": s.source.Name, "boxes": st.BoxCount, "brand": st.Brand,
			"tracks": st.Tracks, "damage": st.Damaged,
		})
	case strings.HasSuffix(name, ".rtp"):
		return "rtp-analysis", mustJSON(map[string]any{
			"source": s.source.Name, "packets": res.rtp.Packets, "lost": res.rtp.Lost,
			"gaps": res.rtp.Gaps, "durationNanos": int64(res.rtp.Duration),
		})
	case strings.HasSuffix(name, ".srt"):
		return "subtitle-track", mustJSON(res.srt)
	case strings.HasSuffix(name, ".vtt"):
		return "subtitle-track", mustJSON(res.vtt)
	case strings.HasSuffix(name, ".txt"):
		return "timecode-table", mustJSON(map[string]any{
			"source": s.source.Name, "valid": res.codes, "rejected": len(res.bad),
		})
	}
	return "source-scan", mustJSON(map[string]any{"source": s.source.Name, "bytes": s.source.Size})
}

func mustJSON(v any) []byte {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(fmt.Sprintf("archivecheck: marshal derivative: %v", err))
	}
	return data
}

// printAnalysis renders structure, subtitle and timecode findings.
func printAnalysis(out io.Writer, res *analysis) {
	fmt.Fprintln(out, "== structure =============================================")
	for _, name := range []string{"clip-valid.mp4", "clip-truncated.mp4"} {
		st := res.mp4[name]
		fmt.Fprintf(out, "  %-20s boxes=%d brand=%s damage=%d\n", name, st.BoxCount, st.Brand, len(st.Damaged))
		for _, span := range st.Damaged {
			fmt.Fprintf(out, "    damaged offset=%d..%d kind=%s %s\n", span.Start, span.End, span.Kind, span.Detail)
		}
		for _, tr := range st.Tracks {
			fmt.Fprintf(out, "    track %d handler=%s samples=%d keyframes=%d mean interval=%s\n",
				tr.ID, tr.Handler, tr.SampleCount, len(tr.Keyframes), time.Duration(tr.Interval))
			if len(tr.Keyframes) > 0 {
				fmt.Fprintf(out, "      keyframes=%s\n", keyOffsets(tr.Keyframes))
			}
			if len(tr.Intervals) > 0 {
				fmt.Fprintf(out, "      intervals=%s\n", durations(tr.Intervals))
			}
		}
	}
	fmt.Fprintf(out, "  %-20s packets=%d lost=%d gaps=%d duration=%s\n",
		"capture-loss.rtp", res.rtp.Packets, res.rtp.Lost, len(res.rtp.Gaps), time.Duration(res.rtp.Duration))
	for _, gap := range res.rtp.Gaps {
		fmt.Fprintf(out, "    gap expected=%d got=%d missing=%d at byte %d\n", gap.Expected, gap.Got, gap.Missing, gap.Offset)
	}
	fmt.Fprintln(out)

	fmt.Fprintln(out, "== subtitles ==============================================")
	for _, tr := range []*subtitle.Track{res.srt, res.vtt} {
		fmt.Fprintf(out, "  %-24s format=%s cues=%d problems=%d\n", filepath.Base(tr.Source), tr.Format, len(tr.Cues), len(tr.Problems))
		for _, c := range tr.Cues {
			fmt.Fprintf(out, "    cue %d id=%q %s..%s lines=%d offset=%d\n", c.Index, c.ID, ms(c.Start), ms(c.End), len(c.Lines), c.Offset)
		}
		for _, p := range tr.Problems {
			fe := faultOf(p.Err)
			fmt.Fprintf(out, "    problem offset=%d kind=%s %s\n", p.Offset, fe.Kind, fe.Detail)
		}
	}
	fmt.Fprintln(out)

	fmt.Fprintln(out, "== timecodes ==============================================")
	for _, v := range res.codes {
		fmt.Fprintf(out, "  %-10s frames=%-8d nanos=%d\n", v.Text, v.Frames, int64(v.Nanos))
	}
	for _, err := range res.bad {
		fe := faultOf(err)
		fmt.Fprintf(out, "  rejected offset=%d %s/%s %s\n", fe.Offset, fe.Stage, fe.Kind, fe.Detail)
	}
	fmt.Fprintln(out)
}

// printPipeline renders stage status, resume boundaries and the shared cause.
func printPipeline(out io.Writer, res *analysis) {
	fmt.Fprintln(out, "== stage status ==========================================")
	for i, rep := range res.reports {
		fmt.Fprintf(out, "  run %d: %s\n", i+1, stageLine(rep))
		if cause := rep.Cause(); cause != nil {
			fmt.Fprintf(out, "    shared cause: %v\n", cause)
		}
		fmt.Fprintf(out, "    peak outstanding=%d ceiling=%d backpressure waits=%d\n",
			rep.PeakOutstanding, rep.OutstandingCeiling, rep.BackpressureWaits)
	}
	if len(res.resumeStages) > 0 {
		fmt.Fprintln(out, "  checkpoint resume state:")
		for _, st := range res.resumeStages {
			fmt.Fprintf(out, "    stage=%-6s status=%s offset=%d chunks=%d bytes=%d digest=%s\n",
				st.Name, st.Status, st.Offset, st.Chunks, st.Bytes, short(st.Digest))
		}
	}
	fmt.Fprintf(out, "  cancellation reason: %s\n", res.cancelReason)
	fmt.Fprintf(out, "  resume byte offset: %d\n", res.resumeOffset)
	if !res.pipelineHealthy {
		fmt.Fprintln(out, "  WARNING: bounded-memory expectations were not met")
	}
	fmt.Fprintln(out)
}

func keyOffsets(kfs []stream.Keyframe) string {
	parts := make([]string, 0, len(kfs))
	for _, k := range kfs {
		parts = append(parts, fmt.Sprintf("#%d@%d", k.Number, k.Offset))
	}
	return strings.Join(parts, " ")
}

func durations(ns []timebase.Nanos) string {
	parts := make([]string, 0, len(ns))
	for _, n := range ns {
		parts = append(parts, time.Duration(n).String())
	}
	return strings.Join(parts, " ")
}

// verifyManifest and the stored-checksum helpers live here so both commands
// share one implementation.
func verifyManifest(dir string) (*manifest.Manifest, error) {
	m, err := manifest.Verify(dir)
	if err != nil {
		return nil, err
	}
	return m, nil
}

// faultOf narrows an error to the shared fault model.
func faultOf(err error) *fault.Err {
	if fe := fault.As(err); fe != nil {
		return fe
	}
	return fault.New(fault.StageDemo, fault.KindCorrupt, "", 0, "%v", err)
}

func newFault(offset int64, format string, args ...any) error {
	return fault.New(fault.StageTimecode, fault.KindInvalid, "timecodes.txt", offset, format, args...)
}

// writeFileAtomic writes data through the shared atomic-write helper.
func writeFileAtomic(path string, data []byte) (string, error) {
	return asset.WriteAtomic(path, data)
}

func loadStoredChecksum(stateDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(stateDir, "manifest-checksum.txt"))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func saveStoredChecksum(stateDir, sum string) error {
	path := filepath.Join(stateDir, "manifest-checksum.txt")
	_, err := writeFileAtomic(path, []byte(sum+"\n"))
	return err
}
