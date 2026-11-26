// Package verify drives the end-to-end acceptance scenario. It generates a
// media tree from a seed, scans it, re-parses a window for round-trip
// fidelity, executes the DASH interval across a damaged fragment with local
// recovery, executes the HLS interval across a discontinuity and a wrap, and
// proves that an unchanged input reuses the index while a changed input
// invalidates it. Every printed counter comes from work done in this
// invocation.
package verify

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"medtrace/internal/core"
	"medtrace/internal/events"
	"medtrace/internal/fixture"
	"medtrace/internal/index"
	"medtrace/internal/manifest"
	"medtrace/internal/report"
	"medtrace/internal/stream"
)

// Options configure a verify run.
type Options struct {
	Work string
	Seed int64
	Out  io.Writer
}

// roundtripWindow is how many leading DASH fragments are re-parsed and
// compared. Five spans both representations and the switch boundary.
const roundtripWindow = 5

// Run executes the acceptance scenario and returns the process exit code:
// 0 when the report was produced (recorded anomalies included), 2 for invalid
// parameters or manifests, 3 for local I/O or report failures.
func Run(opt Options) int {
	out := opt.Out
	if out == nil {
		out = os.Stdout
	}
	root := filepath.Join(opt.Work, "media")
	if err := os.RemoveAll(root); err != nil {
		fmt.Fprintf(out, "verify: clean work dir: %v\n", err)
		return 3
	}
	sc, err := fixture.Build(root, opt.Seed)
	if err != nil {
		fmt.Fprintf(out, "verify: generate: %v\n", err)
		return 3
	}

	dash, err := manifest.Load(root, sc.DashMPD)
	if err != nil {
		fmt.Fprintf(out, "verify: dash: %v\n", err)
		return 2
	}
	hls, err := manifest.Load(root, sc.HLSPlaylist)
	if err != nil {
		fmt.Fprintf(out, "verify: hls: %v\n", err)
		return 2
	}

	dashIdx, dashScan, err := index.Build(root, dash)
	if err != nil {
		fmt.Fprintf(out, "verify: index dash: %v\n", err)
		return 3
	}
	hlsIdx, hlsScan, err := index.Build(root, hls)
	if err != nil {
		fmt.Fprintf(out, "verify: index hls: %v\n", err)
		return 3
	}

	log, err := events.ReadFile(root, sc.EventLog)
	if err != nil {
		fmt.Fprintf(out, "verify: events: %v\n", err)
		return 2
	}
	eventSummary := correlateEvents(log)

	// Round-trip the leading DASH fragments before damaging anything.
	rtHit, rtTotal := roundtrip(root, sc, roundtripWindow)

	// Damage one fragment after indexing, so the indexed hashes and offsets
	// remain the authoritative source positions.
	if err := sc.Damage(root); err != nil {
		fmt.Fprintf(out, "verify: damage: %v\n", err)
		return 3
	}

	dashRes, err := (&stream.Runner{Root: root, Index: dashIdx, VerifyHashes: true}).
		Run(0, sc.DashDuration)
	if err != nil {
		fmt.Fprintf(out, "verify: run dash: %v\n", err)
		return 3
	}
	// The HLS pass verifies epoch handling (discontinuity and wrap); hash
	// counters belong to the DASH pass so the two narratives stay separate.
	hlsDuration := hlsIdx.Segments[len(hlsIdx.Segments)-1].End
	hlsRes, err := (&stream.Runner{Root: root, Index: hlsIdx}).
		Run(0, hlsDuration)
	if err != nil {
		fmt.Fprintf(out, "verify: run hls: %v\n", err)
		return 3
	}
	kfHit, kfTotal := keyframeStarts(hlsIdx)

	invalidated, err := cacheRoundTrip(root, dash, dashIdx, filepath.Join(opt.Work, "index"))
	if err != nil {
		fmt.Fprintf(out, "verify: cache: %v\n", err)
		return 3
	}

	rep := report.Build(0, sc.DashDuration, dashIdx, dashIdx.Epochs, dashScan, dashRes)
	rep.Merge(report.Build(0, hlsDuration, hlsIdx, hlsIdx.Epochs, hlsScan, hlsRes))
	rep.Stats.Roundtrip = rtHit
	rep.Stats.RoundtripTotal = rtTotal
	rep.Stats.KeyframeHit = kfHit
	rep.Stats.KeyframeTotal = kfTotal
	rep.Stats.CacheInvalidated = invalidated
	rep.Recount()

	if err := rep.Write(out); err != nil {
		fmt.Fprintf(out, "verify: write report: %v\n", err)
		return 3
	}
	fmt.Fprintf(out, "\nevents: %s\n", eventSummary)
	if !contractMet(rep.Stats) {
		fmt.Fprintf(out, "verify: acceptance counters do not match the contract\n")
		return 3
	}
	fmt.Fprintf(out, "verify: ok\n")
	return 0
}

// contractMet checks the exact acceptance counters the task specifies.
func contractMet(s report.Stats) bool {
	return s.Roundtrip == 5 && s.RoundtripTotal == 5 &&
		s.KeyframeHit == 4 && s.KeyframeTotal == 4 &&
		s.SegmentHashes == 6 && s.SegmentTotal == 6 &&
		s.HashMismatch == 1 && s.Anomalies == 4 &&
		s.Recovered == 1 && s.OffsetPreserved == 1 &&
		s.CacheInvalidated == 1
}

// roundtrip re-parses the first n DASH fragments and compares each against the
// generator's layout: sample byte offsets, sizes, decode timestamps, durations
// and keyframe flags must all match.
func roundtrip(root string, sc *fixture.Scenario, n int) (matched, total int) {
	if n > len(sc.Dash) {
		n = len(sc.Dash)
	}
	for i := 0; i < n; i++ {
		lay := sc.Dash[i]
		total++
		got, err := index.Rescan(root, lay.URI)
		if err != nil {
			continue
		}
		if layoutMatches(lay, got) {
			matched++
		}
	}
	return matched, total
}

// layoutMatches compares a re-parsed fragment to the generated layout.
func layoutMatches(lay fixture.SegmentLayout, got []index.Rescanned) bool {
	if len(got) != len(lay.Samples) {
		return false
	}
	for i, s := range lay.Samples {
		g := got[i]
		if g.Offset != s.Offset || int(g.Size) != s.Size ||
			g.DTSTicks != s.DTSTicks || g.DurTicks != s.DurTicks || g.Key != s.Key {
			return false
		}
	}
	return true
}

// keyframeStarts counts, over an index, how many segments begin on a verified
// sync sample (hits) out of those with any samples (tries).
func keyframeStarts(idx *core.Index) (hits, tries int) {
	for _, seg := range idx.Segments {
		if len(seg.Samples) == 0 {
			continue
		}
		tries++
		if seg.Samples[0].Keyframe {
			hits++
		}
	}
	return hits, tries
}

// correlateEvents folds the client log through an RTP timeline and summarises
// the wrap it observes, proving pion/rtp parsing and wrap handling.
func correlateEvents(log *events.Log) string {
	tl := events.NewTimeline(90000)
	wraps, mapped := 0, 0
	for _, rec := range log.Records {
		if rec.RTP.Raw == 0 && rec.RTP.ClockRate == 0 {
			continue
		}
		if mapped == 0 {
			tl.Anchor(rec.RTP.Raw, core.Nanos(rec.WallNanos))
		}
		if _, wrap, _, err := tl.Map(rec.RTP.Raw); err == nil {
			if wrap {
				wraps++
			}
			mapped++
		}
	}
	return fmt.Sprintf("parsed=%d rtp_mapped=%d wraps=%d", len(log.Records), mapped, wraps)
}

// cacheRoundTrip proves the summary cache is reused for an unchanged input and
// invalidated for a changed one. It returns 1 when invalidation was observed.
func cacheRoundTrip(root string, pl *manifest.Playlist, idx *core.Index, dir string) (int, error) {
	digest, err := index.InputDigest(root, pl)
	if err != nil {
		return 0, err
	}
	cache := index.NewCache(dir)
	entry := index.BuildEntry(pl, digest, idx, idx.Epochs)
	if err := cache.Save(entry); err != nil {
		return 0, err
	}
	loaded, err := cache.Load(pl.File)
	if err != nil {
		return 0, err
	}
	if !loaded.Valid(digest) {
		return 0, fmt.Errorf("unchanged input did not reuse the cache")
	}
	// Touch a referenced file's modification time; the digest must change and
	// the cached entry must stop being valid.
	abs, err := core.SafeJoin(root, pl.Segments[0].URI)
	if err != nil {
		return 0, err
	}
	now := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(abs, now, now); err != nil {
		return 0, err
	}
	changed, err := index.InputDigest(root, pl)
	if err != nil {
		return 0, err
	}
	if changed == digest {
		return 0, fmt.Errorf("changed input produced an identical digest")
	}
	if loaded.Valid(changed) {
		return 0, nil
	}
	return 1, nil
}
