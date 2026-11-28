// Package engine orchestrates parsing, timeline construction and the
// downstream stages, persisting checkpoints and sealing a deterministic
// manifest.
package engine

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"mediaregress/internal/checkpoint"
	"mediaregress/internal/fingerprint"
	"mediaregress/internal/media"
	"mediaregress/internal/provenance"
	"mediaregress/internal/silence"
	"mediaregress/internal/split"
	"mediaregress/internal/timeline"
	"mediaregress/internal/track"
)

// Options selects the input and the output locations and tuning knobs.
type Options struct {
	Input            string
	CheckpointDir    string
	OutputPath       string
	SegmentTarget    media.Ticks
	SilenceWindow    media.Ticks
	SilenceThreshold silence.Threshold
	MinCompare       media.Ticks
	// InterruptAfterSegments simulates a process kill once that many segments
	// exist. The checkpoint written up to that point is what resume builds on.
	// Zero disables the simulation.
	InterruptAfterSegments int
}

// DefaultOptions returns the CLI defaults.
func DefaultOptions(input string) Options {
	return Options{
		Input:            input,
		SegmentTarget:    split.DefaultTarget,
		SilenceWindow:    silence.DefaultWindow,
		SilenceThreshold: 16,
		MinCompare:       silence.DefaultWindow,
	}
}

// TrackView is the manifest's per-track summary. It omits the full sample
// table, which would dominate the document, but keeps counts, digests and
// every metadata origin needed to explain the result.
type TrackView struct {
	ID           string                  `json:"id"`
	Container    string                  `json:"container"`
	Kind         track.Kind              `json:"kind"`
	Start        media.Ticks             `json:"start_ns"`
	End          media.Ticks             `json:"end_ns"`
	Samples      int                     `json:"sample_count"`
	Bytes        int64                   `json:"bytes"`
	Rotation     int                     `json:"rotation_deg"`
	RotSource    track.Source            `json:"rotation_source"`
	Crop         *track.Aperture         `json:"crop,omitempty"`
	Colors       []timeline.ColorSummary `json:"colors,omitempty"`
	Conflicts    []track.ColorConflict   `json:"color_conflicts,omitempty"`
	Metadata     []timeline.MetaOrigin   `json:"metadata"`
	Rejected     bool                    `json:"rejected,omitempty"`
	Truncated    bool                    `json:"truncated,omitempty"`
	InputDigest  string                  `json:"input_digest"`
	ParseVersion string                  `json:"parse_version"`
	// SourceStart is the byte offset of the container structure that defines
	// this track, so every field in the view can be located in the input.
	SourceStart int64 `json:"source_start"`
	// Errors are this track's structured diagnostics, so a rejected or
	// truncated track is explained in place as well as in the global list.
	Errors []*media.Error `json:"errors,omitempty"`
}

// Manifest is the sealed output document. Field order is fixed by the struct,
// slices are sorted before sealing, and no map is serialised, so equal results
// always produce byte-identical JSON.
type Manifest struct {
	Input         provenance.InputSummary `json:"input"`
	Tracks        []TrackView             `json:"tracks"`
	Segments      []split.Segment         `json:"segments"`
	SilenceRanges []silence.Range         `json:"silence_ranges"`
	Duplicates    []fingerprint.Group     `json:"duplicates"`
	Fingerprints  []fingerprint.Result    `json:"fingerprints"`
	Errors        []*media.Error          `json:"errors,omitempty"`
	Provenance    *provenance.Record      `json:"provenance"`
	Complete      bool                    `json:"complete"`
	OutputSHA256  string                  `json:"output_sha256"`
}

// Stats reports work that the CLI prints but that must not enter the sealed
// document, because a resumed run must hash the same as a fresh one.
type Stats struct {
	// ResumeSegments counts segments whose split result came from durable
	// state; ReusedSegments counts segments whose fingerprint did.
	ResumeSegments   int   `json:"resume_segments"`
	ReusedSegments   int   `json:"reused_segments"`
	ComputedSegments int   `json:"computed_segments"`
	ResumeMillis     int64 `json:"resume_ms"`
	FromCheckpoint   bool  `json:"from_checkpoint"`
}

// Result bundles the manifest with its run statistics.
type Result struct {
	Manifest *Manifest
	Stats    Stats
	// Fatal carries the earliest positioned input error, if any. The CLI maps
	// it to exit code 2.
	Fatal *media.Error
}

// Mode selects how much durable state a run may reuse.
type Mode int

const (
	// ModeRun reuses every completed interval in the checkpoint. It is what a
	// plain rerun and a resume both use; the difference between them is only
	// whether the previous run's output was sealed.
	ModeRun Mode = iota
	// ModeFresh ignores existing checkpoint state entirely.
	ModeFresh
)

// Process executes the full pipeline.
func Process(opts Options, mode Mode) (*Result, error) {
	opts = opts.withDefaults()
	if _, err := os.Stat(opts.Input); err != nil {
		return nil, media.WrapError(media.ClassCorruptInput, media.StageParse, "", 0, "input is unreadable", err)
	}
	in, parseErr := parseInput(opts.Input)
	if in == nil {
		return nil, parseErr
	}
	// Structural damage is reported, but readable metadata still produces a
	// manifest marked incomplete. The CLI exits 2 on Fatal.
	res := &Result{}
	fatal := earliestInputError(in)
	res.Fatal = fatal

	store := checkpoint.NewStore(opts.CheckpointDir)
	cp, err := store.Load()
	if err != nil {
		return nil, err
	}
	cfg := configDigest(opts)
	fromCheckpoint := cp != nil
	stats := Stats{FromCheckpoint: fromCheckpoint}
	if cp == nil {
		cp = &checkpoint.State{InputPath: opts.Input, InputDigest: in.Digest, InputSize: in.Size}
	}
	if cp.ConfigDigest != "" && cp.ConfigDigest != cfg {
		// Options changed (segment length, silence window, threshold): every
		// stored result was produced under different rules.
		cp = &checkpoint.State{InputPath: opts.Input, InputDigest: in.Digest, InputSize: in.Size}
		stats.FromCheckpoint = false
	}
	cp.ConfigDigest = cfg
	if cp.InputDigest != in.Digest || cp.InputSize != in.Size {
		// A different input voided the whole document; per-track invalidation
		// below decides what else can be kept.
		stats.FromCheckpoint = false
	}

	graph, err := track.BuildTrackLanes(in.Tracks)
	if err != nil {
		// No usable lane: still emit a diagnosable manifest.
		return finishEmpty(opts, store, in, fatal, err)
	}
	// Parse node state comes from the parser: each parsed track completed it,
	// rejected tracks are skipped by BuildTrackLanes.
	for _, tr := range in.Tracks {
		it := track.Item{Track: tr.ID, Node: track.NodeParse}
		if tr.Rejected {
			continue
		}
		if e := graph.Begin(it); e != nil {
			return nil, e
		}
		if e := graph.Complete(it); e != nil {
			return nil, e
		}
	}

	tl, tlErr := timeline.Build(in)
	// Timeline is a global prerequisite: mark every timeline node done only
	// when the merge succeeded for that track.
	for _, tt := range tl.Tracks {
		it := track.Item{Track: tt.TrackID, Node: track.NodeTimeline}
		if e := graph.Begin(it); e != nil {
			return nil, e
		}
		if e := graph.Complete(it); e != nil {
			return nil, e
		}
	}
	if tlErr != nil && len(tl.Tracks) == 0 {
		return finishEmpty(opts, store, in, fatal, tlErr)
	}

	// Per-track input digests drive selective invalidation.
	digests, derr := trackDigests(opts.Input, in)
	if derr != nil {
		return nil, derr
	}
	if mode == ModeFresh {
		cp.Tracks = nil
	}
	invalidated := cp.ApplyDigests(digests)
	cp.InputPath = opts.Input
	cp.InputDigest = in.Digest
	cp.InputSize = in.Size
	cp.Stage = media.StageSplit

	// Persist split results before heavy work so an interruption leaves the
	// completed segmentation durable. This is what makes resume meaningful.
	segments, segStats, err := produceSegments(opts, tl, graph, cp, mode)
	if err != nil {
		return nil, err
	}
	cp.Stage = media.StageSplit
	cp.Normalize()
	if err := store.Save(cp); err != nil {
		return nil, err
	}
	stats.ResumeSegments += segStats.reused()
	stats.ComputedSegments += segStats.computed

	silResults, silErrs, err := produceSilence(opts, tl, segments, graph, cp, mode)
	if err != nil {
		return nil, err
	}

	fps, fpErrs, fpStats, err := produceFingerprints(opts, tl, segments, graph, cp, mode, store)
	if err != nil {
		return nil, err
	}
	stats.ReusedSegments += fpStats.reused()
	stats.ComputedSegments += fpStats.computed

	// Surface the checkpoint invalidation itself: a reader of the manifest
	// should know that some results were recomputed rather than reused.
	allErrs := append([]*media.Error(nil), in.Errors...)
	for _, id := range invalidated {
		allErrs = append(allErrs, media.Errorf(media.ClassInputChanged, media.StageCheckpoint, id, -1,
			"track input changed; its indexes and downstream results were discarded"))
	}
	for _, tt := range tl.Tracks {
		allErrs = append(allErrs, tt.Errors...)
	}
	allErrs = append(allErrs, silErrs...)
	allErrs = append(allErrs, fpErrs...)
	allErrs = dedupeErrors(sortErrors(allErrs))

	man := &Manifest{
		Input: provenance.InputSummary{
			Path: opts.Input, Size: in.Size, SHA256: in.Digest,
			TrackCount: len(in.Tracks), RejectedCount: countRejected(in.Tracks),
		},
		Tracks:        trackViews(tl, digests, in),
		Segments:      segments,
		SilenceRanges: flattenSilence(silResults),
		Duplicates:    fingerprint.Duplicates(fps),
		Fingerprints:  fps,
		Errors:        allErrs,
		Complete:      fatal == nil && fpStats.complete && segStats.complete,
	}
	prov, err := buildProvenance(man, in, opts, stats)
	if err != nil {
		return nil, err
	}
	man.Provenance = prov
	// The provenance record seals the processed results (contentDigest), not
	// the whole envelope: the envelope contains the provenance record itself,
	// so sealing the whole document there would be circular.
	digest, err := contentDigest(man)
	if err != nil {
		return nil, err
	}
	man.Provenance.SetOutput("", string(digest), 0)
	if _, err := man.Provenance.Seal(); err != nil {
		return nil, err
	}
	if err := Seal(man); err != nil {
		return nil, err
	}

	// Persist checkpoint state, including the output digest so verify can tie
	// the document to the work that produced it.
	for _, tt := range tl.Tracks {
		st := cp.EnsureTrack(tt.TrackID, digests[tt.TrackID])
		st.OutputDigest = man.OutputSHA256
		if man.Complete {
			st.AddInterval(tt.Start, tt.End)
		}
	}
	cp.Stage = media.StageFingerprint
	cp.Normalize()
	if err := store.Save(cp); err != nil {
		return nil, err
	}
	// Write the manifest, then record its published digest in the checkpoint.
	// The document digest does not depend on the path, so recording it after
	// the write keeps verify and the checkpoint in agreement.
	if opts.OutputPath != "" {
		if err := WriteManifest(opts.OutputPath, man); err != nil {
			return nil, err
		}
	}
	res.Manifest = man
	res.Stats = stats
	return res, nil
}

// configDigest hashes the knobs that change results, so a checkpoint from a
// run with different options is not reused.
func configDigest(o Options) string {
	h := media.NewHasher()
	for _, v := range []int64{
		int64(o.SegmentTarget), int64(o.SilenceWindow),
		int64(o.SilenceThreshold), int64(o.MinCompare),
	} {
		h.Write([]byte(strconv.FormatInt(v, 10)))
		h.Write([]byte{0})
	}
	h.Write([]byte(track.ParseVersion))
	h.Write([]byte{0})
	h.Write([]byte(fingerprint.Algorithm))
	return string(h.Sum())
}

func (o Options) withDefaults() Options {
	if o.SegmentTarget <= 0 {
		o.SegmentTarget = split.DefaultTarget
	}
	if o.SilenceWindow <= 0 {
		o.SilenceWindow = silence.DefaultWindow
	}
	if o.MinCompare <= 0 {
		o.MinCompare = silence.DefaultWindow
	}
	return o
}

// parseInput dispatches on the leading bytes, not the file extension.
func parseInput(path string) (*track.Input, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, media.WrapError(media.ClassCorruptInput, media.StageParse, "", 0, "open input", err)
	}
	var head [4]byte
	n, _ := f.Read(head[:])
	f.Close()
	if n < 4 {
		return nil, media.NewError(media.ClassTruncatedInput, media.StageParse, "", int64(n),
			"input is shorter than a container signature")
	}
	if head[0] == 0x06 && head[1] == 0x0e && head[2] == 0x2b && head[3] == 0x34 {
		return track.ParseMXF(path)
	}
	return track.ParseMP4(path)
}

// earliestInputError picks the lowest-positioned input error. Container-level
// and track-level reports can describe the same damaged byte; the track-level
// one wins because it names the track whose data is unreadable.
func earliestInputError(in *track.Input) *media.Error {
	var errs []*media.Error
	for _, tr := range in.Tracks {
		errs = append(errs, tr.Errors...)
	}
	if off, first, ok := media.FirstOffset(errs); ok {
		for _, e := range in.Errors {
			if e.Offset >= 0 && e.Offset < off {
				return e
			}
		}
		return first
	}
	_, first, _ := media.FirstOffset(in.Errors)
	return first
}

func countRejected(tracks []*track.Track) int {
	n := 0
	for _, t := range tracks {
		if t.Rejected {
			n++
		}
	}
	return n
}

// trackDigests hashes each track's metadata and sample data so a changed track
// can be invalidated without discarding the others. The metadata half matters:
// if only the time table, rotation or crop changed, the sample bytes are the
// same but the segment windows would be stale.
func trackDigests(path string, in *track.Input) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, media.WrapError(media.ClassCorruptInput, media.StageParse, "", 0, "open input", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, media.WrapError(media.ClassCorruptInput, media.StageParse, "", 0, "stat input", err)
	}
	out := map[string]string{}
	buf := make([]byte, 64<<10)
	for _, tr := range in.Tracks {
		h := media.NewHasher()
		meta, err := json.Marshal(struct {
			ID        string
			Kind      track.Kind
			Timescale uint32
			Duration  media.Ticks
			Rotation  int
			Matrix    [9]int32
			Crop      *track.Aperture
			Colors    []track.ColorDescription
			Conflicts []track.ColorConflict
			Samples   []track.Sample
		}{
			ID: tr.ID, Kind: tr.Kind, Timescale: tr.Timescale, Duration: tr.Duration,
			Rotation: tr.Rotation, Matrix: tr.RotMatrix, Crop: tr.Crop,
			Colors: tr.Colors, Conflicts: tr.Conflicts, Samples: tr.Samples,
		})
		if err != nil {
			return nil, media.WrapError(media.ClassCheckpoint, media.StageCheckpoint, tr.ID, -1,
				"encode track metadata for digest", err)
		}
		h.Write(meta)
		for _, s := range tr.Samples {
			if s.Offset < 0 || s.Offset+int64(s.Size) > fi.Size() {
				break
			}
			if _, err := f.Seek(s.Offset, io.SeekStart); err != nil {
				return nil, media.WrapError(media.ClassCorruptInput, media.StageParse, tr.ID, s.Offset,
					"seek for track digest", err)
			}
			if _, err := io.CopyBuffer(h, io.LimitReader(f, int64(s.Size)), buf); err != nil {
				return nil, media.WrapError(media.ClassCorruptInput, media.StageParse, tr.ID, s.Offset,
					"read for track digest", err)
			}
		}
		out[tr.ID] = string(h.Sum())
	}
	return out, nil
}

func trackViews(tl *timeline.Timeline, digests map[string]string, in *track.Input) []TrackView {
	var views []TrackView
	byID := map[string]*track.Track{}
	for _, tr := range in.Tracks {
		byID[tr.ID] = tr
	}
	for _, tt := range tl.Tracks {
		var bytes int64
		for _, s := range tt.Samples {
			bytes += int64(s.Size)
		}
		v := TrackView{
			ID: tt.TrackID, Container: tt.Container, Kind: tt.Kind,
			Start: tt.Start, End: tt.End, Samples: len(tt.Samples), Bytes: bytes,
			Rotation: tt.Rotation, Crop: tt.Crop, Colors: tt.Colors,
			Conflicts: tt.Conflicts, Metadata: tt.Metadata,
			Truncated:    tt.Truncated,
			InputDigest:  digests[tt.TrackID],
			ParseVersion: tt.ParseVersion, Errors: tt.Errors,
		}
		if tr := byID[tt.TrackID]; tr != nil {
			v.RotSource = tr.RotSource
			v.SourceStart = tr.SourceStart
		}
		views = append(views, v)
	}
	// Rejected tracks stay visible in the manifest as diagnostics.
	for _, tt := range tl.Rejected {
		v := TrackView{
			ID: tt.TrackID, Container: tt.Container, Kind: tt.Kind,
			Rejected: true, ParseVersion: tt.ParseVersion,
			InputDigest: digests[tt.TrackID], Errors: tt.Errors,
		}
		views = append(views, v)
	}
	sort.Slice(views, func(i, j int) bool { return views[i].ID < views[j].ID })
	return views
}

func flattenSilence(results []silence.Result) []silence.Range {
	var out []silence.Range
	for _, r := range results {
		out = append(out, r.Silent...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Track != out[j].Track {
			return out[i].Track < out[j].Track
		}
		return out[i].Window.Start < out[j].Window.Start
	})
	return out
}

// dedupeErrors collapses identical reports. Container and track attribution
// can both describe the same byte; the manifest lists it once.
func dedupeErrors(errs []*media.Error) []*media.Error {
	out := make([]*media.Error, 0, len(errs))
	seen := map[string]bool{}
	for _, e := range errs {
		if e == nil {
			continue
		}
		key := e.Track + "|" + string(e.Stage) + "|" + string(e.Class) + "|" +
			itoa(e.Offset) + "|" + e.Message
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, e)
	}
	return out
}

func sortErrors(errs []*media.Error) []*media.Error {
	out := append([]*media.Error(nil), errs...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Track != out[j].Track {
			return out[i].Track < out[j].Track
		}
		if out[i].Offset != out[j].Offset {
			return out[i].Offset < out[j].Offset
		}
		if out[i].Stage != out[j].Stage {
			return out[i].Stage < out[j].Stage
		}
		return out[i].Class < out[j].Class
	})
	return out
}

// ContentSections is the part of a manifest the provenance seal covers.
type ContentSections struct {
	Input         provenance.InputSummary `json:"input"`
	Tracks        []TrackView             `json:"tracks"`
	Segments      []split.Segment         `json:"segments"`
	SilenceRanges []silence.Range         `json:"silence_ranges"`
	Duplicates    []fingerprint.Group     `json:"duplicates"`
	Fingerprints  []fingerprint.Result    `json:"fingerprints"`
	Errors        []*media.Error          `json:"errors,omitempty"`
	Complete      bool                    `json:"complete"`
}

// contentDigest hashes the processed results, excluding the provenance record
// and the document seal. Verify recomputes it to detect a doctored document
// whose outer seal was recomputed but whose content was altered.
func contentDigest(m *Manifest) (media.Digest, error) {
	b, err := json.Marshal(ContentSections{
		Input: m.Input, Tracks: m.Tracks, Segments: m.Segments,
		SilenceRanges: m.SilenceRanges, Duplicates: m.Duplicates,
		Fingerprints: m.Fingerprints, Errors: m.Errors, Complete: m.Complete,
	})
	if err != nil {
		return "", media.WrapError(media.ClassCheckpoint, media.StageProvenance, "", -1,
			"encode manifest content", err)
	}
	return media.DigestBytes(b), nil
}

// Seal computes the document digest over everything except the digest field.
func Seal(m *Manifest) error {
	m.OutputSHA256 = ""
	b, err := json.Marshal(m)
	if err != nil {
		return media.WrapError(media.ClassCheckpoint, media.StageProvenance, "", -1, "encode manifest", err)
	}
	m.OutputSHA256 = string(media.DigestBytes(b))
	return nil
}

// WriteManifest writes indented JSON atomically.
func WriteManifest(path string, m *Manifest) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return media.WrapError(media.ClassCheckpoint, media.StageProvenance, "", -1, "encode manifest", err)
	}
	b = append(b, '\n')
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return media.WrapError(media.ClassCheckpoint, media.StageProvenance, "", -1, "create output dir", err)
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".manifest-*.tmp")
	if err != nil {
		return media.WrapError(media.ClassCheckpoint, media.StageProvenance, "", -1, "create temp manifest", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return media.WrapError(media.ClassCheckpoint, media.StageProvenance, "", -1, "write manifest", err)
	}
	if err := tmp.Close(); err != nil {
		return media.WrapError(media.ClassCheckpoint, media.StageProvenance, "", -1, "close manifest", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return media.WrapError(media.ClassCheckpoint, media.StageProvenance, "", -1, "publish manifest", err)
	}
	return nil
}

// ReadManifest loads a manifest document.
func ReadManifest(path string) (*Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, media.WrapError(media.ClassOutputMismatch, media.StageProvenance, "", -1,
			"read manifest", err)
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, media.WrapError(media.ClassOutputMismatch, media.StageProvenance, "", -1,
			"decode manifest", err)
	}
	return &m, nil
}

func finishEmpty(opts Options, store *checkpoint.Store, in *track.Input, fatal *media.Error, cause error) (*Result, error) {
	man := &Manifest{
		Input: provenance.InputSummary{
			Path: opts.Input, Size: in.Size, SHA256: in.Digest,
			TrackCount: len(in.Tracks), RejectedCount: countRejected(in.Tracks),
		},
		Tracks:   trackViews(&timeline.Timeline{}, nil, in),
		Complete: false,
	}
	var errs []*media.Error
	errs = append(errs, in.Errors...)
	if e, ok := media.AsError(cause); ok {
		errs = append(errs, e)
	}
	man.Errors = dedupeErrors(sortErrors(errs))
	prov := provenance.New(man.Input,
		[]provenance.StageRecord{{Stage: media.StageParse, Version: track.ParseVersion}},
		nil, nil, nil)
	prov.MarkIncomplete()
	if _, err := prov.Seal(); err != nil {
		return nil, err
	}
	man.Provenance = prov
	man.Complete = false
	if err := Seal(man); err != nil {
		return nil, err
	}
	if opts.OutputPath != "" {
		if err := WriteManifest(opts.OutputPath, man); err != nil {
			return nil, err
		}
	}
	_ = store
	return &Result{Manifest: man, Fatal: fatal}, nil
}
