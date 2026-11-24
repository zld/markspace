// Command mediad is the delivery acceptance entry point. It demultiplexes
// the inputs listed in a delivery manifest, normalizes their timelines,
// losslessly remuxes them and verifies the result by re-reading it.
//
// Exit codes: 0 = completed (including recovered-with-gaps), 2 =
// unrecoverable input, 1 = usage or internal error.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"

	"mediad/internal/adapter"
	"mediad/internal/fixture"
	"mediad/internal/media"
	"mediad/internal/pipeline"
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	// The delivery pipeline holds compressed samples for the input and the
	// re-parsed output at once; a tighter GC target keeps peak memory
	// below 1 GB for 2h manifests, with ample CPU headroom to spare.
	debug.SetGCPercent(50)
	if len(args) == 0 || args[0] != "verify" {
		fmt.Fprintln(os.Stderr, "usage: mediad verify [--self-generate] [--input manifest.json] [--seed N] [--segments N] [--corrupt MODE]")
		return 1
	}
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	selfGen := fs.Bool("self-generate", false, "generate synthetic inputs from the seed")
	input := fs.String("input", "", "delivery manifest to verify (default: generated)")
	seed := fs.Int64("seed", 42, "fixture RNG seed")
	segments := fs.Int("segments", 120, "number of delivery segments")
	corrupt := fs.String("corrupt", "", "corruption mode: truncated-box|invalid-edit-list")
	if err := fs.Parse(args[1:]); err != nil {
		return 1
	}
	if !*selfGen && *input == "" {
		fmt.Fprintln(os.Stderr, "either --self-generate or --input is required")
		return 1
	}

	workDir, err := os.MkdirTemp("", "mediad-verify-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	// Artifacts stay only for successful (or recovered) deliveries; an
	// unrecoverable run must not leave half-finished output behind.
	keep := false
	defer func() {
		if !keep {
			os.RemoveAll(workDir)
		}
	}()

	manifestPath := *input
	if *selfGen {
		layout, err := fixture.Generate(fixture.Options{
			Seed: *seed, Segments: *segments, Corrupt: *corrupt, Dir: workDir,
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "generate fixtures:", err)
			return 1
		}
		manifestPath = layout.ManifestPath
	}

	res, verr := verify(manifestPath, *segments, workDir)
	if verr != nil {
		if media.IsUnrecoverable(verr) {
			printMetrics(metrics{status: "unrecoverable", segments: *segments, hashTotal: *segments, exitCode: 2})
			fmt.Fprintln(os.Stderr, verr)
			return 2
		}
		fmt.Fprintln(os.Stderr, verr)
		return 1
	}
	keep = true
	res.exitCode = 0
	printMetrics(res)
	fmt.Printf("artifact_dir=%s\n", workDir)
	return 0
}

type metrics struct {
	consistency float64
	keyframeHit float64
	hashMatch   int
	hashTotal   int
	gaps        int
	driftPPM    int64
	status      string
	segments    int
	exitCode    int
}

func printMetrics(m metrics) {
	fmt.Printf("roundtrip_consistency=%.4f\n", m.consistency)
	fmt.Printf("keyframe_hit_rate=%.4f\n", m.keyframeHit)
	fmt.Printf("segment_hash_match=%d/%d\n", m.hashMatch, m.hashTotal)
	fmt.Printf("recovered_gaps=%d\n", m.gaps)
	fmt.Printf("drift_ppm=%d\n", m.driftPPM)
	fmt.Printf("status=%s\n", m.status)
	fmt.Printf("exit_code=%d\n", m.exitCode)
}

// normed binds a mapped track to its normalized output timeline.
type normed struct {
	mt       adapter.MappedTrack
	outTimes []int64 // per event, quantized to output ticks
	keep     []bool  // per event
}

// verify runs the full delivery pipeline against a manifest and returns
// the acceptance metrics.
func verify(manifestPath string, nSegments int, workDir string) (metrics, error) {
	var man struct {
		Version string `json:"version"`
		Inputs  []struct {
			Path string `json:"path"`
		} `json:"inputs"`
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return metrics{}, media.New(media.PhaseProbe, -1, -1, "read manifest", err)
	}
	if err := json.Unmarshal(raw, &man); err != nil {
		return metrics{}, media.New(media.PhaseProbe, -1, -1, "parse manifest", err).Fatal()
	}
	if man.Version != "v1" {
		return metrics{}, media.New(media.PhaseProbe, -1, -1,
			fmt.Sprintf("manifest version %q unsupported", man.Version), nil).Fatal()
	}

	reg, err := adapter.NewRegistry(adapter.MP4Parser{}, adapter.RTPParser{})
	if err != nil {
		return metrics{}, err
	}

	var streams []*adapter.Stream
	var gaps []media.Gap
	base := filepath.Dir(manifestPath)
	for _, in := range man.Inputs {
		f, err := os.Open(filepath.Join(base, in.Path))
		if err != nil {
			return metrics{}, media.New(media.PhaseProbe, -1, -1, "open input", err)
		}
		st, err := reg.Parse(in.Path, f)
		f.Close()
		if err != nil {
			return metrics{}, err
		}
		streams = append(streams, st)
		gaps = append(gaps, st.Gaps...)
	}

	mapped, err := adapter.MapTracks(streams...)
	if err != nil {
		return metrics{}, err
	}

	// Demux gaps are recorded per source stream with source track IDs;
	// re-key them onto the mapped track pointers for per-track use.
	gapsFor := map[*adapter.Track][]media.Gap{}
	for _, st := range streams {
		byID := map[int]*adapter.Track{}
		for _, tr := range st.Tracks {
			byID[tr.ID] = tr
		}
		for _, g := range st.Gaps {
			if tr := byID[g.TrackID]; tr != nil {
				gapsFor[tr] = append(gapsFor[tr], g)
			}
		}
	}

	// Timeline normalization per track, then quantization to the output
	// timescale so every downstream consumer (remux, index, segments,
	// re-parse verification) agrees on identical timestamps.
	var tracks []normed
	var driftPPM float64
	var driftWeight int
	for _, mt := range mapped {
		samples := make([]media.Sample, len(mt.Track.Events))
		for i, ev := range mt.Track.Events {
			samples[i] = media.Sample{Index: i, CTS: ev.MediaTime, Duration: ev.Duration, Keyframe: ev.Keyframe}
		}
		out, keep, err := media.BuildTimeline(samples, mt.Track.Edits, 0)
		if err != nil {
			return metrics{}, err // invalid edit lists surface here, unrecoverable
		}
		quantizeMonotonic(out, keep, media.NanosPerSecond/pipeline.OutputTimescale)
		tracks = append(tracks, normed{mt: mt, outTimes: out, keep: keep})
		for _, ds := range mt.Track.DriftSegments {
			driftPPM += ds.SlopePPM * float64(ds.Count)
			driftWeight += ds.Count
		}
	}
	if driftWeight > 0 {
		driftPPM /= float64(driftWeight)
	}

	// Remux losslessly straight to the delivery file (no in-memory copy of
	// the output), then re-read it from disk for verification.
	var rts []pipeline.RemuxTrack
	for _, t := range tracks {
		rts = append(rts, pipeline.RemuxTrack{Mapped: t.mt, OutTimes: t.outTimes, Keep: t.keep})
	}
	deliveryPath := filepath.Join(workDir, "delivery.mp4")
	df, err := os.Create(deliveryPath)
	if err != nil {
		return metrics{}, err
	}
	if err := pipeline.Remux(df, rts); err != nil {
		df.Close()
		return metrics{}, media.New(media.PhaseRemux, -1, -1, "remux", err)
	}
	df.Close()
	rf, err := os.Open(deliveryPath)
	if err != nil {
		return metrics{}, err
	}
	reStream, err := reg.Parse("delivery.mp4", rf)
	rf.Close()
	if err != nil {
		return metrics{}, media.New(media.PhaseVerify, -1, -1, "re-parse output", err)
	}

	m := metrics{segments: nSegments}
	m.consistency = checkRoundtrip(tracks, reStream)

	// Keyframe index on the primary (first video) track; hit rate over
	// segment-start lookups.
	primary := -1
	for i, t := range tracks {
		if t.mt.Track.Type == media.TrackVideo {
			primary = i
			break
		}
	}
	if primary < 0 {
		return metrics{}, media.New(media.PhaseIndex, -1, -1, "no video track", nil)
	}
	pt := tracks[primary]
	var keptTimes []int64
	for i, ev := range pt.mt.Track.Events {
		if i < len(pt.keep) && pt.keep[i] && !ev.Damaged {
			keptTimes = append(keptTimes, pt.outTimes[i])
		}
	}
	if len(keptTimes) == 0 {
		return metrics{}, media.New(media.PhaseIndex, pt.mt.OutputID, -1, "no usable samples", nil)
	}
	idx := pipeline.BuildIndex(pt.mt, pt.outTimes)
	gaps = append(gaps, idx.Broken...)
	bounds := pipeline.SegmentBounds(0, keptTimes[len(keptTimes)-1]+1, nSegments)
	hits := 0
	for _, b := range bounds {
		if _, ok := idx.Locate(b[0]); ok {
			hits++
		}
	}
	m.keyframeHit = float64(hits) / float64(len(bounds))

	// Segment hashes from the input side, then recomputed from the
	// re-parsed output: they must agree segment for segment.
	inSegs := pipeline.BuildSegments(pt.mt.OutputID, pt.mt.Track.Events, pt.outTimes, bounds, gapsFor[pt.mt.Track])
	outTrack := findTrack(reStream, pt.mt.OutputID)
	var outTimes []int64
	if outTrack != nil {
		for _, ev := range outTrack.Events {
			outTimes = append(outTimes, ev.MediaTime)
		}
	}
	outSegs := pipeline.BuildSegments(pt.mt.OutputID, outTrack.Events, outTimes, bounds, nil)
	m.hashTotal = len(inSegs)
	for i := range inSegs {
		if i < len(outSegs) && inSegs[i].SHA256 == outSegs[i].SHA256 {
			m.hashMatch++
		}
	}

	m.gaps = len(gaps)
	m.driftPPM = int64(driftPPM + 0.5)
	m.status = "ok"
	if m.gaps > 0 || m.consistency < 1 {
		m.status = "recovered"
	}

	manifest := pipeline.Manifest{
		Segments: inSegs, Gaps: gaps, DriftPPM: driftPPM, Status: m.status,
	}
	mf, err := os.Create(filepath.Join(workDir, "manifest.json"))
	if err != nil {
		return metrics{}, err
	}
	defer mf.Close()
	if err := manifest.WriteJSON(mf); err != nil {
		return metrics{}, err
	}
	return m, nil
}

// quantizeMonotonic normalizes the first kept timestamp to zero (the
// container stts cannot express an initial offset), snaps kept timestamps
// to the output tick grid and re-enforces strict monotonicity with a
// one-tick minimum step.
func quantizeMonotonic(times []int64, keep []bool, step int64) {
	prev := int64(-1)
	base := int64(-1)
	for i := range times {
		if i < len(keep) && !keep[i] {
			continue
		}
		if base < 0 {
			base = times[i]
		}
		t := (times[i] - base) / step * step
		if t <= prev {
			t = prev + step
		}
		times[i], prev = t, t
	}
}

// checkRoundtrip re-reads the remuxed stream and scores payload and
// timestamp equality per kept event. Damaged events are expected to be
// absent from the output and count as mismatches.
func checkRoundtrip(tracks []normed, re *adapter.Stream) float64 {
	var matched, total int
	for ti, tn := range tracks {
		if ti >= len(re.Tracks) {
			continue
		}
		rt := re.Tracks[ti]
		j := 0 // index into the re-parsed output events
		for i, ev := range tn.mt.Track.Events {
			if i >= len(tn.keep) || !tn.keep[i] {
				continue
			}
			total++
			if ev.Damaged {
				continue // correctly absent from the output, but not a match
			}
			if j >= len(rt.Events) {
				break
			}
			reEv := rt.Events[j]
			j++
			if bytes.Equal(ev.Payload, reEv.Payload) && reEv.MediaTime == tn.outTimes[i] {
				matched++
			}
		}
	}
	if total == 0 {
		return 0
	}
	return float64(matched) / float64(total)
}

func findTrack(st *adapter.Stream, id int) *adapter.Track {
	for _, t := range st.Tracks {
		if t.ID == id {
			return t
		}
	}
	return nil
}
