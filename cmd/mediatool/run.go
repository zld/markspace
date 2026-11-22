package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mediapipe/internal/ingest"
	"mediapipe/internal/manifest"
	"mediapipe/internal/pipeline"
	rtprec "mediapipe/internal/rtp"
)

type runConfig struct {
	Seed    int64
	Tracks  int
	Workers int
	Faults  string
}

// fsSource reads segments from the generated tree, corrupting the first
// read of marked URIs to simulate a transient transfer fault.
type fsSource struct {
	dir          string
	corruptOnce  map[string]bool
	mu           sync.Mutex
	seen         map[string]int
}

func (s *fsSource) ReadSegment(uri string) ([]byte, error) {
	clean := filepath.Clean(uri)
	full := filepath.Join(s.dir, clean)
	if !strings.HasPrefix(full, filepath.Clean(s.dir)+string(os.PathSeparator)) {
		return nil, fmt.Errorf("path escape %q", uri)
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.seen[uri]++
	n := s.seen[uri]
	s.mu.Unlock()
	if s.corruptOnce[uri] && n == 1 {
		bad := append([]byte(nil), data...)
		bad[len(bad)/2] ^= 0xFF // transient corruption, fixed on retry
		return bad, nil
	}
	return data, nil
}

// RunOnce generates the corpus and executes the full pipeline once.
func RunOnce(cfg runConfig) (*Report, error) {
	ss, err := Generate(cfg.Seed, cfg.Tracks, cfg.Faults)
	if err != nil {
		return nil, err
	}
	defer ss.Cleanup()
	rep := &Report{RecordsComplete: true, FramesOK: true}

	// Stage 1: parse manifests.
	playlists := make([]*manifest.Playlist, len(ss.Tracks))
	for i, ts := range ss.Tracks {
		f, err := os.Open(ts.Manifest)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		switch ts.Format {
		case "hls":
			playlists[i], err = manifest.ParseHLS(ts.ID, f)
		default:
			playlists[i], err = manifest.ParseDASH(ts.ID, f)
		}
		if err != nil {
			return nil, err
		}
	}

	// Stage 2: concurrent bounded ingest with per-segment latency metrics.
	src := &fsSource{dir: ss.Dir, corruptOnce: ss.CorruptOnce, seen: map[string]int{}}
	pool, err := pipeline.NewPool(context.Background(), cfg.Workers, cfg.Workers*4)
	if err != nil {
		return nil, err
	}
	allRecords := make([][]ingest.Record, len(ss.Tracks))
	var mu sync.Mutex
	var latencies []float64
	for ti, ts := range ss.Tracks {
		pl := playlists[ti]
		allRecords[ti] = make([]ingest.Record, len(pl.Refs))
		val := &ingest.Validator{Src: src, Channels: ts.InLayout.Channels(), InRate: ts.InRate,
			MaxRetries: ingest.DefaultMaxRetries, Checksums: ts.Checksums}
		for ri, ref := range pl.Refs {
			ti, ri, ref := ti, ri, ref
			start := time.Now()
			if err := pool.Submit(func(ctx context.Context) error {
				rec, err := val.Ingest(ctx, ref)
				if err != nil {
					return err
				}
				allRecords[ti][ri] = rec
				mu.Lock()
				latencies = append(latencies, float64(time.Since(start).Microseconds())/1000)
				mu.Unlock()
				return nil
			}); err != nil {
				return nil, err
			}
		}
	}
	if err := pool.Wait(); err != nil {
		return nil, err
	}
	rep.P95LatencyMs = p95(latencies)

	// Stage 3: per-track streaming audio normalization on the shared clock.
	allResults := make([][]pipeline.SegmentResult, len(ss.Tracks))
	for ti, ts := range ss.Tracks {
		results, err := processAudio(ts, allRecords[ti])
		if err != nil {
			return nil, err
		}
		allResults[ti] = results
	}

	// Stage 4: RTP recovery per track.
	var rtpRec, rtpLost int
	for _, ts := range ss.Tracks {
		rec := rtprec.NewRecoverer(rtpWindow)
		for _, p := range ss.RTP[ts.ID] {
			rec.Push(p)
		}
		rec.Flush()
		st := rec.Stats()
		rtpRec += st.Recovered
		rtpLost += st.Lost
		if st.Lost > 0 {
			rep.FaultRecords = append(rep.FaultRecords, fmt.Sprintf(
				"track=%s stage=rtp: %d packets lost beyond window", ts.ID, st.Lost))
		}
	}
	rep.RTPLost = rtpLost
	rep.PacketRecoveryRate = rtprec.Stats{Recovered: rtpRec, Lost: rtpLost}.RecoveryRate()

	// Stage 5: deterministic merge and metrics.
	var merges []*pipeline.TrackMerge
	for ti := range ss.Tracks {
		tm, err := pipeline.MergeTrack(playlists[ti], allResults[ti], 2)
		if err != nil {
			return nil, err
		}
		merges = append(merges, tm)
		rep.TimelineViolations += len(tm.Violations)
		for _, rec := range tm.Records {
			switch rec.Status {
			case ingest.Missing:
				rep.Missing++
				rep.FaultRecords = append(rep.FaultRecords, fmt.Sprintf(
					"track=%s stage=manifest: missing slot %d [%d,%d)",
					rec.Ref.TrackID, rec.Ref.Index, rec.Ref.Interval.Start, rec.Ref.Interval.End))
			case ingest.Recovered:
				rep.Recovered++
			case ingest.Unrecoverable:
				rep.Unrecoverable++
				rep.FaultRecords = append(rep.FaultRecords, fmt.Sprintf(
					"track=%s stage=ingest: unrecoverable %s: %s",
					rec.Ref.TrackID, rec.Ref.URI, rec.Reason))
			}
		}
		if d := tm.Frames - int64(playlists[ti].Duration()); d < -1 || d > 1 {
			rep.FramesOK = false
		}
	}
	rep.SyncErrorMs = syncErrorMs(playlists, allResults)
	pipeline.SortTracks(merges)
	mh := pipeline.ManifestHash(merges)
	h := sha256.New()
	h.Write(mh[:])
	for _, tm := range merges {
		ah := pipeline.AudioHash(tm.Samples)
		h.Write(ah[:])
		tm.Samples = nil // release; hash is the durable summary
	}
	copy(rep.OutputSHA256[:], h.Sum(nil))

	rep.FaultsPropagated = faultsPropagated(cfg.Faults, rep)
	return rep, nil
}
