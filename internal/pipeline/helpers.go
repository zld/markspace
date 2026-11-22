package pipeline

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"mediq/internal/commit"
	"mediq/internal/index"
	"mediq/internal/journal"
	"mediq/internal/manifest"
	"mediq/internal/report"
	"mediq/internal/segment"
	"mediq/internal/timeline"
)

// writeInputs persists streams and their indexes through the atomic
// commit boundary, so even input staging is crash-safe.
func writeInputs(workDir string, assets []*Asset, w *journal.WAL) error {
	for _, a := range assets {
		streamPath := filepath.Join(workDir, dirStreams, a.Spec.ID+".raw")
		if err := atomicWrite(w, "input:"+a.Spec.ID, streamPath, a.Data); err != nil {
			return err
		}
		idx := index.Render(a.Spec.ID+".raw", SHA256Hex(a.Data), a.Entries)
		idxPath := filepath.Join(workDir, dirIndexes, a.Spec.ID+".idx")
		if err := atomicWrite(w, "index:"+a.Spec.ID, idxPath, idx); err != nil {
			return err
		}
	}
	return nil
}

// atomicWrite stages bytes and commits them through one WAL boundary.
func atomicWrite(w *journal.WAL, stage, finalPath string, data []byte) error {
	sw, err := commit.Create(finalPath)
	if err != nil {
		return err
	}
	if _, err := sw.Write(data); err != nil {
		_ = sw.Abort(w, stage)
		return err
	}
	_, err = sw.Commit(w, stage, nil)
	return err
}

// stageOnly writes segment temp files and records Prepared without a
// commit, simulating a crash before the atomic boundary.
func stageOnly(workDir string, assets []*Asset, plans []*segment.Plan, w *journal.WAL) error {
	dataByRep := map[string][]byte{}
	for _, a := range assets {
		dataByRep[a.Spec.ID] = a.Data
	}
	for _, p := range plans {
		data := dataByRep[p.RepID]
		for _, s := range p.Segments {
			dir := dirSegments
			if s.Quarantined {
				dir = dirQuarantine
			}
			tmp := filepath.Join(workDir, dir, fmt.Sprintf(".mediq-tmp-%s-%d.bin", p.RepID, s.Index))
			if err := os.MkdirAll(filepath.Dir(tmp), 0o755); err != nil {
				return err
			}
			body := data[s.StartOffset:s.EndOffset]
			if err := os.WriteFile(tmp, body, 0o644); err != nil {
				return err
			}
			if _, err := w.Prepared("segment:"+p.RepID, "", SHA256Hex(body), []string{tmp},
				map[string]string{"boundary": fmt.Sprintf("%d", s.Index)}); err != nil {
				return err
			}
		}
	}
	return nil
}

// materialize commits every segment file and returns per-boundary hashes.
func materialize(workDir string, assets []*Asset, plans []*segment.Plan,
	w *journal.WAL, rb *report.Builder) (map[string]map[int]string, error) {
	dataByRep := map[string][]byte{}
	for _, a := range assets {
		dataByRep[a.Spec.ID] = a.Data
	}
	hashes := map[string]map[int]string{}
	for _, p := range plans {
		hashes[p.RepID] = map[int]string{}
		data := dataByRep[p.RepID]
		for _, s := range p.Segments {
			dir := dirSegments
			if s.Quarantined {
				dir = dirQuarantine
			}
			finalPath := filepath.Join(workDir, dir, fmt.Sprintf("%s-%04d.bin", p.RepID, s.Index))
			body := data[s.StartOffset:s.EndOffset]
			stage := "segment:" + p.RepID
			if err := atomicWrite(w, stage, finalPath, body); err != nil {
				return nil, err
			}
			hash, err := commit.HashFile(finalPath)
			if err != nil {
				return nil, err
			}
			if err := rb.HashSegment(p.RepID, s.Index, finalPath); err != nil {
				return nil, err
			}
			hashes[p.RepID][s.Index] = hash
		}
	}
	return hashes, nil
}

func commitAndReport(cfg Config, w *journal.WAL, rb *report.Builder, rec *journal.Report,
	assets []*Asset, plans []*segment.Plan, alignment *segment.Alignment,
	counts FaultCounts, inputHashes map[string]string, hitRows, hitTotal int) (*Outcome, error) {
	segHashes, err := materialize(cfg.WorkDir, assets, plans, w, rb)
	if err != nil {
		return nil, err
	}
	hitRate := 1.0
	if hitTotal > 0 {
		hitRate = float64(hitRows) / float64(hitTotal)
	}
	m := manifest.Build(plans, segHashes, manifest.Options{
		Tolerance:   cfg.Tolerance,
		SkewMaxMs:   alignment.SkewMaxMs,
		InputHash:   inputHashes,
		GeneratedAt: time.Now(),
	})
	rendered, err := manifest.Marshal(m)
	if err != nil {
		return nil, err
	}
	verify, err := manifest.RoundTrip(m, rendered)
	if err != nil {
		return nil, err
	}
	for _, e := range verify.Errors {
		rb.AddError(e)
	}
	if err := manifest.ComparePlans(m, plans); err != nil {
		return nil, err
	}
	manPath := filepath.Join(cfg.WorkDir, dirManifest, "manifest.json")
	if err := atomicWrite(w, string(timeline.StageManifest), manPath, rendered); err != nil {
		return nil, err
	}
	if err := WriteEvidenceSidecar(cfg.WorkDir, rendered); err != nil {
		return nil, err
	}
	exitCode := 0
	if counts.anyDamage() || verify.TotalRows != verify.MatchedRows {
		exitCode = 2
	}
	rep := rb.Finalize(plans, hitRate, verify.ConsistencyPct, alignment.SkewMaxMs,
		verify.MatchedRows, verify.TotalRows, rec.Replayed, rec.RolledBack,
		counts.toInjection(), time.Now())
	repBytes, err := report.Marshal(rep)
	if err != nil {
		return nil, err
	}
	repPath := filepath.Join(cfg.WorkDir, dirReport, "report.json")
	if err := atomicWrite(w, string(timeline.StageVerify), repPath, repBytes); err != nil {
		return nil, err
	}
	return &Outcome{
		Plans: plans, Alignment: alignment, Manifest: m, Report: rep,
		Recover: rec, ExitCode: exitCode, InputHashes: inputHashes,
	}, nil
}

// finalizeRejected handles skew rejection: no segment commit happens,
// but the report is still written with the quarantine evidence.
func finalizeRejected(cfg Config, w *journal.WAL, rb *report.Builder, plans []*segment.Plan,
	rec *journal.Report, counts FaultCounts, inputHashes map[string]string,
	hitRows, hitTotal int) (*Outcome, error) {
	hitRate := 1.0
	if hitTotal > 0 {
		hitRate = float64(hitRows) / float64(hitTotal)
	}
	rep := rb.Finalize(plans, hitRate, 0, maxSkewMs(plans), 0, 0,
		rec.Replayed, rec.RolledBack, counts.toInjection(), time.Now())
	repBytes, err := report.Marshal(rep)
	if err != nil {
		return nil, err
	}
	repPath := filepath.Join(cfg.WorkDir, dirReport, "report.json")
	if err := atomicWrite(w, string(timeline.StageVerify), repPath, repBytes); err != nil {
		return nil, err
	}
	return &Outcome{Plans: plans, Report: rep, Recover: rec, ExitCode: 1,
		InputHashes: inputHashes}, nil
}

func stopAt(rb *report.Builder, rec *journal.Report, counts FaultCounts,
	plans []*segment.Plan, hitRate float64, at FailPoint) (*Outcome, error) {
	rep := rb.Finalize(plans, hitRate, 0, maxSkewMs(plans), 0, 0,
		rec.Replayed, rec.RolledBack, counts.toInjection(), time.Now())
	return &Outcome{Plans: plans, Report: rep, Recover: rec, ExitCode: 1, StoppedAt: at}, nil
}

// maxSkewMs computes the largest clean-boundary start-time spread
// across representations, mirroring the aligner's measurement so a
// rejected group still reports the skew that caused the rejection.
func maxSkewMs(plans []*segment.Plan) float64 {
	if len(plans) == 0 {
		return 0
	}
	boundaries := len(plans[0].Segments)
	maxMs := 0.0
	for b := 0; b < boundaries; b++ {
		var minTS, maxTS timeline.Ticks
		first := true
		for _, p := range plans {
			if b >= len(p.Segments) || p.Segments[b].Quarantined {
				continue
			}
			ts := p.Segments[b].StartPTS
			if first || ts < minTS {
				minTS = ts
			}
			if first || ts > maxTS {
				maxTS = ts
			}
			first = false
		}
		if !first {
			if ms := timeline.AbsDiff(maxTS, minTS).MillisFloat(); ms > maxMs {
				maxMs = ms
			}
		}
	}
	return maxMs
}

func asTimeline(err error) *timeline.Error {
	if te, ok := err.(*timeline.Error); ok {
		return te
	}
	return timeline.New(timeline.StageAlign, timeline.KindInconsistent, -1, "reject_group", err.Error())
}

func (c FaultCounts) anyDamage() bool {
	return c.Truncated > 0 || c.Corrupt > 0 || c.MissingIndex > 0 ||
		c.Duplicate > 0 || c.Misaligned > 0
}

func (c FaultCounts) toInjection() report.InjectionCounts {
	return report.InjectionCounts{
		TruncatedStreams: c.Truncated,
		CorruptUnits:     c.Corrupt,
		MissingIndex:     c.MissingIndex,
		DuplicateIndex:   c.Duplicate,
		MisalignedReps:   c.Misaligned,
	}
}
