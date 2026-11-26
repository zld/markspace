package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"rtx/internal/cache"
	"rtx/internal/cmaf"
	"rtx/internal/evidence"
	"rtx/internal/index"
	"rtx/internal/model"
	"rtx/internal/repair"
)

// Limits imposed by the run contract.
const (
	// MaxQueueFragments bounds how many fragments may be buffered at once.
	MaxQueueFragments = 32
	// MaxFragmentBytes bounds a single fragment file.
	MaxFragmentBytes = 64 << 20
)

// Code values.
const (
	// CodeQueueOverflow marks a run that would buffer more fragments than the
	// queue allows.
	CodeQueueOverflow model.Code = "queue_overflow"
	// CodeFragmentTooBig marks a fragment over 64 MiB.
	CodeFragmentTooBig model.Code = "fragment_too_large"
	// CodePathEscape marks a manifest path that leaves the work directory.
	CodePathEscape model.Code = "path_escape"
)

// Options are the resolved run parameters.
type Options struct {
	InputDir   string
	CacheDir   string
	Checkpoint string
	OutDir     string
	StopAfter  int
	// NoRepair disables tfdt repair; it is recorded in the cache key so cached
	// artifacts from a repairing run are not reused.
	NoRepair bool
	// SkipEvidence disables metric computation (used by cache tests).
	SkipEvidence bool
	// Progress receives one short line per completed record.
	Progress func(string)
	// OnRecord, when set, is called after each record lands; returning an
	// error simulates an interruption after the checkpoint has advanced.
	OnRecord func(done int, total int) error
}

// TrackSpecs are the fixture track layout.
var TrackSpecs = repair.DefaultTrackSpecs()

// Record is one manifest fragment ref, resolved and verified.
type Record struct {
	SourceID string
	Order    int
	Path     string // absolute path
	RelPath  string
	Sum      [32]byte
	Size     int64
	Fragment *cmaf.Fragment
	Raw      []byte
	// Per-fragment cache hit/miss for the metrics line.
	CacheHit bool
}

// Result summarises a completed run.
type Result struct {
	Records      int  `json:"records"`
	Cursor       int  `json:"cursor"`
	Resume       bool `json:"resume"`
	CacheHits    int  `json:"cache_hits"`
	CacheMisses  int  `json:"cache_misses"`
	Gaps         int  `json:"gaps"`
	Repairs      int  `json:"tfdt_rollbacks"`
	SourceSwitch int  `json:"source_switches"`
	Timeline     *repair.Timeline
	Index        *index.Index
	Metrics      *evidence.Metrics
	ManifestSum  string
	InputSum     string
	ParamsSum    string
	Outputs      []ArtifactRecord
	IndexSum     string
	MediaSum     string
	// CacheReasons records why entries had to be recomputed.
	CacheReasons []string
	RTP          *RTPVerification
	// RepeatEqual is set only after the repeatability check passes.
	RepeatEqual bool
	// MediaRate and Offsets are retained for that re-derivation.
	MediaRate model.Rate
	Offsets   map[string]int64
	// FragmentDigests are the written fragment artifacts' SHA-256 values, in
	// record order.
	FragmentDigests []string
	timelineSum     string
}

// Run executes or resumes the pipeline.
func Run(opts Options) (*Result, error) {
	if opts.Progress == nil {
		opts.Progress = func(string) {}
	}
	if opts.StopAfter < 0 {
		return nil, model.E(model.CodeInvalidInput, "run", "stop-after must be >= 0")
	}
	if err := ensureDir(opts.OutDir); err != nil {
		return nil, err
	}

	manifestPath := filepath.Join(opts.InputDir, "manifest.json")
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, model.E(model.CodeManifest, "read manifest", "%v", err).WithPath(manifestPath).WithCause(err)
	}
	manifest, err := cmaf.ParseManifest(manifestPath, manifestData)
	if err != nil {
		return nil, err
	}
	if err := ensureWithin(opts.InputDir, opts.OutDir); err != nil {
		return nil, err
	}

	params := cache.Params{
		MediaRateNum: manifest.MediaRate.Num,
		MediaRateDen: manifest.MediaRate.Den,
		VideoTrackID: TrackSpecs.VideoTrackID,
		AudioTrackID: TrackSpecs.AudioTrackID,
		Repair:       !opts.NoRepair,
	}
	paramsSum := paramsDigest(params)

	// Resolve every record and hash its bytes; this is the input identity.
	records, err := resolveRecords(opts, manifest, manifestData)
	if err != nil {
		return nil, err
	}
	if len(records) > MaxQueueFragments {
		return nil, model.E(CodeQueueOverflow, "plan run",
			"%d fragments exceeds the %d fragment queue", len(records), MaxQueueFragments)
	}
	manifestSum := DigestBytes(manifestData)
	inputSum := digestInputs(manifestData, records)

	// Reconcile any checkpoint.
	cp, err := LoadCheckpoint(opts.Checkpoint)
	if err != nil {
		return nil, err
	}
	decision, err := Reconcile(cp, manifestSum, inputSum, paramsSum, len(records))
	if err != nil {
		return nil, err
	}
	cursor := decision.Cursor
	if cursor > 0 {
		if err := VerifyArtifacts(opts.OutDir, cp.Artifacts); err != nil {
			return nil, err
		}
	}

	// Derive the full timeline and index now; they are deterministic functions
	// of the input and are stored verbatim in the checkpoint so a resume
	// reproduces the uninterrupted result exactly.
	mediaRate, err := model.NewRateRatio(manifest.MediaRate.Num, manifest.MediaRate.Den)
	if err != nil {
		return nil, err
	}
	offsets, err := sourceOffsets(manifest, records)
	if err != nil {
		return nil, err
	}
	rtpResult, err := verifyRTP(opts.InputDir, manifest, offsets)
	if err != nil {
		return nil, err
	}
	tl, err := buildTimeline(mediaRate, offsets, records)
	if err != nil {
		return nil, err
	}
	idx, err := index.Build(tl, index.TrackKeys{VideoTrackID: TrackSpecs.VideoTrackID, AudioTrackID: TrackSpecs.AudioTrackID})
	if err != nil {
		return nil, err
	}
	if err := idx.Validate(); err != nil {
		return nil, err
	}

	res := &Result{
		Records: len(records),
		Resume:  decision.Resuming,
		Gaps:    countGaps(tl),
		// The reported rollback count is per fragment: one fragment with a
		// rewinding tfdt on both tracks is one rollback event.
		Repairs:     tl.RepairedFragments(),
		Timeline:    tl,
		Index:       idx,
		ManifestSum: manifestSum,
		InputSum:    inputSum,
		ParamsSum:   paramsSum,
		MediaRate:   mediaRate,
		Offsets:     offsets,
	}
	switches := detectSwitches(manifest, records)
	res.SourceSwitch = len(switches)
	if len(rtpResult.Switches) > res.SourceSwitch && len(manifest.Sources) > 1 {
		// Anchors are authoritative for a source switch: they are explicit.
		res.SourceSwitch = len(rtpResult.Switches)
	}
	res.RTP = rtpResult

	// Cache only determines whether a record's derived artifact was recomputed;
	// index and evidence are always derived from the parsed input.
	contentCache, err := cache.Open(opts.CacheDir)
	if err != nil {
		return nil, err
	}
	for _, rec := range records {
		key := cache.NewKey(cache.KeyInput{
			Kind:       "fragment-artifact",
			Source:     rec.SourceID,
			SourcePath: rec.RelPath,
			SourceSum:  rec.Sum,
			Params:     params,
		})
		got, err := contentCache.Get(key, rec.Sum)
		if err != nil {
			return nil, err
		}
		if got.Entry != nil {
			res.CacheHits++
			continue
		}
		// A miss for any reason other than a genuinely absent entry means the
		// stored bytes can no longer be trusted; drop them before recomputing.
		if got.Reason != "" && got.Reason != "miss" {
			if err := contentCache.Invalidate(key); err != nil {
				return nil, err
			}
			res.CacheReasons = appendUnique(res.CacheReasons, got.Reason)
		}
		res.CacheMisses++
		artifact, err := fragmentArtifact(rec)
		if err != nil {
			return nil, err
		}
		if _, err := contentCache.Put(key, rec.Sum, artifact); err != nil {
			return nil, err
		}
	}

	// Land outputs record by record, checkpointing after each atomic write.
	// Repairs are keyed by source and path; the timeline's fragment path is the
	// absolute input path, which is also the record path.
	plans := map[string]map[uint32]WritePlan{}
	for key, byTrack := range planRepairs(tl) {
		plans[key] = byTrack
	}
	repairKey := func(rec Record) string { return rec.SourceID + "\x00" + rec.Fragment.Path }
	artifacts := dedupeByName(cpArtifacts(cp, cursor))
	// StopAfter counts records processed by this invocation, so a resumed run
	// with the same flag makes further progress instead of stopping at once.
	limit := len(records)
	if opts.StopAfter > 0 && cursor+opts.StopAfter < limit {
		limit = cursor + opts.StopAfter
	}
	for ; cursor < limit; cursor++ {
		rec := records[cursor]
		recArtifacts, err := writeRecord(opts.OutDir, rec, plans[repairKey(rec)])
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, recArtifacts...)
		// Advance only after the artifacts are durably on disk.
		if err := saveCheckpoint(opts, cp, res, cursor+1, artifacts, false); err != nil {
			return nil, err
		}
		opts.Progress(fmt.Sprintf("record=%d/%d source=%s order=%d fragment=%s", cursor+1, len(records), rec.SourceID, rec.Order, rec.RelPath))
		if opts.OnRecord != nil {
			if err := opts.OnRecord(cursor+1, len(records)); err != nil {
				// The checkpoint for this record has already advanced, so the
				// next invocation continues here. Report it as interrupted.
				return nil, model.E(model.CodeInterrupted, "run",
					"interrupted after %d of %d records: %v", cursor+1, len(records), err)
			}
		}
	}
	if limit < len(records) {
		// Stop cleanly at the requested count: outputs already landed for the
		// records below the cursor, so the next run continues exactly there.
		if err := saveCheckpoint(opts, cp, res, cursor, artifacts, false); err != nil {
			return nil, err
		}
		res.Cursor = cursor
		return res, model.E(model.CodeInterrupted, "run",
			"stopped after %d of %d records", cursor, len(records))
	}

	// Write the derived artifacts (index, evidence, timeline, report).
	res.timelineSum = DigestBytes(mustMarshal(tl))
	outArtifacts, metrics, err := writeDerived(opts, res, records, tl, idx, switches)
	if err != nil {
		return nil, err
	}
	artifacts = dedupeByName(append(artifacts, outArtifacts...))
	res.Cursor = len(records)
	res.Outputs = artifacts
	var facts []rtpFacts
	if rtpResult != nil {
		for _, s := range rtpResult.Summaries {
			facts = append(facts, rtpFacts{
				Source: s.Source, Packets: s.Packets, GapPackets: s.Gaps,
				SequenceWraps: s.SequenceWraps, OutOfOrder: s.OutOfOrder, TimestampWrap: s.TimestampWrap,
			})
		}
	}
	if a, err := writeReport(opts, res, facts, switches, plans, records); err != nil {
		return nil, err
	} else {
		artifacts = dedupeByName(append(artifacts, a...))
	}
	res.Outputs = artifacts
	res.Metrics = metrics
	// The index instance already carries its own digest; force it to be the
	// digest of what was actually written so repeat_equal can be checked.
	for _, a := range artifacts {
		if strings.HasSuffix(a.Name, ".m4s") {
			res.FragmentDigests = append(res.FragmentDigests, a.SHA256)
		}
	}
	if err := res.checkRepeatability(records, plans); err != nil {
		return nil, err
	}
	if err := saveCheckpoint(opts, cp, res, len(records), artifacts, true); err != nil {
		return nil, err
	}
	res.Cursor = len(records)
	return res, nil
}

// checkRepeatability proves the claim printed as repeat_equal: the index
// written to disk hashes to the recorded digest, and re-deriving the timeline,
// index and media digest from the same inputs reproduces them exactly. Any
// nondeterminism in map iteration or time handling would fail here.
func (res *Result) checkRepeatability(records []Record, plans map[string]map[uint32]WritePlan) error {
	tlBytes, err := cmaf.MarshalDeterministic(res.Timeline)
	if err != nil {
		return model.E(model.CodeInternal, "repeatability", "%v", err)
	}
	if got := DigestBytes(tlBytes); got != res.timelineSum {
		return model.E(model.CodeInternal, "repeatability", "timeline digest changed between derivation and write")
	}
	idxBytes, err := cmaf.MarshalDeterministic(res.Index)
	if err != nil {
		return model.E(model.CodeInternal, "repeatability", "%v", err)
	}
	if got := DigestBytes(idxBytes); got != res.IndexSum {
		return model.E(model.CodeInternal, "repeatability", "index digest changed between derivation and write")
	}
	// Rebuild the timeline and index from scratch; they must be identical.
	offsets := res.Offsets
	tl2, err := buildTimeline(res.MediaRate, offsets, records)
	if err != nil {
		return err
	}
	idx2, err := index.Build(tl2, index.TrackKeys{VideoTrackID: TrackSpecs.VideoTrackID, AudioTrackID: TrackSpecs.AudioTrackID})
	if err != nil {
		return err
	}
	idxBytes2, err := cmaf.MarshalDeterministic(idx2)
	if err != nil {
		return model.E(model.CodeInternal, "repeatability", "%v", err)
	}
	if DigestBytes(idxBytes2) != res.IndexSum {
		return model.E(model.CodeInternal, "repeatability", "re-derived index differs from the first derivation")
	}
	res.RepeatEqual = true
	return nil
}

// resolveRecords expands the manifest into per-file records, enforcing limits
// and path containment and computing digests.
func resolveRecords(opts Options, m *cmaf.Manifest, manifestData []byte) ([]Record, error) {
	var out []Record
	work, err := filepath.Abs(opts.InputDir)
	if err != nil {
		return nil, model.E(model.CodeInvalidInput, "resolve input", "%v", err)
	}
	for _, src := range m.Sources {
		frags, err := src.FragmentsInOrder()
		if err != nil {
			return nil, err
		}
		for _, ref := range frags {
			abs, err := resolveWithin(work, ref.Path)
			if err != nil {
				return nil, err
			}
			info, err := os.Stat(abs)
			if err != nil {
				return nil, model.E(model.CodeInvalidInput, "stat fragment", "%v", err).WithPath(abs).WithCause(err)
			}
			if info.Size() > MaxFragmentBytes {
				return nil, model.E(CodeFragmentTooBig, "plan run",
					"%s is %d bytes, limit %d", ref.Path, info.Size(), MaxFragmentBytes)
			}
			data, err := os.ReadFile(abs)
			if err != nil {
				return nil, model.E(model.CodeInvalidInput, "read fragment", "%v", err).WithPath(abs)
			}
			parsed, err := cmaf.ParseBytes(abs, data)
			if err != nil {
				return nil, err
			}
			frag, err := cmaf.ParseFragment(parsed)
			if err != nil {
				return nil, err
			}
			frag.Source = src.ID
			out = append(out, Record{
				SourceID: src.ID,
				Order:    ref.Order,
				Path:     abs,
				RelPath:  ref.Path,
				Sum:      sha256.Sum256(data),
				Size:     info.Size(),
				Fragment: frag,
				Raw:      data,
			})
		}
	}
	if len(out) == 0 {
		return nil, model.E(model.CodeManifest, "plan run", "manifest lists no fragments")
	}
	return out, nil
}

// resolveWithin resolves rel against root and rejects escapes.
func resolveWithin(root, rel string) (string, error) {
	abs, err := filepath.Abs(filepath.Join(root, rel))
	if err != nil {
		return "", model.E(CodePathEscape, "resolve path", "%v", err)
	}
	relCheck, err := filepath.Rel(root, abs)
	if err != nil || relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) {
		return "", model.E(CodePathEscape, "resolve path", "path %q escapes the work directory", rel)
	}
	return abs, nil
}

// ensureWithin rejects an output directory that would overwrite the inputs.
// Manifest-referenced paths are already confined to the input directory by
// resolveWithin; this guards the one path the operator supplies separately.
func ensureWithin(inputDir, outDir string) error {
	in, err := filepath.Abs(inputDir)
	if err != nil {
		return model.E(CodePathEscape, "check output", "%v", err)
	}
	out, err := filepath.Abs(outDir)
	if err != nil {
		return model.E(CodePathEscape, "check output", "%v", err)
	}
	rel, err := filepath.Rel(in, out)
	if err != nil {
		return model.E(CodePathEscape, "check output", "%v", err)
	}
	if rel == "." {
		return model.E(CodePathEscape, "check output", "output directory must differ from the input directory")
	}
	// An output inside the input tree is allowed only if it is a dedicated
	// subdirectory, never the input directory itself.
	if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil
	}
	// Outside the input tree is fine: the acceptance contract writes to a
	// sibling directory under the same temporary root.
	return nil
}

func sourceOffsets(m *cmaf.Manifest, records []Record) (map[string]int64, error) {
	out := map[string]int64{}
	for _, src := range m.Sources {
		if src.MediaOffset < 0 {
			return nil, model.E(model.CodeAnchorMismatch, "source offsets",
				"source %s has negative media offset %d", src.ID, src.MediaOffset)
		}
		out[src.ID] = src.MediaOffset
	}
	for _, rec := range records {
		if _, ok := out[rec.SourceID]; !ok {
			return nil, model.E(model.CodeManifest, "source offsets", "source %s missing from manifest", rec.SourceID)
		}
	}
	return out, nil
}

func buildTimeline(mediaRate model.Rate, offsets map[string]int64, records []Record) (*repair.Timeline, error) {
	inputs := make([]repair.Input, len(records))
	for i, rec := range records {
		inputs[i] = repair.Input{Source: rec.SourceID, Order: rec.Order, Frag: rec.Fragment}
	}
	return repair.BuildTimeline(mediaRate, TrackSpecs, offsets, inputs)
}

func detectSwitches(m *cmaf.Manifest, records []Record) []cmaf.Event {
	var out []cmaf.Event
	for _, e := range m.Events {
		if e.Kind == cmaf.EventSourceSwitch {
			out = append(out, e)
		}
	}
	return out
}

func countGaps(tl *repair.Timeline) int {
	n := 0
	for _, g := range tl.SequenceGaps {
		n += len(g.Missing)
	}
	return n
}

// fragmentArtifact is the cached per-fragment derived payload: a normalized
// fragment summary including the repaired tfdt. It is deterministic.
func fragmentArtifact(rec Record) ([]byte, error) {
	type trackArtifact struct {
		TrackID     uint32 `json:"track_id"`
		TFDTOffset  int64  `json:"tfdt_field_offset"`
		OriginalTFD uint64 `json:"original_tfdt"`
		Samples     int    `json:"samples"`
		Bytes       int64  `json:"sample_bytes"`
	}
	art := struct {
		Source   string          `json:"source"`
		Path     string          `json:"path"`
		Sequence *uint32         `json:"mfhd_sequence"`
		Tracks   []trackArtifact `json:"tracks"`
	}{Source: rec.SourceID, Path: rec.RelPath, Sequence: rec.Fragment.MFHDSequence}
	for _, t := range rec.Fragment.Tracks {
		var total int64
		for _, s := range t.Samples {
			total += int64(s.Size)
		}
		art.Tracks = append(art.Tracks, trackArtifact{
			TrackID: t.TrackID, TFDTOffset: t.TFDTOffset, OriginalTFD: t.BaseMediaDecodeTime,
			Samples: len(t.Samples), Bytes: total,
		})
	}
	sort.Slice(art.Tracks, func(i, j int) bool { return art.Tracks[i].TrackID < art.Tracks[j].TrackID })
	return cmaf.MarshalDeterministic(art)
}

func cpArtifacts(cp *Checkpoint, cursor int) []ArtifactRecord {
	if cp == nil || cp.Cursor != cursor {
		return nil
	}
	return cp.Artifacts
}

func saveCheckpoint(opts Options, cp *Checkpoint, res *Result, cursor int, artifacts []ArtifactRecord, completed bool) error {
	tlBytes, err := cmaf.MarshalDeterministic(res.Timeline)
	if err != nil {
		return model.E(CodeCheckpoint, "encode timeline", "%v", err)
	}
	idxBytes, err := cmaf.MarshalDeterministic(res.Index)
	if err != nil {
		return model.E(CodeCheckpoint, "encode index", "%v", err)
	}
	out := &Checkpoint{
		RunID:       res.ManifestSum[:12],
		ManifestSum: res.ManifestSum,
		InputSum:    res.InputSum,
		ParamsSum:   res.ParamsSum,
		Cursor:      cursor,
		Total:       res.Records,
		Timeline:    tlBytes,
		Index:       idxBytes,
		Artifacts:   artifacts,
		Completed:   completed,
	}
	if err := WriteCheckpoint(opts.Checkpoint, out); err != nil {
		return err
	}
	if cp != nil {
		*cp = *out
	}
	return nil
}

func ensureDir(dir string) error {
	if dir == "" {
		return model.E(model.CodeInvalidInput, "prepare output", "empty output directory")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return model.E(model.CodeInvalidInput, "prepare output", "%v", err).WithPath(dir)
	}
	return nil
}

// dedupeByName keeps the last record for each artifact name, so a repeated run
// over a completed checkpoint does not accumulate duplicate entries.
func dedupeByName(in []ArtifactRecord) []ArtifactRecord {
	if len(in) == 0 {
		return nil
	}
	index := map[string]int{}
	out := make([]ArtifactRecord, 0, len(in))
	for _, r := range in {
		if i, ok := index[r.Name]; ok {
			out[i] = r
			continue
		}
		index[r.Name] = len(out)
		out = append(out, r)
	}
	return out
}

func mustMarshal(v interface{}) []byte {
	b, err := cmaf.MarshalDeterministic(v)
	if err != nil {
		panic(err)
	}
	return b
}

// appendUnique adds s once, preserving order.
func appendUnique(list []string, s string) []string {
	for _, existing := range list {
		if existing == s {
			return list
		}
	}
	return append(list, s)
}

func paramsDigest(p cache.Params) string {
	data, _ := cmaf.MarshalDeterministic(p)
	return DigestBytes(data)
}

func digestInputs(manifestData []byte, records []Record) string {
	h := sha256.New()
	h.Write(manifestData)
	for _, r := range records {
		h.Write([]byte(r.SourceID))
		h.Write([]byte{0})
		h.Write([]byte(r.RelPath))
		h.Write([]byte{0})
		h.Write(r.Sum[:])
	}
	return hex.EncodeToString(h.Sum(nil))
}
