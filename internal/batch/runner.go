package batch

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"mediapack/internal/cmaf"
	"mediapack/internal/clock"
	"mediapack/internal/index"
	"mediapack/internal/model"
	"mediapack/internal/repair"
)

// Options configures a batch run.
type Options struct {
	WorkDir   string
	ChunkSize int
}

// Report summarizes a finished (or resumed) run.
type Report struct {
	InputHash       string
	Accepted        int
	Rejected        int
	Repaired        int
	Quarantined     int
	Duplicates      int
	Unrecoverable   int
	ReusedChunks    int
	RecoveredChunks int
	Warnings        []model.Warning
	Hashes          [][32]byte // per accepted fragment, in processing order
	Index           *index.Index
	Checkpoint      *Checkpoint
}

func inputDir(work string) string { return filepath.Join(work, "input") }
func indexDir(work string) string { return filepath.Join(work, "index") }

// listInputFiles returns the sorted relative paths of all regular files
// under dir. Symbolic links are rejected so nothing escapes the work dir.
func listInputFiles(dir string) ([]string, error) {
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, &clock.Error{Cat: clock.CatIO, Msg: "resolve input dir", Err: err}
	}
	var out []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return &clock.Error{Cat: clock.CatIO, Msg: "symbolic link rejected: " + path}
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

// HashInputDir computes the deterministic hash of all files under dir.
func HashInputDir(dir string) (string, error) {
	files, err := listInputFiles(dir)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			return "", err
		}
		sum := sha256.Sum256(data)
		fmt.Fprintf(h, "%s:%s\n", f, hex.EncodeToString(sum[:]))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func loadManifest(dir string) (map[uint32]model.TrackDesc, error) {
	data, err := os.ReadFile(filepath.Join(dir, "tracks.json"))
	if err != nil {
		return nil, &clock.Error{Cat: clock.CatIO, Msg: "read track manifest", Err: err}
	}
	var tracks []model.TrackDesc
	if err := json.Unmarshal(data, &tracks); err != nil {
		return nil, &clock.Error{Cat: clock.CatParse, Msg: "invalid track manifest", Err: err}
	}
	out := make(map[uint32]model.TrackDesc, len(tracks))
	for _, t := range tracks {
		out[t.ID] = t
	}
	return out, nil
}

// Run executes the batch pipeline over work/input, resuming from any
// valid checkpoint, and persists index chunks under work/index.
func Run(opts Options) (*Report, error) {
	if opts.ChunkSize <= 0 {
		return nil, fmt.Errorf("chunk size must be positive")
	}
	inDir, ixDir := inputDir(opts.WorkDir), indexDir(opts.WorkDir)
	files, err := listInputFiles(inDir)
	if err != nil {
		return nil, err
	}
	inputHash, err := HashInputDir(inDir)
	if err != nil {
		return nil, err
	}
	manifest, err := loadManifest(inDir)
	if err != nil {
		return nil, err
	}
	var frags []string
	for _, f := range files {
		if filepath.Ext(f) == ".mp4" {
			frags = append(frags, f)
		}
	}

	rep := &Report{InputHash: inputHash, Index: index.New()}
	seq := repair.NewSequencer()
	timelines := clock.NewTimelineSet()
	tsr := repair.NewTimestampRepairer(timelines)
	cp := &Checkpoint{InputHash: inputHash}

	// Resume: replay the longest prefix of fully validated chunks.
	start := 0
	if old, _ := LoadCheckpoint(opts.WorkDir); old != nil && old.InputHash == inputHash {
		for _, rec := range old.Chunks {
			chunk, ok, err := index.LoadChunk(ixDir, rec.Index)
			if err != nil {
				return nil, err
			}
			if !ok || !rec.Done || chunk.Checksum() != rec.SHA256 || len(chunk.Entries) != rec.Count {
				break
			}
			for _, e := range chunk.Entries {
				if err := replay(timelines, seq, rep, e); err != nil {
					return nil, err
				}
			}
			cp.Chunks = append(cp.Chunks, rec)
			start = rec.NextInput
		}
		rep.ReusedChunks = len(cp.Chunks)
		rep.RecoveredChunks = len(old.Chunks) - rep.ReusedChunks
	}

	pending := make([]index.Entry, 0, opts.ChunkSize)
	flush := func(nextInput int) error {
		if len(pending) == 0 {
			return nil
		}
		chunk := index.Chunk{Index: uint32(len(cp.Chunks)), Entries: pending}
		if err := index.WriteChunk(ixDir, chunk); err != nil {
			return err
		}
		cp.Chunks = append(cp.Chunks, ChunkRecord{
			Index: chunk.Index, Count: len(pending),
			NextInput: nextInput, SHA256: chunk.Checksum(), Done: true,
		})
		pending = pending[:0]
		return SaveCheckpoint(opts.WorkDir, cp)
	}

	for i := start; i < len(frags); i++ {
		rel := frags[i]
		data, err := os.ReadFile(filepath.Join(inDir, rel))
		if err != nil {
			return nil, err
		}
		frag, err := cmaf.ParseFragment(data)
		if err != nil {
			rep.Rejected++
			continue
		}
		desc, ok := manifest[frag.TrackID]
		if !ok {
			rep.Rejected++
			continue
		}
		dec := seq.Assign(frag.TrackID, frag.SequenceNumber, frag.Hash)
		switch {
		case dec.Quarantined:
			rep.Quarantined++
			continue
		case dec.Duplicate:
			rep.Duplicates++
			continue
		}
		ts, err := tsr.Repair(frag.TrackID, desc.Timescale, frag.HasTfdt, frag.BaseDecodeTime, frag.Duration)
		if err != nil {
			if clock.HasCategory(err, clock.CatUnrecoverable) {
				rep.Unrecoverable++
				rep.Rejected++
				continue
			}
			return nil, err
		}
		if ts.Repaired {
			rep.Repaired++
		}
		rec := index.Entry{
			Track: frag.TrackID, Logical: dec.Logical, Mfhd: frag.SequenceNumber,
			Time: ts.Base, Duration: ts.Duration, Timescale: desc.Timescale,
			Source: rel, Offset: frag.MoofOffset, Size: frag.MoofSize,
			Keyframe: frag.Keyframe, Repaired: ts.Repaired, Hash: frag.Hash,
		}
		if frag.Keyframe {
			if err := rep.Index.Add(rec); err != nil {
				return nil, err
			}
		}
		rep.Hashes = append(rep.Hashes, frag.Hash)
		rep.Accepted++
		pending = append(pending, rec)
		if len(pending) == opts.ChunkSize {
			if err := flush(i + 1); err != nil {
				return nil, err
			}
		}
	}
	if err := flush(len(frags)); err != nil {
		return nil, err
	}

	warns, err := normalizeManifest(manifest)
	if err != nil {
		return nil, err
	}
	rep.Warnings = warns
	if err := verifyMarkers(inDir, timelines); err != nil {
		return nil, err
	}
	cp.Complete = true
	if err := SaveCheckpoint(opts.WorkDir, cp); err != nil {
		return nil, err
	}
	rep.Checkpoint = cp
	if err := CheckInvariants(opts.WorkDir, rep); err != nil {
		return rep, err
	}
	return rep, nil
}

// replay reinstates one persisted record into the pipeline state.
func replay(timelines *clock.TimelineSet, seq *repair.Sequencer, rep *Report, e index.Entry) error {
	seq.Restore(e.Track, e.Mfhd, e.Logical, e.Hash)
	tl, err := timelines.Track(e.Track, e.Timescale)
	if err != nil {
		return err
	}
	if err := tl.Advance(e.Time, e.Duration); err != nil {
		return err
	}
	if e.Keyframe {
		if err := rep.Index.Add(e); err != nil {
			return err
		}
	}
	rep.Hashes = append(rep.Hashes, e.Hash)
	rep.Accepted++
	return nil
}

func normalizeManifest(manifest map[uint32]model.TrackDesc) ([]model.Warning, error) {
	ids := make([]int, 0, len(manifest))
	for id := range manifest {
		ids = append(ids, int(id))
	}
	sort.Ints(ids)
	var out []model.Warning
	for _, id := range ids {
		warns, err := model.Normalize(manifest[uint32(id)])
		if err != nil {
			return nil, err
		}
		out = append(out, warns...)
	}
	return out, nil
}

// verifyMarkers checks the RTP provenance markers of every track against
// the repaired timeline: one marker per accepted fragment, in order.
func verifyMarkers(inDir string, timelines *clock.TimelineSet) error {
	for _, tl := range timelines.Timelines() {
		path := filepath.Join(inDir, fmt.Sprintf("track-%04d", tl.TrackID), "markers.rtp")
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		markers, err := cmaf.ParseSourceMarkers(data)
		if err != nil {
			return err
		}
		if uint64(len(markers)) != tl.Fragments {
			return &clock.Error{Cat: clock.CatInvariant, Track: tl.TrackID,
				Msg: fmt.Sprintf("marker count %d != accepted fragments %d", len(markers), tl.Fragments)}
		}
		for i, m := range markers {
			if m.SSRC != tl.TrackID || uint32(m.Sequence) != uint32(i) {
				return &clock.Error{Cat: clock.CatInvariant, Track: tl.TrackID,
					Msg: fmt.Sprintf("marker %d out of order", i)}
			}
		}
	}
	return nil
}

// CheckInvariants verifies the post-run invariants: the index contains
// only monotonic keyframe entries and the checkpoint references only
// fully validated chunks.
func CheckInvariants(workDir string, rep *Report) error {
	last := map[uint32]int64{}
	for _, e := range rep.Index.Entries() {
		if !e.Keyframe {
			return &clock.Error{Cat: clock.CatInvariant, Track: e.Track, Msg: "non-keyframe in index"}
		}
		if prev, ok := last[e.Track]; ok && e.Time < prev {
			return &clock.Error{Cat: clock.CatInvariant, Track: e.Track, Msg: "index not monotonic"}
		}
		last[e.Track] = e.Time
	}
	if rep.Checkpoint != nil {
		for _, rec := range rep.Checkpoint.Chunks {
			chunk, ok, err := index.LoadChunk(indexDir(workDir), rec.Index)
			if err != nil {
				return err
			}
			if !rec.Done || !ok || chunk.Checksum() != rec.SHA256 {
				return &clock.Error{Cat: clock.CatInvariant,
					Msg: fmt.Sprintf("checkpoint references invalid chunk %d", rec.Index)}
			}
		}
	}
	return nil
}
