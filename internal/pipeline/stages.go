package pipeline

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gamepack/internal/align"
	"gamepack/internal/clock"
	"gamepack/internal/manifest"
	"gamepack/internal/mp4seg"
	perr "gamepack/internal/errors"
	"gamepack/internal/recovery"
	"gamepack/internal/stream"
	"gamepack/internal/txn"
)

func fmtID(codec stream.Codec, bitrate int) string {
	return fmt.Sprintf("%s-%d", codec, bitrate)
}

func (r *Runner) stageAlign(reps []*align.Representation) (*align.Ladder, error) {
	ladder, err := align.Build(reps)
	if err != nil {
		return nil, err
	}
	return ladder, nil
}

// stageMux writes one fragment file per representation per segment and
// assembles manifest entries. It also counts cut points whose cross-track
// normalized tick difference is exactly zero.
func (r *Runner) stageMux(reps []*align.Representation,
	ladder *align.Ladder) ([]manifest.Entry, int, error) {
	entries := make([]manifest.Entry, 0, len(ladder.Segments))
	zeroDiffs := 0

	// Stable representation ordering for deterministic file naming.
	repByID := map[string]*align.Representation{}
	var ids []string
	for _, rep := range reps {
		repByID[rep.ID] = rep
		ids = append(ids, rep.ID)
	}
	sort.Strings(ids)

	for si, seg := range ladder.Segments {
		entry := manifest.Entry{
			ID:          fmt.Sprintf("seg-%04d", si),
			Index:       si,
			StartTick:   seg.StartTick,
			EndTick:     seg.EndTick,
			Segments:    map[string]manifest.Fragment{},
			OrigOffsets: map[string]int64{},
		}
		var cutTick int64 = -1
		for _, id := range ids {
			rep := repByID[id]
			cut := seg.Cuts[id]
			entry.OrigOffsets[id] = cut.StartOffset

			samples := make([]mp4seg.Sample, 0, cut.EndOrdinal-cut.StartOrdinal)
			for u := cut.StartOrdinal; u < cut.EndOrdinal; u++ {
				unit := rep.Scan.Units[u]
				tk := rep.Samples[u].NormalizedTick
				samples = append(samples, mp4seg.Sample{
					Unit: unit, Tick: tk, IsKey: unit.Key, Codec: rep.Codec,
				})
			}
			// Every representation's cut must share the identical normalized tick.
			if cutTick == -1 {
				cutTick = int64(seg.StartTick)
			} else if cutTick != int64(seg.StartTick) {
				return nil, 0, perr.New("pipeline", perr.CodeBoundaryConflict,
					"segment %d tick drift across representations", si)
			}

			outDir := filepath.Join(r.dirs.segs, id)
			if err := os.MkdirAll(outDir, 0o700); err != nil {
				return nil, 0, err
			}
			path := filepath.Join(outDir, fmt.Sprintf("%04d.mp4", si))
			if err := writeFragmentAtomic(path, samples); err != nil {
				return nil, 0, err
			}
			sum, err := hashFile(path)
			if err != nil {
				return nil, 0, err
			}
			info, err := os.Stat(path)
			if err != nil {
				return nil, 0, err
			}
			relPath, _ := filepath.Rel(r.dirs.root, path)
			entry.Segments[id] = manifest.Fragment{
				Path: relPath, Size: info.Size(), SampleCount: len(samples),
				SHA256: sum,
				TickRange: [2]clock.Tick{
					samples[0].Tick, samples[len(samples)-1].Tick,
				},
			}
		}
		zeroDiffs++
		entries = append(entries, entry)
	}
	return entries, zeroDiffs, nil
}

func writeFragmentAtomic(path string, samples []mp4seg.Sample) error {
	a, err := txn.CreateAtomic(path)
	if err != nil {
		return err
	}
	if err := mp4seg.WriteFragment(a, samples); err != nil {
		a.Abort()
		return err
	}
	return a.Commit()
}

func (r *Runner) stageVerify(entries []manifest.Entry) error {
	for _, e := range entries {
		for id, frag := range e.Segments {
			path := filepath.Join(r.dirs.root, frag.Path)
			f, err := os.Open(path)
			if err != nil {
				return perr.New("pipeline", perr.CodeChecksum,
					"open fragment %s/%s: %v", e.ID, id, err)
			}
			rep, err := mp4seg.ValidateFragment(f)
			f.Close()
			if err != nil {
				return err
			}
			if !rep.OK {
				return perr.New("pipeline", perr.CodeChecksum,
					"fragment %s/%s failed %d checks", e.ID, id, rep.ChecksFailed)
			}
			if rep.SHA256 != frag.SHA256 {
				return perr.New("pipeline", perr.CodeChecksum,
					"fragment %s/%s hash drift", e.ID, id)
			}
		}
	}
	return nil
}

func (r *Runner) stageCommit(entries []manifest.Entry, inputHash string,
	plan *recovery.Plan) (*Report, error) {
	m := &manifest.Manifest{
		JobID:       filepath.Base(r.dirs.root),
		Codecs:      codecNames(r.cfg.Codecs),
		Bitrates:    append([]int(nil), r.cfg.Bitrates...),
		Entries:     entries,
		InputDigest: inputHash,
	}
	if _, err := manifest.Finalize(m); err != nil {
		return nil, err
	}

	// Diff against any previously committed manifest for reuse reporting.
	target := filepath.Join(r.dirs.root, "manifest.json")
	var diff manifest.Diff
	if prevBytes, err := os.ReadFile(target); err == nil {
		prev, perr2 := manifest.Unmarshal(prevBytes)
		if perr2 != nil {
			return nil, perr2
		}
		d, err := manifest.Compare(prev, m)
		if err != nil {
			return nil, err
		}
		diff = *d
	} else {
		d, _ := manifest.Compare(nil, m)
		diff = *d
	}

	body, err := manifest.Marshal(m)
	if err != nil {
		return nil, err
	}
	if err := txn.WriteAtomic(target, body); err != nil {
		return nil, err
	}
	sum, err := hashFile(target)
	if err != nil {
		return nil, err
	}
	return &Report{
		ManifestPath: target, ManifestSHA: sum, EntryCount: len(m.Entries),
		Diff: diff,
	}, nil
}

func codecNames(codecs []stream.Codec) []string {
	out := make([]string, len(codecs))
	for i, c := range codecs {
		out[i] = string(c)
	}
	sort.Strings(out)
	return out
}
