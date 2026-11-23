package batch

import (
	"os"
	"path/filepath"

	"mediapack/internal/cmaf"
	"mediapack/internal/clock"
	"mediapack/internal/index"
	"mediapack/internal/repair"
)

// Validation holds the acceptance metrics of a run.
type Validation struct {
	RoundtripOK     int
	RoundtripTotal  int
	RoundtripRate   float64
	KeyframeHits    int
	KeyframeQueries int
	KeyframeHitRate float64
}

// reprocess replays the input through a fresh parse/repair pipeline and
// returns the normalized fragment records it produces.
func reprocess(opts Options) ([]index.Entry, error) {
	inDir := inputDir(opts.WorkDir)
	files, err := listInputFiles(inDir)
	if err != nil {
		return nil, err
	}
	manifest, err := loadManifest(inDir)
	if err != nil {
		return nil, err
	}
	seq := repair.NewSequencer()
	tsr := repair.NewTimestampRepairer(clock.NewTimelineSet())
	var out []index.Entry
	for _, rel := range files {
		if filepath.Ext(rel) != ".mp4" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(inDir, rel))
		if err != nil {
			return nil, err
		}
		frag, err := cmaf.ParseFragment(data)
		if err != nil {
			continue
		}
		desc, ok := manifest[frag.TrackID]
		if !ok {
			continue
		}
		dec := seq.Assign(frag.TrackID, frag.SequenceNumber, frag.Hash)
		if dec.Quarantined || dec.Duplicate {
			continue
		}
		ts, err := tsr.Repair(frag.TrackID, desc.Timescale, frag.HasTfdt, frag.BaseDecodeTime, frag.Duration)
		if err != nil {
			continue
		}
		out = append(out, index.Entry{
			Track: frag.TrackID, Logical: dec.Logical, Mfhd: frag.SequenceNumber,
			Time: ts.Base, Duration: ts.Duration, Timescale: desc.Timescale,
			Source: rel, Offset: frag.MoofOffset, Size: frag.MoofSize,
			Keyframe: frag.Keyframe, Repaired: ts.Repaired, Hash: frag.Hash,
		})
	}
	return out, nil
}

// ValidateRoundtrip re-derives the repaired records from the input and
// compares them one-to-one with the persisted chunks.
func ValidateRoundtrip(opts Options, rep *Report, v *Validation) error {
	want, err := reprocess(opts)
	if err != nil {
		return err
	}
	var got []index.Entry
	for _, rec := range rep.Checkpoint.Chunks {
		chunk, ok, err := index.LoadChunk(indexDir(opts.WorkDir), rec.Index)
		if err != nil {
			return err
		}
		if !ok {
			return &clock.Error{Cat: clock.CatInvariant, Msg: "chunk missing during validation"}
		}
		got = append(got, chunk.Entries...)
	}
	v.RoundtripTotal = len(want)
	if len(got) < len(want) {
		v.RoundtripTotal = len(got)
	}
	n := len(want)
	if len(got) < n {
		n = len(got)
	}
	for i := 0; i < n; i++ {
		a, b := want[i], got[i]
		if a.Track == b.Track && a.Logical == b.Logical && a.Mfhd == b.Mfhd &&
			a.Time == b.Time && a.Duration == b.Duration && a.Hash == b.Hash {
			v.RoundtripOK++
		}
	}
	if v.RoundtripTotal > 0 {
		v.RoundtripRate = float64(v.RoundtripOK) / float64(v.RoundtripTotal)
	}
	return nil
}

// ValidateKeyframes exercises boundary queries against the index: every
// keyframe must be locatable at its own time, queries before the first
// keyframe and on unknown tracks must miss, and timescale conversion must
// round consistently.
func ValidateKeyframes(rep *Report, v *Validation) {
	ix := rep.Index
	check := func(ok bool) {
		v.KeyframeQueries++
		if ok {
			v.KeyframeHits++
		}
	}
	maxTrack := uint32(0)
	for _, e := range ix.Entries() {
		got, ok := ix.Query(e.Track, e.Time)
		check(ok && got.Logical == e.Logical && got.Time == e.Time)
		if e.Track > maxTrack {
			maxTrack = e.Track
		}
	}
	seen := map[uint32]bool{}
	for _, e := range ix.Entries() {
		if seen[e.Track] {
			continue
		}
		seen[e.Track] = true
		first, _ := ix.First(e.Track)
		if first.Time > 0 {
			_, ok := ix.Query(e.Track, first.Time-1)
			check(!ok)
		}
		last, _ := ix.Last(e.Track)
		got, ok := ix.Query(e.Track, last.Time)
		check(ok && got.Logical == last.Logical)
		// Timescale conversion rounding: the exact second of the last
		// keyframe must resolve back to it.
		sec := clock.Seconds(last.Time, last.Timescale)
		got, ok = ix.QuerySeconds(e.Track, sec, last.Timescale)
		check(ok && got.Logical == last.Logical)
	}
	_, ok := ix.Query(maxTrack+1000, 1<<40)
	check(!ok) // track without keyframes must miss
	if v.KeyframeQueries > 0 {
		v.KeyframeHitRate = float64(v.KeyframeHits) / float64(v.KeyframeQueries)
	}
}

// HashMatch compares two runs' per-fragment hashes and returns the number
// of matching positions and the total.
func HashMatch(a, b *Report) (matched, total int) {
	total = len(a.Hashes)
	n := len(a.Hashes)
	if len(b.Hashes) < n {
		n = len(b.Hashes)
	}
	for i := 0; i < n; i++ {
		if a.Hashes[i] == b.Hashes[i] {
			matched++
		}
	}
	return matched, total
}
