package main

import (
	"fmt"
	"sort"

	"mediapipe/internal/audio"
	"mediapipe/internal/clock"
	"mediapipe/internal/ingest"
	"mediapipe/internal/manifest"
	"mediapipe/internal/pipeline"
)

const (
	targetLUFS   = -23.0
	truePeakCeil = -1.0 // dBTP
)

// Report carries the acceptance metrics of one pipeline run.
type Report struct {
	SyncErrorMs        float64
	PacketRecoveryRate float64
	P95LatencyMs       float64
	Missing            int
	Recovered          int
	Unrecoverable      int
	TimelineViolations int
	RTPLost            int
	OutputSHA256       [32]byte
	RecordsComplete    bool
	FramesOK           bool
	FaultsPropagated   bool
	FaultRecords       []string
}

// processAudio streams one track's segments through resample -> channel map
// -> loudness -> true-peak, keeping resampler state across segments.
func processAudio(ts *TrackSample, records []ingest.Record) ([]pipeline.SegmentResult, error) {
	rs, err := audio.NewResampler(ts.InRate, clock.Rate, ts.InLayout.Channels())
	if err != nil {
		return nil, err
	}
	mapping, err := audio.ChannelMatrix(ts.InLayout, audio.Stereo)
	if err != nil {
		return nil, &clock.StageError{Track: ts.ID, Stage: clock.StageAudio, Op: "channel-map", Err: err}
	}
	results := make([]pipeline.SegmentResult, len(records))
	lastLive := -1
	for i, rec := range records {
		res := pipeline.SegmentResult{Record: rec}
		if rec.Status == ingest.OK || rec.Status == ingest.Recovered {
			samples := audio.S16LEToFloat(rec.Payload)
			samples = rs.Process(samples)
			samples = audio.ApplyMap(mapping, samples)
			gain := targetLUFS - audio.LoudnessLUFS(samples)
			audio.ApplyGain(samples, gain)
			audio.LimitTruePeak(samples, 2, truePeakCeil)
			res.Samples = samples
			res.Frames = int64(len(samples) / 2)
			lastLive = i
		}
		results[i] = res
	}
	// Fixed tail rule: the deferred resampler tail joins the last live slot.
	if lastLive >= 0 {
		if tail := rs.Flush(); len(tail) > 0 {
			tail = audio.ApplyMap(mapping, tail)
			results[lastLive].Samples = append(results[lastLive].Samples, tail...)
			results[lastLive].Frames += int64(len(tail) / 2)
		}
	}
	return results, nil
}

// syncErrorMs returns the worst cumulative frame drift, in milliseconds,
// over tracks whose timeline is fully intact (no missing/unrecoverable).
func syncErrorMs(playlists []*manifest.Playlist, results [][]pipeline.SegmentResult) float64 {
	var worst float64
	for ti, pl := range playlists {
		normal := true
		for _, r := range results[ti] {
			if r.Record.Status == ingest.Missing || r.Record.Status == ingest.Unrecoverable {
				normal = false
				break
			}
		}
		if !normal {
			continue
		}
		var cumExp, cumAct int64
		for i, ref := range pl.Refs {
			cumExp += int64(ref.Interval.Dur())
			cumAct += results[ti][i].Frames
			if d := cumAct - cumExp; d < 0 {
				d = -d
				if ms := float64(d) * 1000 / clock.Rate; ms > worst {
					worst = ms
				}
				continue
			} else if ms := float64(d) * 1000 / clock.Rate; ms > worst {
				worst = ms
			}
		}
	}
	return worst
}

func p95(lat []float64) float64 {
	if len(lat) == 0 {
		return 0
	}
	sort.Float64s(lat)
	idx := (95*len(lat) + 99) / 100 // ceil(0.95*n)
	return lat[idx-1]
}

func faultsPropagated(faults string, rep *Report) bool {
	if faults == "all" {
		return rep.Missing > 0 && rep.Unrecoverable > 0 && rep.RTPLost > 0
	}
	return rep.Missing == 0 && rep.Unrecoverable == 0 && rep.RTPLost == 0
}

// Print emits the acceptance metrics as key=value lines.
func (r *Report) Print() {
	fmt.Printf("sync_error_ms=%.3f\n", r.SyncErrorMs)
	fmt.Printf("packet_recovery_rate=%.4f\n", r.PacketRecoveryRate)
	fmt.Printf("p95_latency_ms=%.2f\n", r.P95LatencyMs)
	fmt.Printf("missing_segments=%d\n", r.Missing)
	fmt.Printf("recovered_segments=%d\n", r.Recovered)
	fmt.Printf("unrecoverable_segments=%d\n", r.Unrecoverable)
	fmt.Printf("timeline_violations=%d\n", r.TimelineViolations)
	fmt.Printf("rtp_lost_packets=%d\n", r.RTPLost)
	fmt.Printf("output_sha256=%x\n", r.OutputSHA256)
	for _, fr := range r.FaultRecords {
		fmt.Printf("fault_record=%s\n", fr)
	}
}

// ThresholdsOK checks the demo acceptance thresholds.
func (r *Report) ThresholdsOK() bool {
	return r.SyncErrorMs <= 20 && r.PacketRecoveryRate >= 0.90 && r.P95LatencyMs <= 2000
}

// Invariants lists the verify-mode checks and returns passed/total.
func (r *Report) Invariants(deterministic bool) (int, int) {
	checks := []struct {
		name string
		ok   bool
	}{
		{"deterministic", deterministic},
		{"records_complete", r.RecordsComplete},
		{"frames_match_manifest", r.FramesOK},
		{"no_timeline_violations", r.TimelineViolations == 0},
		{"packet_recovery_rate>=0.90", r.PacketRecoveryRate >= 0.90},
		{"sync_error_ms<=20", r.SyncErrorMs <= 20},
		{"p95_latency_ms<=2000", r.P95LatencyMs <= 2000},
		{"faults_propagated", r.FaultsPropagated},
	}
	passed := 0
	for _, c := range checks {
		if c.ok {
			passed++
		}
		fmt.Printf("invariant=%s pass=%v\n", c.name, c.ok)
	}
	return passed, len(checks)
}
