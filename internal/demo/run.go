package demo

import (
	"os"
	"path/filepath"
	"time"

	"medpipe/internal/clock"
	"medpipe/internal/demux"
	"medpipe/internal/fault"
	"medpipe/internal/pipeline"
	medrtp "medpipe/internal/rtp"
	"medpipe/internal/samplegen"
)

// Run executes all demo samples and returns the JSON-serializable report.
func Run(cfg Config) *Report {
	if cfg.OutDir == "" {
		cfg.OutDir = "medpipe-output"
	}
	if cfg.Seed == 0 {
		cfg.Seed = 17
	}
	if cfg.LatencyRuns <= 0 {
		cfg.LatencyRuns = 21
	}
	faultSet := map[string]bool{}
	for _, f := range cfg.Faults {
		faultSet[f] = true
	}
	rep := &Report{
		SyncErrorMS:      -1,
		LossRecoveryRate: 1,
		P95LatencyMS:     0,
		LatencySamples:   0,
		PSIVersion:       -1,
		ExitCode:         fault.ExitOK,
		Seed:             cfg.Seed,
		Faults:           cfg.Faults,
		OutDir:           cfg.OutDir,
	}

	// Reset only medpipe-managed sample subdirectories. Never RemoveAll the
	// user-supplied output root itself.
	for _, sub := range []string{"normal", "resume", "corrupt"} {
		if err := os.RemoveAll(filepath.Join(cfg.OutDir, sub)); err != nil {
			rep.ExitCode = fault.ExitInternal
			return rep.withInternal(fault.Wrap(fault.ErrInternal, "demo.reset", 0, "reset sample directory", err))
		}
	}
	for _, sub := range []string{"normal", "resume", "corrupt"} {
		if err := os.MkdirAll(filepath.Join(cfg.OutDir, sub), 0o755); err != nil {
			rep.ExitCode = fault.ExitInternal
			return rep.withInternal(fault.Wrap(fault.ErrInternal, "demo.mkdir", 0, "create output directory", err))
		}
	}

	rules := []demux.MapRule{
		{FromPID: samplegen.VideoPID, ToIndex: 1, Enabled: true},
		{FromPID: samplegen.AudioPID, ToIndex: 2, Enabled: true},
	}
	cleanTS := buildCleanTS(cfg.Seed, faultSet)
	if err := os.WriteFile(filepath.Join(cfg.OutDir, "normal", "input.ts"), cleanTS, 0o644); err != nil {
		return rep.internal("write clean fixture", err)
	}

	receivedTS, rtpStats, netStats, rtpEvents := runRTP(cleanTS, cfg.Seed, faultSet, false)
	_ = os.WriteFile(filepath.Join(cfg.OutDir, "normal", "received.rtp-reordered.ts"), receivedTS, 0o644)
	rep.DroppedDuplicates += rtpStats.Duplicates
	normal, media, marker, fe := commitHealthy(filepath.Join(cfg.OutDir, "normal"), "normal.mp4", receivedTS, rules)
	normal.RTPStats = &rtpStats
	normal.NetworkStats = &netStats
	if fe != nil {
		rep.Normal = normal
		rep.ExitCode = fault.ExitCode(fe)
		return rep
	}
	rep.Normal = normal
	rep.OutputSHA256 = marker.OutputSHA256
	rep.PSIVersion = media.PSIVersion
	rep.SyncErrorMS = trackSyncError(media)
	rep.LossRecoveryRate = recoveryRate(netStats)
	rep.GapEvents += countGapEvents(media.Events)
	rep.EventDetails = append(rep.EventDetails, relevantEvents(media.Events)...)
	_ = rtpEvents

	p95, n, ok := measureLatency(cfg, receivedTS, rules)
	if !ok {
		rep.ExitCode = fault.ExitInternal
		return rep.withInternal(fault.New(fault.ErrInternal, "demo.latency", 0, "no latency samples"))
	}
	rep.P95LatencyMS = p95
	rep.LatencySamples = n

	resume, resumeSHA, fe := runResume(filepath.Join(cfg.OutDir, "resume"), receivedTS, rules, cfg.Resume)
	rep.Resume = resume
	if fe != nil {
		rep.ExitCode = fault.ExitCode(fe)
		return rep
	}
	if resumeSHA != rep.OutputSHA256 {
		rep.ExitCode = fault.ExitInternal
		return rep.withInternal(fault.New(fault.ErrDigestMismatch, "demo.resume", 0, "resumed output SHA differs from healthy output"))
	}

	corrupt, corruptEvents, rollbacks, cfe := runCorrupt(filepath.Join(cfg.OutDir, "corrupt"), cfg.Seed, faultSet, rules)
	rep.Corrupt = corrupt
	rep.RollbackCount += rollbacks
	rep.GapEvents += corrupt.GapEvents
	rep.EventDetails = append(rep.EventDetails, relevantEvents(corruptEvents)...)
	if cfe == nil {
		rep.ExitCode = fault.ExitInternal
		return rep.withInternal(fault.New(fault.ErrInternal, "demo.corrupt", 0, "corrupt sample was unexpectedly accepted"))
	}
	rep.Corrupt.ErrorCode = cfe.Code
	rep.Corrupt.ErrorMessage = cfe.Message
	for _, code := range corrupt.ErrorCodes {
		if code == string(cfe.Code) {
			cfe.Code = fault.Code(code)
		}
	}
	rep.ExitCode = fault.ExitCode(cfe)
	rep.OK = rep.ExitCode == fault.ExitCorrupt
	return rep
}

func buildCleanTS(seed int64, faults map[string]bool) []byte {
	if faults["psi-version"] {
		return samplegen.BuildVersionSwitchTS(seed, true)
	}
	return samplegen.BuildTS(samplegen.MediaSpec(seed))
}

func runRTP(data []byte, seed int64, faults map[string]bool, unrecovered bool) ([]byte, medrtp.Stats, samplegen.NetworkStats, []fault.Event) {
	packets := samplegen.PacketizeTS(data, uint32(seed))
	arrivals, net := samplegen.Impair(packets, seed, faults)
	if unrecovered && faults["burst-loss"] && len(packets) > 4 {
		seq := packets[4].Sequence
		var removed int
		arrivals, removed = samplegen.DropSequence(arrivals, seq)
		if removed > 0 {
			net.UnrecoveredLosses = 1
		}
	}
	out, stats, events := samplegen.ReassembleRTPAdaptive(arrivals, 128, 1_500_000, true)
	return out, stats, net, events
}

func trackSyncError(m *demux.Media) float64 {
	var video, audio []int64
	for _, t := range m.Tracks {
		times := make([]int64, len(t.Samples))
		for i, s := range t.Samples {
			times[i] = s.PTSMS
		}
		switch t.Kind {
		case "video":
			video = times
		case "audio":
			audio = times
		}
	}
	return clock.SyncError(video, audio)
}

func recoveryRate(stats samplegen.NetworkStats) float64 {
	recoverable := stats.DroppedPrimary
	if recoverable == 0 {
		return 1
	}
	return float64(stats.RedundantRecovered) / float64(recoverable)
}

func measureLatency(cfg Config, ts []byte, rules []demux.MapRule) (float64, int, bool) {
	samples := make([]time.Duration, 0, cfg.LatencyRuns)
	for i := 0; i < cfg.LatencyRuns; i++ {
		path := filepath.Join(cfg.OutDir, "latency.tmp.mp4")
		start := time.Now()
		_, _, _, err := pipeline.ProcessTSBytes(ts, path, rules)
		elapsed := time.Since(start)
		_ = os.Remove(path)
		if err != nil {
			return 0, 0, false
		}
		samples = append(samples, elapsed)
	}
	return clock.Percentile95(samples)
}

func countGapEvents(events []fault.Event) int {
	n := 0
	for _, e := range events {
		if fault.IsGapCode(e.Code) {
			n++
		}
	}
	return n
}

func relevantEvents(events []fault.Event) []fault.Event {
	var out []fault.Event
	for _, e := range events {
		if fault.IsGapCode(e.Code) || e.Code == fault.ErrBadCRC || e.Code == fault.ErrPSIVersion || e.Code == fault.ErrPCRWrap || e.Code == fault.ErrPTSWrap {
			out = append(out, e)
		}
	}
	return out
}

func (r *Report) withInternal(err *fault.Error) *Report {
	r.ExitCode = fault.ExitInternal
	if r.Normal != nil {
		r.Normal.ExitCode = fault.ExitInternal
		r.Normal.ErrorCode = err.Code
		r.Normal.ErrorMessage = err.Message
	}
	return r
}

func (r *Report) internal(stage string, err error) *Report {
	fe := fault.Wrap(fault.ErrInternal, "demo."+stage, 0, stage, err)
	return r.withInternal(fe)
}
