// Package pipeline wires ingestion, track normalization, segmentation,
// silence analysis, fingerprinting and the manifest into the three
// acceptance paths: normal, damaged and recovered.
package pipeline

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"mediamerge/internal/audio"
	"mediamerge/internal/fingerprint"
	"mediamerge/internal/fixgen"
	"mediamerge/internal/ingest"
	"mediamerge/internal/manifest"
	"mediamerge/internal/model"
	"mediamerge/internal/segment"
	"mediamerge/internal/track"
)

// Exit codes shared by the library and the CLI.
const (
	ExitOK       = 0
	ExitDamaged  = 2
	ExitBadInput = 1
)

// Options are the acceptance run parameters.
type Options struct {
	Seed       int64
	SegmentMs  int
	TargetBps  int64
	FixtureDir string
}

// Fixture is one acceptance path's inputs.
type Fixture struct {
	Name   string
	Files  map[string][]byte
	Expect int
}

// Suite is the result of all three paths.
type Suite struct {
	Options Options
	Paths   []manifest.PathRecord
	Doc     *manifest.Document
	Index   Index
}

// IndexEntry is the fingerprint report for one acceptance path.
type IndexEntry struct {
	Name   string             `json:"name"`
	Report fingerprint.Report `json:"report"`
}

// Index is the local fingerprint index covering all paths.
type Index struct {
	Schema string       `json:"schema"`
	Note   string       `json:"note"`
	Paths  []IndexEntry `json:"paths"`
}

// WriteFile persists the index to a local path.
func (ix Index) WriteFile(path string) error {
	b, err := json.MarshalIndent(ix, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// Generate writes the deterministic fixtures into dir and returns them.
func Generate(dir string, seed int64, segmentMs int) ([]Fixture, error) {
	fixtures := Fixtures(seed, segmentMs)
	for _, f := range fixtures {
		for _, name := range sortedNames(f.Files) {
			if err := os.WriteFile(filepath.Join(dir, name), f.Files[name], 0o644); err != nil {
				return nil, fmt.Errorf("pipeline: write fixture %s: %w", name, err)
			}
		}
	}
	return fixtures, nil
}

// Run processes fixtures and returns the aggregated manifest.
func Run(fixtures []Fixture, opts Options) *Suite {
	input := manifest.Input{Seed: opts.Seed, SegmentMs: opts.SegmentMs,
		TargetBps: opts.TargetBps}
	suite := &Suite{Options: opts, Index: Index{
		Schema: "mediamerge.fingerprint-index.v1",
		Note: "Local fingerprint index. Quick hashes group candidates; " +
			"duplicates are confirmed by strong and per-sample digests.",
	}}
	for _, fx := range fixtures {
		run, _ := runPath(fx, opts)
		input.Runs = append(input.Runs, run)
		suite.Index.Paths = append(suite.Index.Paths, IndexEntry{Name: fx.Name, Report: run.Fingerprints})
	}
	suite.Doc = manifest.Build(input)
	suite.Paths = suite.Doc.Paths
	return suite
}

// runPath processes one acceptance path and returns its manifest input and
// exit code: 0 clean, 2 damage locally isolated, 1 structural failure.
func runPath(fx Fixture, opts Options) (manifest.RunInput, int) {
	errs := &model.Errors{}
	run := manifest.RunInput{Name: fx.Name}

	var files []*ingest.File
	total := 0
	exit := ExitOK
	for _, name := range sortedNames(fx.Files) {
		data := fx.Files[name]
		total += len(data)
		if total > ingest.MaxTotalBytes {
			errs.Add(model.NewError(model.LevelFatal, "ingest", name, -1, 0,
				"input exceeds the 64 MiB total limit"))
			exit = ExitBadInput
			break
		}
		f, fatal := ingest.ParseFile(name, data, errs)
		if fatal != nil {
			exit = ExitBadInput
			continue
		}
		files = append(files, f)
	}

	var res *ingest.Result
	var segs segment.Result
	var reports []audio.Report
	var fps fingerprint.Report
	var norm *track.Normalized
	if exit != ExitBadInput {
		res = ingest.Recover(files, nil, errs)
		norm, _ = track.Normalize(files, track.BuildGraph(files), errs)

		maxDur := model.NsDuration(int64(opts.SegmentMs) * 1_000_000)
		segs = segment.Split(res.Samples, segment.Options{MaxDuration: maxDur})

		payload := payloadFunc(files)
		reports = analyzeAudio(norm, res.Samples, payload)
		fps = fingerprint.Build(segs.Segments, audioIDs(norm), payload, fingerprint.Options{})

		if len(res.IsolatedTracks) > 0 || errs.MaxLevel() >= model.LevelIsolated {
			exit = ExitDamaged
		}
	}

	isolated := isolatedTracks(errs, res)
	isoSet := make(map[uint32]bool, len(isolated))
	for _, id := range isolated {
		isoSet[id] = true
	}
	// An isolated track's metadata is sample-free noise: do not pretend to
	// have measured quality, and (for audio) do not claim it is mixable.
	if norm != nil && len(isolated) > 0 {
		iso := make(map[uint32]bool, len(isolated))
		for _, id := range isolated {
			iso[id] = true
		}
		for i := range norm.Tracks {
			if !iso[norm.Tracks[i].ID] {
				continue
			}
			if norm.Tracks[i].Handler == "soun" {
				norm.Tracks[i].Mixable = false
				if norm.Tracks[i].ChannelPlan != nil {
					norm.Tracks[i].ChannelPlan.Mixable = false
				}
			}
		}
		for i := range reports {
			if !iso[reports[i].TrackID] {
				continue
			}
			// No samples were kept for this track, so every window is
			// missing data rather than silence.
			reports[i] = scrambledReport(reports[i])
		}
	}
	recovered := map[uint32]bool{}
	var recoveredIDs []uint32
	if res != nil {
		for _, id := range res.RecoveredTracks {
			recovered[id] = true
		}
	}
	// A track can be repaired and later quarantined; isolation is the
	// stronger outcome, so do not report it as a successful recovery.
	for id := range isoSet {
		delete(recovered, id)
	}
	for id := range recovered {
		recoveredIDs = append(recoveredIDs, id)
	}
	sort.Slice(recoveredIDs, func(i, j int) bool { return recoveredIDs[i] < recoveredIDs[j] })

	if exit != fx.Expect {
		errs.Add(model.NewError(model.LevelFatal, "pipeline", fx.Name, -1, 0,
			"fixture %s expected exit %d, produced %d", fx.Name, fx.Expect, exit))
		exit = ExitBadInput
	}

	run.ExitCode = exit
	run.Segments = segs
	run.Audio = reports
	run.Fingerprints = fps
	run.Tracks = norm
	run.Errors = errs.All()
	run.Recovered = recoveredIDs
	run.Isolated = isolated
	if res != nil {
		run.Gaps = res.Gaps
	}
	return run, exit
}

// isolatedTracks merges tracks quarantined by recovery with tracks named by
// parse diagnostics, deduplicated and sorted.
func isolatedTracks(errs *model.Errors, res *ingest.Result) []uint32 {
	set := make(map[uint32]bool)
	if res != nil {
		for _, id := range res.IsolatedTracks {
			set[id] = true
		}
	}
	for _, e := range errs.All() {
		if e.Level == model.LevelIsolated && e.TrackID != 0 {
			set[e.TrackID] = true
		}
	}
	out := make([]uint32, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	if out == nil {
		out = []uint32{}
	}
	return out
}

// scrambledReport replaces an isolated track's analysis with an explicit
// all-missing result so no quality figure is attributed to it.
func scrambledReport(rep audio.Report) audio.Report {
	rep.Windows = nil
	rep.Intervals = nil
	rep.Stats = audio.Stats{Missing: 1}
	rep.Missing = []model.Interval{{}}
	return rep
}

func payloadFunc(files []*ingest.File) fingerprint.PayloadFunc {
	byPath := make(map[string]*ingest.File)
	for _, f := range files {
		byPath[f.Path] = f
	}
	return func(s model.Sample) ([]byte, bool) {
		f := byPath[s.SourceFile]
		if f == nil || s.ByteOffset < 0 {
			return nil, false
		}
		end := s.ByteOffset + int64(s.Size)
		if end > int64(len(f.Data)) {
			return nil, false
		}
		return f.Data[s.ByteOffset:end], true
	}
}

func analyzeAudio(norm *track.Normalized, samples []model.Sample, payload fingerprint.PayloadFunc) []audio.Report {
	if norm == nil {
		return nil
	}
	byTrack := make(map[uint32][]model.Sample)
	for _, s := range samples {
		byTrack[s.TrackID] = append(byTrack[s.TrackID], s)
	}
	var reports []audio.Report
	for _, md := range norm.Tracks {
		if md.Handler != "soun" {
			continue
		}
		reports = append(reports, audio.Analyze(md.ID, byTrack[md.ID], audio.PayloadFunc(payload),
			audio.Options{Window: model.NsDuration(100 * 1000 * 1000)}))
	}
	return reports
}

func audioIDs(norm *track.Normalized) []uint32 {
	if norm == nil {
		return nil
	}
	var ids []uint32
	for _, md := range norm.Tracks {
		if md.Handler == "soun" {
			ids = append(ids, md.ID)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func sortedNames(m map[string][]byte) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Fixture geometry. The three paths together span 30 s and carry
// 2,400,300 payload bytes, which is 640,080 bps at the default target.
const (
	audioTimescale = 48000
	videoTimescale = 90000
	ticksPerSecond = 1000 * 1000 * 1000

	audioSamplesPerFrag = 100 // 100 x 960 ticks = 2 s
	audioTickPerSample  = 960
	audioBytesPerSample = 960

	videoSamplesPerFrag = 60 // 60 x 3000 ticks = 2 s
	videoTickPerSample  = 3000
	videoBytesNormal    = 1067
	videoBytesDamaged   = 2667
	fragsPerPath        = 5
)

// Fixtures returns the three acceptance paths. The anomaly classes are
// spread across the damaged path (mfhd gap, rollback across the gap,
// truncated box, channel/sample-rate conflict) and the recovered path
// (rollback with a continuous chain, repaired locally).
func Fixtures(seed int64, segmentMs int) []Fixture {
	_ = seed
	_ = segmentMs
	return []Fixture{
		{Name: "normal", Expect: ExitOK, Files: normalFiles()},
		{Name: "damaged", Expect: ExitDamaged, Files: damagedFiles()},
		{Name: "recovered", Expect: ExitOK, Files: recoveredFiles()},
	}
}

func audioTrack(id uint32, ch uint16, rate uint32) fixgen.TrackSpec {
	return fixgen.TrackSpec{ID: id, Handler: "soun", Timescale: rate,
		Channels: ch, SampleRate: rate}
}

func videoTrack(id uint32, color *fixgen.ColorSpec) fixgen.TrackSpec {
	return fixgen.TrackSpec{ID: id, Handler: "vide", Timescale: videoTimescale,
		Width: 1920, Height: 1080, Color: color}
}

// pcmPattern builds little-endian PCM16 sample bytes for a content pattern.
func pcmPattern(pattern byte, parts int) []byte {
	out := make([]byte, parts*2)
	if pattern == 0 {
		return out
	}
	v := int16(1200 + int16(pattern)*700)
	for i := 0; i < parts; i++ {
		binary.LittleEndian.PutUint16(out[2*i:], uint16(v))
	}
	return out
}

func audioFrag(seq uint32, trackID uint32, tfdt uint64, pattern byte) fixgen.FragSpec {
	f := fixgen.FragSpec{Seq: seq, TrackID: trackID, TFDT: tfdt}
	for i := 0; i < audioSamplesPerFrag; i++ {
		f.Samples = append(f.Samples, fixgen.SampleSpec{
			Data: pcmPattern(pattern, audioBytesPerSample/2), Duration: audioTickPerSample,
		})
	}
	return f
}

func videoFrag(seq uint32, trackID uint32, tfdt uint64, bytesPerSample int) fixgen.FragSpec {
	f := fixgen.FragSpec{Seq: seq, TrackID: trackID, TFDT: tfdt}
	for i := 0; i < videoSamplesPerFrag; i++ {
		f.Samples = append(f.Samples, fixgen.SampleSpec{
			Data:     make([]byte, bytesPerSample),
			Duration: videoTickPerSample,
		})
	}
	return f
}

// normalFiles: healthy 10 s movie with one silent 2 s block and one
// content duplicate (segment 3 repeats segment 1's audio content).
func normalFiles() map[string][]byte {
	patterns := []byte{1, 2, 0, 1, 3} // segment 2 is all-zero = silence
	spec := fixgen.FileSpec{
		Tracks: []fixgen.TrackSpec{
			videoTrack(1, &fixgen.ColorSpec{Primaries: 1, Transfer: 1, Matrix: 1}),
			audioTrack(2, 2, audioTimescale),
		},
	}
	for i := 0; i < fragsPerPath; i++ {
		spec.Frags = append(spec.Frags,
			videoFrag(uint32(i+1), 1, uint64(i)*2*videoTimescale, videoBytesNormal))
		spec.Frags = append(spec.Frags,
			audioFrag(uint32(i+1), 2, uint64(i)*audioSamplesPerFrag*audioTickPerSample, patterns[i]))
	}
	return map[string][]byte{"normal.mp4": fixgen.Build(spec)}
}

// recoveredFiles: audio fragment 2 carries a rolled-back tfdt; the mfhd
// chain is continuous so recovery restores the expected decode time and
// keeps video/audio segment boundaries aligned.
func recoveredFiles() map[string][]byte {
	spec := fixgen.FileSpec{
		Tracks: []fixgen.TrackSpec{
			videoTrack(1, &fixgen.ColorSpec{Primaries: 1, Transfer: 1, Matrix: 1}),
			audioTrack(2, 2, audioTimescale),
		},
	}
	for i := 0; i < fragsPerPath; i++ {
		seq := uint32(i + 1)
		spec.Frags = append(spec.Frags,
			videoFrag(seq, 1, uint64(i)*2*videoTimescale, videoBytesNormal))
		tfdt := uint64(i) * audioSamplesPerFrag * audioTickPerSample
		if i == 1 {
			tfdt = 0 // rollback, repaired to 96000 by continuous durations
		}
		spec.Frags = append(spec.Frags,
			audioFrag(seq, 2, tfdt, byte(i+1)))
	}
	return map[string][]byte{"recovered.mp4": fixgen.Build(spec)}
}

// damagedFiles: the primary upload has a healthy video track and an audio
// track with an mfhd gap plus a rollback across the gap, which isolates it.
// The second upload declares the same audio track with a conflicting 6
// channel layout and a 44.1 kHz track whose trailing box is truncated.
func damagedFiles() map[string][]byte {
	spec := fixgen.FileSpec{
		Tracks: []fixgen.TrackSpec{
			videoTrack(1, &fixgen.ColorSpec{Primaries: 1, Transfer: 1, Matrix: 1}),
			audioTrack(2, 2, audioTimescale),
		},
	}
	for i := 0; i < fragsPerPath; i++ {
		spec.Frags = append(spec.Frags,
			videoFrag(uint32(i+1), 1, uint64(i)*2*videoTimescale, videoBytesDamaged))
	}
	spec.Frags = append(spec.Frags,
		audioFrag(1, 2, 0, 1),
		audioFrag(2, 2, audioSamplesPerFrag*audioTickPerSample, 2),
		audioFrag(5, 2, 100, 3)) // seq gap 2->5 and rollback across it

	extra := fixgen.FileSpec{
		Tracks: []fixgen.TrackSpec{
			audioTrack(2, 6, audioTimescale),
			audioTrack(3, 2, 44100),
		},
		Frags: []fixgen.FragSpec{
			audioFrag(1, 2, 0, 9),
			audioFrag(1, 3, 0, 4),
		},
	}
	full := fixgen.Build(extra)
	extra.TruncateAt = len(full) - 20 // cut inside track 3's trailing mdat
	return map[string][]byte{
		"damaged.mp4":      fixgen.Build(spec),
		"upload_extra.mp4": fixgen.Build(extra),
	}
}
