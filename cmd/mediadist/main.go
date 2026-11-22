// Command mediadist is the terminal acceptance adapter for the media library.
// It generates a seeded local fixture, runs the full pipeline, prints the
// evidence a reviewer needs, and exits with a status that distinguishes input
// errors from unmet thresholds.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"mediadist/internal/fixture"
	"mediadist/internal/pipeline"
	"mediadist/internal/track"
)

// Exit codes are part of the contract with the acceptance script.
const (
	exitOK        = 0
	exitInput     = 1
	exitThreshold = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is separated from main so tests can drive it without spawning a process.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: mediadist demo [flags]")
		return exitInput
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printUsage(stdout)
		return exitOK
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		printUsage(stderr)
		return exitInput
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, `mediadist demo [flags]

Generates a seeded local fixture (RTP over H.264/H.265/AV1), runs the media
pipeline, and prints acceptance metrics.

Flags:
  --seed N            fixture seed (default 41)
  --loss-burst N      consecutive packets lost per burst (default 3)
  --bandwidth-kbps N  nominal bandwidth used for buffering hints (default 1800)
  --segment-ms N      unified timeline segment length (default 2000)
  --out DIR           output directory (default: new temp dir)
  --unknown-tracks M  "error" or "drop" (default error)
  --strict-transport  reject recoverable RTP header oddities`)
}

func runDemo(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	seed := fs.Int64("seed", 41, "fixture seed")
	lossBurst := fs.Int("loss-burst", 3, "consecutive packets lost per burst")
	kbps := fs.Int("bandwidth-kbps", 1800, "nominal bandwidth in kbps")
	segmentMS := fs.Int("segment-ms", 2000, "segment length in milliseconds")
	outDir := fs.String("out", "", "output directory (default: temporary)")
	unknown := fs.String("unknown-tracks", "error", "unknown-track policy: error or drop")
	strict := fs.Bool("strict-transport", false, "reject recoverable RTP header oddities")
	if err := fs.Parse(args); err != nil {
		return exitInput
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "unexpected arguments: %v\n", fs.Args())
		return exitInput
	}
	policy, err := track.ParseUnknownPolicy(*unknown)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitInput
	}
	if *lossBurst < 0 || *kbps <= 0 || *segmentMS <= 0 || *seed < 0 {
		fmt.Fprintln(stderr, "invalid parameters: seed>=0, loss-burst>=0, bandwidth-kbps>0, segment-ms>0")
		return exitInput
	}

	// Without --out the run still needs a writable directory. It is kept so a
	// reviewer can inspect the MP4 and JSON report; the path is printed.
	dir := *outDir
	if dir == "" {
		tmp, err := os.MkdirTemp("", "mediadist-demo-")
		if err != nil {
			fmt.Fprintf(stderr, "cannot create output directory: %v\n", err)
			return exitInput
		}
		dir = tmp
	} else if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(stderr, "cannot create output directory: %v\n", err)
		return exitInput
	}

	fx, err := fixture.Generate(fixture.Spec{
		Seed: *seed, SegmentMS: *segmentMS, LossBurst: *lossBurst,
		BandwidthKbps: *kbps,
	})
	if err != nil {
		fmt.Fprintf(stderr, "fixture error: %v\n", err)
		return exitInput
	}
	in := toInput(fx)
	// The manifest is what the operator asked to forward. The AV1 track is
	// deliberately left out so the unknown-track policy has a real subject.
	if *unknown == "drop" {
		in.Manifest = []uint32{1, 2}
	}
	opts := pipeline.DefaultOptions()
	opts.Seed = *seed
	opts.LossBurst = *lossBurst
	opts.BandwidthKbps = *kbps
	opts.SegmentMS = *segmentMS
	opts.OutputDir = dir
	opts.UnknownTracks = policy
	opts.StrictTransport = *strict

	res := pipeline.Run(in, opts)
	if res.Fatal != nil {
		fmt.Fprintf(stderr, "run failed: %v\n", res.Fatal)
		return exitInput
	}
	printResult(stdout, res, fx, dir)

	if !res.Metrics.Pass(pipeline.DefaultThresholds()) {
		fmt.Fprintln(stdout, "thresholds_met=false")
		return exitThreshold
	}
	fmt.Fprintln(stdout, "thresholds_met=true")
	return exitOK
}

// toInput adapts a generated fixture to the pipeline's input view.
func toInput(fx *fixture.Fixture) *pipeline.Input {
	in := &pipeline.Input{IndexJSON: fx.IndexJSON}
	for _, st := range fx.Streams {
		ps := &pipeline.InputStream{
			Name: st.Name, Codec: st.Codec, TrackID: st.TrackID,
			Timescale: st.Timescale, Origin: st.Origin,
			Datagrams: st.Datagrams, Repairs: st.Repairs,
		}
		for _, u := range st.AccessUnits {
			ps.CleanSamples = append(ps.CleanSamples, pipeline.CleanSample{
				Timestamp: u.Timestamp, Keyframe: u.Keyframe, Data: u.Data,
			})
		}
		in.Streams = append(in.Streams, ps)
	}
	return in
}

// printResult renders the evidence blocks the acceptance script greps for.
func printResult(w io.Writer, res *pipeline.RunResult, fx *fixture.Fixture, dir string) {
	fmt.Fprintf(w, "output_dir=%s\n", dir)

	fmt.Fprintln(w, "== tracks ==")
	for _, tr := range res.Tracks {
		fmt.Fprintf(w, "track=%d input=%d codec=%s samples=%d keyframes=%d timescale=%d\n",
			tr.TrackID, tr.InputID, tr.Codec, tr.Samples, tr.Keyframes, tr.Timescale)
	}
	fmt.Fprintf(w, "mapping=%s\n", res.Mapping)

	fmt.Fprintln(w, "== streams ==")
	for _, s := range res.Streams {
		fmt.Fprintf(w, "stream=%s(%d) codec=%s samples=%d keyframes=%d damaged_samples=%d "+
			"received=%d committed=%d duplicates=%d reordered=%d late=%d lost=%d recovered=%d "+
			"lost_bursts=%d recovered_bursts=%d damaged_spans=%d origin_ns=%d\n",
			s.Name, s.Stream, s.Codec, s.Samples, s.Keyframes, s.DamagedSamples,
			s.Received, s.Committed, s.Duplicates, s.Reordered, s.Late,
			s.LostSequences, s.RecoveredSeqs, s.LostBursts, s.RecoveredBursts,
			s.DamagedSpans, s.OriginNS)
	}

	fmt.Fprintln(w, "== segments ==")
	for _, seg := range res.Segments {
		fmt.Fprintf(w, "segment=%d start_ns=%d end_ns=%d samples=%d bytes=%d lost=%d "+
			"keyframes=%d rate_bps=%d buffer_cap=%d sha256=%s\n",
			seg.Index, seg.StartNS, seg.EndNS, seg.Samples, seg.Bytes, seg.Lost,
			seg.Keyframes, seg.RateBps, seg.BufferCap, seg.SHA256)
	}

	fmt.Fprintln(w, "== index ==")
	fmt.Fprintf(w, "index_total=%d matched=%d mismatches=%d rebuilt=%t\n",
		res.Index.Total, res.Index.Matched, res.Index.Mismatches, res.Index.Rebuilt)
	if res.Index.Reason != "" {
		fmt.Fprintf(w, "index_rebuild_reason=%q\n", res.Index.Reason)
	}

	fmt.Fprintln(w, "== faults ==")
	f := fx.Faults
	fmt.Fprintf(w, "fault_plan duplicates=%d reordered=%d loss_bursts=%d lost_packets=%d "+
		"repairs=%d truncated=%d bad_index=%d\n",
		f.Duplicates, f.Reordered, f.LossBursts, f.LostPackets, f.Repairs, f.Truncated, f.BadIndex)

	fmt.Fprintln(w, "== errors ==")
	if len(res.Errors) == 0 {
		fmt.Fprintln(w, "no recoverable errors")
	}
	for _, e := range res.Errors {
		fmt.Fprintf(w, "error=%s\n", e.Error())
	}

	if res.Remux != nil {
		fmt.Fprintf(w, "mp4=sample_count=%d size=%d sha256=%s\n",
			res.Remux.SampleCount, res.Remux.Size, res.Remux.SHA256)
	}

	m := res.Metrics
	fmt.Fprintln(w, "== metrics ==")
	fmt.Fprintf(w, "roundtrip_match=%d/%d\n", m.RoundtripMatch, m.RoundtripTotal)
	fmt.Fprintf(w, "roundtrip_rate=%.4f\n", m.RoundtripRate())
	fmt.Fprintf(w, "keyframe_hit=%d/%d\n", m.KeyframeHit, m.KeyframeTotal)
	fmt.Fprintf(w, "keyframe_rate=%.4f\n", m.KeyframeRate())
	fmt.Fprintf(w, "segment_sha256=%s\n", m.SegmentSHA256)
	fmt.Fprintf(w, "duplicate_packets=%d\n", m.DuplicatePackets)
	fmt.Fprintf(w, "reordered_packets=%d\n", m.ReorderedPackets)
	fmt.Fprintf(w, "recovered_gaps=%d\n", m.RecoveredGaps)
	fmt.Fprintf(w, "damaged_spans=%d\n", m.DamagedSpans)
	fmt.Fprintf(w, "repeat_run_match=%t\n", m.RepeatRunMatch)
	fmt.Fprintf(w, "readback_match=%d/%d\n", res.ReadBackMatch, res.ReadBackTotal)
}

