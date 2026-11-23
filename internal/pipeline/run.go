// Package pipeline wires parsing, scanning, alignment and verification into
// one resumable run. Stage cursors and input digests are persisted so an
// interrupted run continues rather than restarting, while any change to an
// input invalidates the cursor instead of silently reusing stale results.
package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"mediadeliver/internal/align"
	"mediadeliver/internal/dash"
	"mediadeliver/internal/hls"
	"mediadeliver/internal/model"
	"mediadeliver/internal/segment"
)

// Stage names used by the cursor.
const (
	StageParse  = "parse"
	StageScan   = "scan"
	StageAlign  = "align"
	StageVerify = "verify"
	StageDone   = "done"
)

// stageRank orders stages; cursors store the last completed stage.
func stageRank(stage string) int {
	switch stage {
	case StageParse:
		return 1
	case StageScan:
		return 2
	case StageAlign:
		return 3
	case StageVerify:
		return 4
	case StageDone:
		return 5
	default:
		return 0
	}
}

// Options configures one run.
type Options struct {
	WorkDir      string
	MPDPath      string
	HLSPath      string
	AlignPath    string            // optional pre-aligned JSON; empty means derive
	Media        map[string][]byte // representation ID -> media bytes
	ManifestPath string
	InterruptAt  string // stage name to stop before, for tests
	Resume       bool   // honour an existing cursor; false forces a full run
	// MediaPaths maps representation IDs to on-disk media. When set, files
	// are hashed and scanned by streaming (Media may then be nil).
	MediaPaths map[string]string
}

// Run executes the pipeline, honouring any cursor already present.
func Run(opts Options) (*Outcome, error) {
	started := time.Now()
	if err := ensureInside(opts.WorkDir, opts.ManifestPath); err != nil {
		return nil, err
	}
	entries, mpdData, hlsData, err := hashInputs(opts)
	if err != nil {
		return nil, err
	}
	digest := inputDigest(entries)
	cp, err := loadCheckpoint(opts.WorkDir)
	if err != nil {
		return nil, model.Wrap(err, model.CodeResumeInconsistent, checkpointPath(opts.WorkDir),
			model.NoOffset, model.StageResume, false)
	}
	if !opts.Resume {
		cp = nil
	}
	resumed := cp != nil
	if cp != nil && cp.InputDigest != digest {
		return nil, model.NewError(model.CodeInputChanged, checkpointPath(opts.WorkDir),
			model.NoOffset, model.StageResume, false,
			"input digest changed since the checkpoint was written")
	}

	out := &Outcome{Stage: StageParse, Resumed: resumed}
	mp, hlsPl, diags, stageErr := parseStage(opts, mpdData, hlsData)
	out.Diagnostics = append(out.Diagnostics, diags...)
	cp = newCursor(digest, cp, resumed)
	if stageErr != nil {
		cp.Stage = StageParse
		out.Stage = StageParse
		return finish(out, cp, opts, diags, started)
	}
	if interrupted(opts, StageScan) {
		cp.Stage = StageScan
		out.Stage = StageScan
		return finish(out, cp, opts, diags, started)
	}

	scans, scanDiags := scanStage(opts, mp, cp)
	out.Diagnostics = append(out.Diagnostics, scanDiags...)
	if interrupted(opts, StageAlign) {
		cp.Stage = StageAlign
		out.Stage = StageAlign
		return finish(out, cp, opts, out.Diagnostics, started)
	}

	manifest := align.Build(align.Input{
		MPD: mp, Playlist: hlsPl, Scans: scans, Inputs: toInputRefs(entries),
	})
	out.Diagnostics = append(out.Diagnostics, manifest.Errors...)
	if interrupted(opts, StageVerify) {
		cp.Stage = StageVerify
		out.Stage = StageVerify
		return finish(out, cp, opts, out.Diagnostics, started)
	}

	if !manifest.Summary.Deliverable {
		out.Manifest = manifest
		cp.Stage = StageVerify
		out.Stage = StageVerify
		return finish(out, cp, opts, out.Diagnostics, started)
	}
	if errs := Verify(manifest); len(errs) > 0 {
		out.Diagnostics = append(out.Diagnostics, errs...)
		out.Manifest = manifest
		cp.Stage = StageVerify
		out.Stage = StageVerify
		return finish(out, cp, opts, out.Diagnostics, started)
	}
	manifest.Summary.Diagnostics = len(out.Diagnostics)
	published, sha, err := publish(manifest, opts, cp, out.Diagnostics)
	if err != nil {
		return nil, err
	}
	if !published {
		out.Manifest = manifest
		out.Stage = StageVerify
		return finish(out, cp, opts, out.Diagnostics, started)
	}
	out.Manifest = manifest
	out.Published = true
	out.ManifestPath = opts.ManifestPath
	out.ManifestSHA = sha
	out.Stage = StageDone
	cp.Stage = StageDone
	out.SegmentsCounted = segmentsCounted(cp)
	out.ResumeMillis = time.Since(started).Milliseconds()
	return out, nil
}

func finish(out *Outcome, cp *Checkpoint, opts Options, diags []*model.Error, started time.Time) (*Outcome, error) {
	model.SortErrors(diags)
	out.Diagnostics = diags
	if out.Manifest != nil {
		out.Manifest.Errors = diags
	}
	if err := saveCheckpoint(opts.WorkDir, cp); err != nil {
		return nil, err
	}
	out.SegmentsCounted = segmentsCounted(cp)
	out.ResumeMillis = time.Since(started).Milliseconds()
	return out, nil
}

func newCursor(digest string, old *Checkpoint, resumed bool) *Checkpoint {
	if resumed && old != nil {
		return old
	}
	return &Checkpoint{InputDigest: digest, Scans: map[string]ScanRecord{}}
}

func interrupted(opts Options, stage string) bool {
	return opts.InterruptAt != "" && opts.InterruptAt == stage
}

func parseStage(opts Options, mpdData, hlsData []byte) (*dash.MPD, *hls.Playlist, []*model.Error, error) {
	mp, err := dash.Parse(mpdData, opts.MPDPath)
	var diags []*model.Error
	if err != nil {
		e := model.Wrap(err, model.CodeMPDInvalid, opts.MPDPath, model.NoOffset, model.StageParse, false)
		diags = append(diags, e)
		return nil, nil, diags, e
	}
	diags = append(diags, mp.Errors...)
	pl, err := hls.Parse(hlsData, opts.HLSPath)
	if err != nil {
		e := model.Wrap(err, model.CodeHLSInvalid, opts.HLSPath, model.NoOffset, model.StageParse, false)
		diags = append(diags, e)
		return mp, nil, diags, e
	}
	diags = append(diags, pl.Errors...)
	return mp, pl, diags, nil
}

func scanStage(opts Options, mp *dash.MPD, cp *Checkpoint) ([]align.Scan, []*model.Error) {
	var scans []align.Scan
	var diags []*model.Error
	reps := append([]*dash.Representation(nil), mp.Reps...)
	sort.SliceStable(reps, func(i, j int) bool { return reps[i].ID < reps[j].ID })
	for _, rep := range reps {
		path, digestHex, ok := mediaIdentity(opts, rep.ID)
		if !ok {
			continue
		}
		if stageRank(cp.Stage) >= stageRank(StageAlign) {
			if rec, ok := cp.Scans[rep.ID]; ok && rec.SHA256 == digestHex {
				// The scan result is reused verbatim: the fragments in it
				// are not counted or parsed a second time.
				scans = append(scans, align.Scan{RepID: rep.ID, Path: rec.Path, Digest: digestHex,
					Result: recordToResult(rec)})
				continue
			}
		}
		res, err := scanRepresentation(opts, rep.ID, path)
		if err != nil {
			diags = append(diags, model.Wrap(err, model.CodeTruncatedBox, path, model.NoOffset, model.StageScan, true))
			continue
		}
		rec := resultToRecord(res)
		rec.SHA256 = digestHex
		cp.Scans[rep.ID] = rec
		diags = append(diags, res.Errors...)
		scans = append(scans, align.Scan{RepID: rep.ID, Path: res.Path, Digest: digestHex, Result: res})
	}
	cp.Stage = StageAlign
	return scans, diags
}

// mediaIdentity returns the representation's media path and content hash,
// preferring on-disk streaming over an in-memory copy.
func mediaIdentity(opts Options, repID string) (string, string, bool) {
	if path, ok := opts.MediaPaths[repID]; ok && path != "" {
		sha, _, err := hashFile(path)
		if err != nil {
			return "", "", false
		}
		return path, sha, true
	}
	data, ok := opts.Media[repID]
	if !ok {
		return "", "", false
	}
	sum := sha256.Sum256(data)
	return mediaPath(opts.WorkDir, repID), hex.EncodeToString(sum[:]), true
}

func scanRepresentation(opts Options, repID, path string) (*segment.Result, error) {
	if _, ok := opts.MediaPaths[repID]; ok && path != "" {
		return segment.ScanFile(path)
	}
	data := opts.Media[repID]
	return segment.Scan(newByteSeeker(data), path, int64(len(data)))
}

// segmentsCounted is derived from the checkpoint rather than accumulated,
// so a resumed run cannot double count fragments already scanned.
func segmentsCounted(cp *Checkpoint) int {
	total := 0
	for _, rec := range cp.Scans {
		total += len(rec.Segments)
	}
	return total
}

func resultToRecord(r *segment.Result) ScanRecord {
	return ScanRecord{
		Path: r.Path, Size: r.Size, ScannedTo: r.ScannedTo, IndexFound: r.IndexFound,
		SampleCount: r.SampleCount, Segments: r.Segments,
	}
}

func recordToResult(rec ScanRecord) *segment.Result {
	return &segment.Result{Path: rec.Path, Size: rec.Size, Segments: rec.Segments,
		ScannedTo: rec.ScannedTo, IndexFound: rec.IndexFound, SampleCount: rec.SampleCount}
}

func toInputRefs(entries []InputEntry) []align.InputRef {
	out := make([]align.InputRef, 0, len(entries))
	for _, e := range entries {
		out = append(out, align.InputRef{Role: e.Role, Representation: e.Representation,
			Path: e.Path, SHA256: e.SHA256, Size: e.Size})
	}
	return out
}

func publish(m *align.Manifest, opts Options, cp *Checkpoint, diags []*model.Error) (bool, string, error) {
	// Paths are reported relative to the work directory so the manifest is
	// byte-identical across runs, machines and checkout locations.
	for i := range m.Representations {
		m.Representations[i].MediaPath = relativize(opts.WorkDir, m.Representations[i].MediaPath)
		m.Representations[i].SourcePath = relativize(opts.WorkDir, m.Representations[i].SourcePath)
	}
	for i := range m.Intervals {
		for j := range m.Intervals[i].Errors {
			m.Intervals[i].Errors[j].Path = relativize(opts.WorkDir, m.Intervals[i].Errors[j].Path)
		}
	}
	for i := range diags {
		diags[i].Path = relativize(opts.WorkDir, diags[i].Path)
	}
	if len(diags) == 0 {
		m.Errors = nil // empty diagnostics serialise as absent, not []
	} else {
		m.Errors = diags
	}
	m.Summary.Diagnostics = len(diags)
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return false, "", err
	}
	data = append(data, '\n')
	if err := writeAtomic(opts.ManifestPath, data); err != nil {
		return false, "", err
	}
	sha := sha256.Sum256(data)
	cp.Stage = StageDone
	if err := saveCheckpoint(opts.WorkDir, cp); err != nil {
		return false, "", err
	}
	return true, hex.EncodeToString(sha[:]), nil
}

// relativize makes a path work-directory relative when possible.
func relativize(work, path string) string {
	if path == "" {
		return ""
	}
	if rel, err := filepath.Rel(work, path); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(path)
}

func hashInputs(opts Options) ([]InputEntry, []byte, []byte, error) {
	var entries []InputEntry
	mpdData, err := os.ReadFile(opts.MPDPath)
	if err != nil {
		return nil, nil, nil, model.Wrap(err, model.CodeMPDInvalid, opts.MPDPath, model.NoOffset, model.StageParse, false)
	}
	hlsData, err := os.ReadFile(opts.HLSPath)
	if err != nil {
		return nil, nil, nil, model.Wrap(err, model.CodeHLSInvalid, opts.HLSPath, model.NoOffset, model.StageParse, false)
	}
	mpdRel, _ := filepath.Rel(opts.WorkDir, opts.MPDPath)
	hlsRel, _ := filepath.Rel(opts.WorkDir, opts.HLSPath)
	mpdHash := sha256.Sum256(mpdData)
	hlsHash := sha256.Sum256(hlsData)
	entries = append(entries,
		InputEntry{Role: "mpd", Path: mpdRel, SHA256: hex.EncodeToString(mpdHash[:]), Size: int64(len(mpdData))},
		InputEntry{Role: "hls", Path: hlsRel, SHA256: hex.EncodeToString(hlsHash[:]), Size: int64(len(hlsData))})
	// MediaPaths wins for a representation; Media covers in-memory fixtures.
	ids := map[string]bool{}
	for id := range opts.Media {
		ids[id] = true
	}
	for id, path := range opts.MediaPaths {
		if path != "" {
			ids[id] = true
		}
	}
	ordered := make([]string, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	for _, id := range ordered {
		if path := opts.MediaPaths[id]; path != "" {
			sha, size, err := hashFile(path)
			if err != nil {
				return nil, nil, nil, model.Wrap(err, model.CodeTruncatedBox, path, model.NoOffset, model.StageParse, false)
			}
			entries = append(entries, InputEntry{Role: "media", Representation: id,
				Path: "inputs/" + id + ".mp4", SHA256: sha, Size: size})
			continue
		}
		data := opts.Media[id]
		h := sha256.Sum256(data)
		entries = append(entries, InputEntry{Role: "media", Representation: id,
			Path: "inputs/" + id + ".mp4", SHA256: hex.EncodeToString(h[:]), Size: int64(len(data))})
	}
	return entries, mpdData, hlsData, nil
}

// WriteInputs materialises MPD, playlist and media bytes under work/inputs so
// the run hashes real files and a resumed process sees identical inputs.
//
// The files are always rewritten so a work directory reflects the current
// fixtures. The pipeline still refuses to reuse a cursor whose recorded
// input digest differs from the bytes on disk.
func WriteInputs(work string, mpd, playlist []byte, media map[string][]byte) (string, string, error) {
	dir := filepath.Join(work, "inputs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	mpdPath := filepath.Join(dir, "manifest.mpd")
	plPath := filepath.Join(dir, "media.m3u8")
	if err := writeAtomic(mpdPath, mpd); err != nil {
		return "", "", err
	}
	if err := writeAtomic(plPath, playlist); err != nil {
		return "", "", err
	}
	ids := make([]string, 0, len(media))
	for id := range media {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := writeAtomic(filepath.Join(dir, id+".mp4"), media[id]); err != nil {
			return "", "", err
		}
	}
	return mpdPath, plPath, nil
}

// ensureInside rejects manifest paths outside the work directory.
func ensureInside(work, target string) error {
	if target == "" {
		return nil
	}
	rel, err := filepath.Rel(work, target)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return model.NewError(model.CodePathEscape, target, model.NoOffset, model.StagePublish, false,
			"manifest path escapes the work directory")
	}
	return nil
}

// Verify checks the manifest's cross-cutting invariants. It is deliberately
// independent of the aligner: it only trusts the published data, so the
// verify command catches a manifest edited after delivery.
func Verify(m *align.Manifest) []*model.Error {
	var errs []*model.Error
	if m == nil {
		return []*model.Error{model.NewError(model.CodeManifestInvalid, "", model.NoOffset,
			model.StageVerify, false, "no manifest")}
	}
	if m.Schema != align.Schema {
		errs = append(errs, model.NewError(model.CodeManifestInvalid, "", model.NoOffset,
			model.StageVerify, false, "unexpected manifest schema "+m.Schema))
	}
	if len(m.Representations) == 0 || len(m.Intervals) == 0 {
		errs = append(errs, model.NewError(model.CodeManifestInvalid, "", model.NoOffset,
			model.StageVerify, false, "manifest has no representations or intervals"))
	}
	mpdDigest := ""
	mediaDigest := map[string]string{}
	for _, in := range m.Inputs {
		switch in.Role {
		case "mpd":
			mpdDigest = in.SHA256
		case "media":
			mediaDigest[in.Representation] = in.SHA256
		}
	}
	// Per-representation ordering state, keyed by discontinuity sequence.
	state := map[string]*repState{}
	prevEnd := int64(-1)
	for _, iv := range m.Intervals {
		if iv.StartNS >= iv.EndNS {
			errs = append(errs, model.NewError(model.CodeManifestInvalid, "", iv.StartNS,
				model.StageVerify, false, fmt.Sprintf("interval %d has a non-positive span", iv.Index)))
		}
		if iv.StartNS < prevEnd {
			errs = append(errs, model.NewError(model.CodeTimeRegression, "", iv.StartNS,
				model.StageVerify, false, fmt.Sprintf("interval %d starts before the previous interval ends", iv.Index)))
		}
		prevEnd = iv.EndNS
		for i := 1; i < len(iv.HLSMediaSequences); i++ {
			if iv.HLSMediaSequences[i] <= iv.HLSMediaSequences[i-1] {
				errs = append(errs, model.NewError(model.CodeSequenceNotInc, "", model.NoOffset,
					model.StageVerify, false, "HLS media sequence is not strictly increasing"))
			}
		}
		for _, e := range iv.Entries {
			if err := verifyEntry(e, iv, mpdDigest, mediaDigest, state); err != nil {
				errs = append(errs, err)
			}
		}
	}
	model.SortErrors(errs)
	return errs
}

// repState tracks one representation's ordering across intervals.
type repState struct {
	lastEnd  int64
	lastSeq  uint64
	lastDisc uint64
	seenDisc bool
}

func verifyEntry(e align.Entry, iv align.Interval, mpdDigest string, mediaDigest map[string]string, state map[string]*repState) *model.Error {
	if e.MPDDigest == "" || (mpdDigest != "" && e.MPDDigest != mpdDigest) {
		return model.NewError(model.CodeUntraceable, e.RepresentationID, e.SourceOffset,
			model.StageVerify, false, "entry does not carry the MPD digest")
	}
	if want, ok := mediaDigest[e.RepresentationID]; ok && e.MediaDigest != want {
		return model.NewError(model.CodeUntraceable, e.RepresentationID, e.MediaOffset,
			model.StageVerify, false, "entry does not carry the media input digest")
	}
	if e.StartNS >= e.EndNS {
		return model.NewError(model.CodeManifestInvalid, e.RepresentationID, e.StartNS,
			model.StageVerify, false, "entry has a non-positive span")
	}
	if e.DriftNS > align.MaxDriftNS && e.Deliverable {
		return model.NewError(model.CodeBoundaryDrift, e.RepresentationID, e.SourceOffset,
			model.StageVerify, false, fmt.Sprintf("entry drifts %dns but is marked deliverable", e.DriftNS))
	}
	st := state[e.RepresentationID]
	if st == nil {
		st = &repState{}
		state[e.RepresentationID] = st
	}
	if e.StartNS < st.lastEnd {
		return model.NewError(model.CodeTimeRegression, e.RepresentationID, e.SourceOffset,
			model.StageVerify, false, "entry starts before the previous entry of the same representation ends")
	}
	st.lastEnd = e.EndNS
	if len(e.SegmentNumbers) > 0 {
		first := e.SegmentNumbers[0]
		if st.seenDisc && st.lastDisc == iv.DiscontinuitySeq && first <= st.lastSeq {
			return model.NewError(model.CodeSequenceNotInc, e.RepresentationID, e.SourceOffset,
				model.StageVerify, false,
				fmt.Sprintf("segment %d follows %d inside discontinuity %d", first, st.lastSeq, iv.DiscontinuitySeq))
		}
		for i := 1; i < len(e.SegmentNumbers); i++ {
			if e.SegmentNumbers[i] <= e.SegmentNumbers[i-1] {
				return model.NewError(model.CodeSequenceNotInc, e.RepresentationID, e.SourceOffset,
					model.StageVerify, false, "segment numbers are not strictly increasing")
			}
		}
		st.lastSeq = e.SegmentNumbers[len(e.SegmentNumbers)-1]
		st.lastDisc = iv.DiscontinuitySeq
		st.seenDisc = true
	}
	if e.SourceOffset == model.NoOffset && !e.Corrupt {
		return model.NewError(model.CodeUntraceable, e.RepresentationID, model.NoOffset,
			model.StageVerify, false, "entry has neither a source offset nor a corruption marker")
	}
	return nil
}
