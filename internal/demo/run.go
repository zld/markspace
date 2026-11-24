package demo

import (
	"os"
	"path/filepath"

	"mediad/internal/batch"
	"mediad/internal/clock"
	"mediad/internal/manifest"
	"mediad/internal/mederr"
)

// Run builds/processes the demo and applies optional restart/overlap faults.
// The returned report is safe for the CLI to print even when err is non-nil.
func Run(opt Options) (manifest.Report, error) {
	p, err := BuildProgram(opt)
	if err != nil {
		return manifest.Report{OK: false, Errors: []*mederr.Error{asMediaError(err)}}, err
	}
	idx, runRep, err := p.Execute()
	if err != nil {
		return manifest.Report{OK: false, Errors: []*mederr.Error{asMediaError(err)}}, err
	}
	rep, err := p.WriteManifest(idx, runRep)
	if err != nil {
		return rep, err
	}

	var overlapErr *mederr.Error
	if opt.LateOverlap {
		indexPath := filepath.Join(opt.Out, batch.IndexName)
		before, readErr := os.ReadFile(indexPath)
		if readErr != nil {
			return rep, readErr
		}
		late := clock.Span{Start: 500_000_000, End: 1_500_000_000}
		if oerr := batch.CheckLateOverlap(idx, "late:0", late); oerr != nil {
			me := asMediaError(oerr)
			overlapErr = me
			rep.OverlapRejected = true
			rep.Errors = append(rep.Errors, me)
		}
		after, readErr := os.ReadFile(indexPath)
		if readErr != nil {
			return rep, readErr
		}
		if string(before) != string(after) {
			me := mederr.New(mederr.CatIndex, "index", 500_000_000, 1_500_000_000, false,
				"checkpoint changed while rejecting late overlap")
			rep.Errors = append(rep.Errors, me)
			return rep, me
		}
	}

	recovery := opt.CorruptIndex || opt.TruncateChunk
	if opt.CorruptIndex {
		indexPath := filepath.Join(opt.Out, batch.IndexName)
		if werr := os.WriteFile(indexPath, []byte(`{"version":1,"fingerprint":"corrupt`), 0o644); werr != nil {
			return rep, werr
		}
	}
	if opt.TruncateChunk && len(idx.Chunks) > 0 {
		target := filepath.Join(opt.Out, idx.Chunks[0].File)
		_ = os.Truncate(target, 100)
	}
	if recovery {
		idx2, runRep2, rerr := p.Execute()
		if rerr != nil {
			return rep, rerr
		}
		rep, err = p.WriteManifest(idx2, runRep2)
		if err != nil {
			return rep, err
		}
		if overlapErr != nil {
			rep.OverlapRejected = true
			rep.Errors = append(rep.Errors, overlapErr)
		}
	}

	if qerr := CheckQuantitativeGates(rep, opt); qerr != nil {
		me := asMediaError(qerr)
		rep.OK = false
		rep.Errors = append(rep.Errors, me)
		return rep, me
	}
	if overlapErr != nil {
		rep.OK = false
		return rep, overlapErr
	}
	rep.OK = true
	return rep, nil
}

// CheckQuantitativeGates enforces the published acceptance tolerances.
func CheckQuantitativeGates(r manifest.Report, opt Options) error {
	if r.ChunksCommitted <= 0 {
		return mederr.New(mederr.CatChecksum, "batch", -1, -1, false, "no chunks committed")
	}
	if d := r.BitrateDeviationPct; d > 5 || d < -5 {
		return mederr.New(mederr.CatAudio, "manifest", -1, -1, false,
			"bitrate deviation %.3f%% exceeds 5%%", d)
	}
	if r.PeakDBFS > opt.PeakCeilingDB+0.02 {
		return mederr.New(mederr.CatAudio, "master", -1, -1, false,
			"peak %.3f dBFS exceeds ceiling %.2f dBFS", r.PeakDBFS, opt.PeakCeilingDB)
	}
	if r.TimelineAlignmentErrorMs > 2 {
		return mederr.New(mederr.CatTimeline, "timeline", -1, -1, false,
			"alignment error %.4f ms exceeds 2 ms", r.TimelineAlignmentErrorMs)
	}
	return nil
}

func asMediaError(err error) *mederr.Error {
	if me, ok := err.(*mederr.Error); ok {
		return me
	}
	return mederr.New(mederr.CatIO, "demo", -1, -1, false, "%v", err)
}
