package main

import (
	"fmt"
	"os"
	"path/filepath"

	"continuity/internal/damage"
	"continuity/internal/manifest"
	"continuity/internal/manifest/provenance"
	"continuity/internal/pipeline"
	"continuity/internal/timeline"
)

func trackReport(track, kind string, timescale, durTicks, target, actual int64) manifest.TrackReport {
	var dev int64
	if target > 0 {
		dev = (actual - target) * 10000 / target // hundredths of a percent
	}
	return manifest.TrackReport{
		Track: track, Kind: kind, Timescale: timescale,
		DurationTicks: durTicks, TargetBitrate: target, ActualBitrate: actual,
		BitrateDeviation: fmt.Sprintf("%+.2f%%", float64(dev)/100),
	}
}

func buildManifest(cfg config, scanPath, damagePath, keyframePath, rtpPath, mp4Path string) error {
	var s scanOut
	if err := readJSON(scanPath, &s); err != nil {
		return err
	}
	var dmg []timeline.Range
	if err := readJSON(damagePath, &dmg); err != nil {
		return err
	}
	var kfs []keyframeOut
	if err := readJSON(keyframePath, &kfs); err != nil {
		return err
	}
	unified := timeline.Clock{Timescale: unifiedTimescale}
	m := &manifest.Manifest{UnifiedTimescale: unifiedTimescale}

	var actualAudio int64
	if s.ExpectedTicks > 0 {
		actualAudio = s.ReceivedBits * unifiedTimescale / s.ExpectedTicks
	}
	m.Tracks = append(m.Tracks, trackReport(
		"audio/rtp", "audio", unifiedTimescale, s.ExpectedTicks, cfg.AudioTarget, actualAudio))

	var alignMax int64
	for _, tr := range s.MP4.Tracks {
		dur := tr.Duration()
		durU, err := tr.Clock.Convert(dur, unified)
		if err != nil {
			return err
		}
		var actual int64
		if dur > 0 {
			actual = tr.TotalBytes() * 8 * tr.Clock.Timescale / int64(dur)
		}
		target := cfg.AudioTarget
		if tr.Kind == "video" {
			target = cfg.VideoTarget
		}
		name := fmt.Sprintf("%s/mp4#%d", tr.Kind, tr.ID)
		m.Tracks = append(m.Tracks, trackReport(
			name, tr.Kind, tr.Clock.Timescale, int64(durU), target, actual))
		// Alignment error: worst clock round-trip drift at sample boundaries.
		var start timeline.Ticks
		for _, sm := range tr.Samples {
			u, err := tr.Clock.Convert(start, unified)
			if err != nil {
				return err
			}
			back, err := unified.Convert(u, tr.Clock)
			if err != nil {
				return err
			}
			if d := int64(back - start); d < 0 {
				if -d > alignMax {
					alignMax = -d
				}
			} else if d > alignMax {
				alignMax = d
			}
			start += sm.Duration
		}
	}
	m.AlignmentErrorTicks = alignMax

	for _, kfo := range kfs {
		var vclk timeline.Clock
		for _, tr := range s.MP4.Tracks {
			if fmt.Sprintf("%s/mp4#%d", tr.Kind, tr.ID) == kfo.Track {
				vclk = tr.Clock
			}
		}
		for _, iv := range kfo.Intervals {
			st, err := vclk.Convert(iv.Start, unified)
			if err != nil {
				return err
			}
			en, err := vclk.Convert(iv.End, unified)
			if err != nil {
				return err
			}
			m.Keyframes = append(m.Keyframes, manifest.KeyframeReport{
				Track: kfo.Track, StartSample: iv.StartSample, EndSample: iv.EndSample,
				StartTicks: int64(st), EndTicks: int64(en), LengthTicks: int64(en - st),
				TooLong: iv.Length() > timeline.Ticks(cfg.MaxKeyframeTicks),
			})
		}
	}

	all := append([]timeline.Range(nil), dmg...)
	for _, kfo := range kfs {
		all = append(all, kfo.Long...)
	}
	m.Damage = damage.Merge(all)

	expectedPkts := int64(len(s.RTP.Packets))
	for _, g := range s.RTP.Gaps {
		expectedPkts += int64(g.EndSeq-g.StartSeq) + 1
	}
	q := manifest.QualityMetrics{
		Basis:      "structural: RTP sequence/Opus TOC durations and MP4 sample tables; no psychoacoustic decode",
		Computable: len(s.RTP.Packets) > 0,
		Duplicates: s.RTP.Duplicates,
		GapCount:   len(s.RTP.Gaps),
	}
	if !q.Computable {
		q.Note = "no decodable audio packets; quality metrics not computable"
	}
	if expectedPkts > 0 {
		q.PacketLossPPM = (expectedPkts - int64(len(s.RTP.Packets))) * 1000000 / expectedPkts
	}
	if s.ExpectedTicks > 0 {
		q.CoveragePPM = s.CoverageTicks * 1000000 / s.ExpectedTicks
	}
	for _, g := range s.RTP.Gaps {
		q.GapTicks += (int64(g.EndSeq-g.StartSeq) + 1) * s.SamplesPerPkt
	}
	m.Quality = q

	rtpSum, err := provenance.SumFile(rtpPath)
	if err != nil {
		return err
	}
	mp4Sum, err := provenance.SumFile(mp4Path)
	if err != nil {
		return err
	}
	m.Provenance.InputSHA256 = map[string]string{"input.rtp": rtpSum, "input.mp4": mp4Sum}
	m.Provenance.StepSHA256 = map[string]string{"fixtures": provenance.Combine(rtpSum, mp4Sum)}
	for id, p := range map[string]string{
		"scan": scanPath, "damage": damagePath, "keyframe": keyframePath,
	} {
		h, err := provenance.SumFile(p)
		if err != nil {
			return err
		}
		m.Provenance.StepSHA256[id] = h
	}

	_, err = m.Write(filepath.Join(cfg.Out, "manifest.json"))
	return err
}

func printSummary(cfg config, results map[string]pipeline.Result) int {
	manifestPath := filepath.Join(cfg.Out, "manifest.json")
	var m manifest.Manifest
	if err := readJSON(manifestPath, &m); err != nil {
		fmt.Fprintln(os.Stderr, "continuity:", err)
		return 1
	}
	sum, err := provenance.SumFile(manifestPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "continuity:", err)
		return 1
	}
	fmt.Println("== continuity demo summary ==")
	fmt.Print("steps:")
	for _, id := range []string{"fixtures", "scan", "damage", "keyframe", "manifest"} {
		fmt.Printf(" %s=%s", id, results[id].Status)
	}
	fmt.Println()
	for _, t := range m.Tracks {
		fmt.Printf("track %-12s target=%d bps actual=%d bps (%s)\n",
			t.Track, t.TargetBitrate, t.ActualBitrate, t.BitrateDeviation)
	}
	fmt.Printf("quality basis: %s\n", m.Quality.Basis)
	if !m.Quality.Computable {
		fmt.Printf("quality: not computable (%s)\n", m.Quality.Note)
	} else {
		fmt.Printf("quality: loss=%d ppm duplicates=%d gaps=%d gapTicks=%d coverage=%d ppm\n",
			m.Quality.PacketLossPPM, m.Quality.Duplicates, m.Quality.GapCount,
			m.Quality.GapTicks, m.Quality.CoveragePPM)
	}
	fmt.Printf("alignment error: %d ticks (limit 1 audio sample tick)\n", m.AlignmentErrorTicks)
	fmt.Printf("damage ranges: %d\n", len(m.Damage))
	for _, r := range m.Damage {
		fmt.Printf("  %s [%s] seq[%d-%d] off[%d-%d] ticks[%d-%d]\n",
			r.Track, r.Reason, r.StartSeq, r.EndSeq, r.StartOffset, r.EndOffset, r.Start, r.End)
	}
	fmt.Printf("manifest: %s\nsha256: %s\n", manifestPath, sum)
	if len(m.Damage) > 0 {
		fmt.Println("exit: 2 (damaged input; report generated, no audio fabricated)")
		return 2
	}
	fmt.Println("exit: 0 (complete)")
	return 0
}
