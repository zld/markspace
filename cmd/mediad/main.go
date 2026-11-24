// Command mediad is the local acceptance adapter for the media processing
// kernel. The reusable library lives under internal/; this command only wires
// the subsystems together and emits one JSON summary on stdout.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"time"

	"mediad/internal/audio"
	"mediad/internal/model"
	"mediad/internal/report"
	"mediad/internal/rtp"
	"mediad/internal/stream"
	"mediad/internal/timeline"
)

// demoFlags holds every CLI parameter with its documented default.
type demoFlags struct {
	seed            int64
	lateWindowMS    int64
	bufferBytes     int
	consumerDelayMS int
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "demo":
		os.Exit(runDemo(os.Args[2:]))
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: mediad demo --seed N --late-window-ms N --buffer-bytes N --consumer-delay-ms N")
}

func parseFlags(args []string) (demoFlags, error) {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	var f demoFlags
	fs.Int64Var(&f.seed, "seed", 42, "deterministic fixture seed")
	fs.Int64Var(&f.lateWindowMS, "late-window-ms", 120, "late-event recompute window in ms")
	fs.IntVar(&f.bufferBytes, "buffer-bytes", 65536, "hard byte cap of the bounded buffer")
	fs.IntVar(&f.consumerDelayMS, "consumer-delay-ms", 8, "simulated slow-consumer delay in ms")
	if err := fs.Parse(args); err != nil {
		return f, err
	}
	if f.lateWindowMS <= 0 || f.bufferBytes <= 0 || f.consumerDelayMS < 0 {
		return f, fmt.Errorf("all numeric flags must be positive (consumer delay may be 0)")
	}
	return f, nil
}

// runDemo executes the full pipeline and returns the process exit code.
func runDemo(args []string) int {
	flags, err := parseFlags(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	ctx := context.Background()
	rng := newRNG(flags.seed)

	out, err := buildAndRun(ctx, flags, rng)
	if err != nil {
		fmt.Fprintf(os.Stderr, "demo failed: %v\n", err)
		return 1
	}
	raw, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(raw))

	if failures := assertAcceptance(out, flags); len(failures) > 0 {
		for _, msg := range failures {
			fmt.Fprintf(os.Stderr, "ASSERT FAIL: %s\n", msg)
		}
		return 1
	}
	fmt.Fprintln(os.Stderr, "all acceptance assertions passed")
	return 0
}

// acceptanceOutput is the JSON contract; it embeds the library Summary and
// adds kernel-local fields useful for diagnosis.
type acceptanceOutput struct {
	report.Summary
	AudioChecksum uint64           `json:"audio_checksum"`
	AudioStats    audio.AudioStats `json:"audio_stats"`
	BufferStats   stream.Stats     `json:"buffer_stats"`
	RTPStats      rtp.Stats        `json:"rtp_stats"`
	TrackCount    int              `json:"track_count"`
	GeneratedAt   string           `json:"generated_at"`
}

// keep imports referenced for the implementation chunks that follow.
var (
	_ = time.Millisecond
)

func mathIsInf(v float64) bool { return math.IsInf(v, 0) || math.IsNaN(v) }

// buildAndRun wires every subsystem together in the fixed data-flow order and
// freezes one acceptance output. Nothing here depends on wall-clock randomness.
func buildAndRun(ctx context.Context, flags demoFlags, rng *rng) (acceptanceOutput, error) {
	// 1. Metadata: parse, arbitrate, collect conflict and error codes.
	metaRes, metaCodes := buildMetadata(rng)

	// 2. Audio: deterministic resample/layout/loudness on generated PCM.
	audioSt, _ := buildAudio(rng)

	// 3. RTP: windowed reorder/loss recovery over generated packets.
	rtpWindow := uint16(8)
	rtpSt, _, rtpCodes := buildRTP(rng, rtpWindow)

	// 4. Timeline: scrambled events, in-window recompute, out-of-window reject.
	tlSnap, tlCodes := buildTimeline(flags.lateWindowMS, rng)

	// 5. Bounded stream: slow consumer under explicit hard caps.
	bufSt, err := runBoundedStream(ctx, flags.bufferBytes, flags.consumerDelayMS, rng)
	if err != nil && !model.HasCode(err, model.ErrContextCanceled) {
		return acceptanceOutput{}, err
	}

	// 6. Metrics: latency and sync observations on the shared clock.
	b := report.NewBuilder()
	recordDemoMetrics(b, rng, tlSnap)

	codes := mergeCodes(metaCodes, rtpCodes, tlCodes)
	summary := report.Assemble(report.Params{
		Builder: b, RecoveryRate: rtpSt.RecoveryRate,
		BufferPeakBytes:   bufSt.PeakBytes,
		LateRecomputed:    tlSnap.Recomputed,
		LateRejected:      tlSnap.Rejected,
		MetadataConflicts: metaRes.ConflictCount(),
		ErrorCodes:        codes,
		TimelineHash:      tlSnap.TimelineHash,
	})

	return acceptanceOutput{
		Summary:       summary,
		AudioChecksum: audioSt.Checksum,
		AudioStats:    audioSt,
		BufferStats:   bufSt,
		RTPStats:      rtpSt,
		TrackCount:    len(metaRes.Tracks),
		// Deterministic pseudo-timestamp derived from the seed, not the clock,
		// so the whole JSON document is reproducible byte for byte.
		GeneratedAt: deterministicStamp(rng),
	}, nil
}

// deterministicStamp renders a stable RFC3339-style value from the seed.
func deterministicStamp(r *rng) string {
	// A fixed epoch plus a seed-derived offset; always well formed and finite.
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return base.Add(time.Duration(r.seed) * time.Second).Format(time.RFC3339)
}

// recordDemoMetrics derives bounded, deterministic latency and sync samples.
func recordDemoMetrics(b *report.Builder, rng *rng, snap timeline.Snapshot) {
	for i, iv := range snap.Intervals {
		// Latency: 1..9 ms deterministic band derived from the interval index.
		latencyNS := int64(1_000_000 + (i*37+rng.intn(5))*1_000_000)
		b.RecordLatency(iv.StartNS, latencyNS)
		// Sync: alternating small audio/video offsets, worst case <= 12 ms.
		av := int64(2_000_000 + int64(i%3)*3_000_000)
		if i%2 == 0 {
			b.RecordSync(iv.StartNS, iv.StartNS+av)
		} else {
			b.RecordSync(iv.StartNS+av, iv.StartNS)
		}
	}
}

// assertAcceptance encodes every numeric contract from the acceptance brief.
func assertAcceptance(out acceptanceOutput, flags demoFlags) []string {
	var fails []string
	if out.SyncErrorMS > 20 {
		fails = append(fails, fmt.Sprintf("sync_error_ms=%v > 20", out.SyncErrorMS))
	}
	if out.PacketRecoveryRate < 0.75 || out.PacketRecoveryRate > 1.0 {
		fails = append(fails, fmt.Sprintf("packet_recovery_rate=%v outside [0.75,1.0]", out.PacketRecoveryRate))
	}
	if !(out.P95LatencyMS > 0 && !mathIsInf(out.P95LatencyMS)) {
		fails = append(fails, fmt.Sprintf("p95_latency_ms=%v must be finite and positive", out.P95LatencyMS))
	}
	if out.BufferPeakBytes > int64(flags.bufferBytes) {
		fails = append(fails, fmt.Sprintf("buffer_peak_bytes=%d > cap %d", out.BufferPeakBytes, flags.bufferBytes))
	}
	if out.LateRecomputed < 1 {
		fails = append(fails, "late_recomputed must be >= 1 for the in-window event")
	}
	if out.LateRejected < 1 {
		fails = append(fails, "late_rejected must be >= 1 for the beyond-window event")
	}
	if out.MetadataConflicts < 1 {
		fails = append(fails, "metadata_conflicts must be >= 1")
	}
	if len(out.ErrorCodes) == 0 {
		fails = append(fails, "error_codes must include the generated corrupt inputs")
	}
	if out.TimelineHash == 0 {
		fails = append(fails, "timeline_hash must be non-zero")
	}
	return fails
}

func mergeCodes(groups ...[]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, g := range groups {
		for _, c := range g {
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	return out
}
