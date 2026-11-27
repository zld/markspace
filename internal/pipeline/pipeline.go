// Package pipeline wires fixture generation, ingest, timeline alignment,
// measurement, durable job state, the deterministic manifest and offline
// evidence into one resumable run. It is the single place that knows the
// ordering of the four processing stages and the recovery contract.
package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"regress/internal/evidence"
	"regress/internal/fixture"
	"regress/internal/ingest"
	"regress/internal/job"
	"regress/internal/manifest"
	"regress/internal/measure"
	"regress/internal/model"
	"regress/internal/timeline"
)

// TaskKey is the stable identifier of the acceptance scenario. It never
// changes across reruns, which is what makes recovery and dedup possible.
const TaskKey = "regress-demo"

// Result is what a completed run reports to the CLI.
type Result struct {
	Measure      *measure.Report
	Evidence     *evidence.Report
	ManifestSHA  string
	ManifestPath string
	Recovered    bool
	Republished  int
	Recovery     job.Recovery
	// ArtifactCount is the number of distinct artifact files on disk.
	ArtifactCount int
}

// Run executes (or resumes) one scenario under workDir. fault controls the
// generated anomaly. For FaultAfterArtifact the run exits at the configured
// point on the first attempt; callers re-invoke Run with the same arguments
// to exercise recovery.
func Run(workDir string, fault fixture.Fault) (*Result, error) {
	if err := validateFault(fault); err != nil {
		return nil, err
	}
	// Each fault scenario owns a distinct subtree so the acceptance commands
	// can run against one --work directory in any order without one
	// scenario's persisted state or fixture bytes colliding with another's.
	// Recovery still uses a single fixed path, so rerunning the same fault
	// resumes the same job.
	scenarioDir := filepath.Join(workDir, string(fault))
	spec := fixture.DefaultSpec(fault)
	plan, err := fixture.Plan(spec)
	if err != nil {
		return nil, err
	}

	// 1. Receive: generate the fixtures into the confined work directory.
	inputRoot := filepath.Join(scenarioDir, "input")
	if err := os.MkdirAll(inputRoot, 0o755); err != nil {
		return nil, model.NewError(model.StageReceive, "input-mkdir", inputRoot,
			"", 0, err)
	}
	// The fault choice is part of the scenario identity and is persisted so a
	// resumed run regenerates byte-identical inputs; changing it within one
	// scenario is rejected by the input-hash check inside the job store.
	faultPath := filepath.Join(inputRoot, "fault.txt")
	if raw, err := os.ReadFile(faultPath); err == nil {
		persisted := fixture.Fault(string(raw))
		if persisted != fault {
			return nil, model.NewError(model.StageReceive, "fault-mismatch",
				faultPath, "", 0,
				fmt.Errorf("work dir already runs fault %q, got %q",
					persisted, fault))
		}
	} else if os.IsNotExist(err) {
		if err := writeInput(faultPath, []byte(fault)); err != nil {
			return nil, err
		}
	} else {
		return nil, model.NewError(model.StageReceive, "fault-read", faultPath,
			"", 0, err)
	}
	// Write the generated fixtures only when their bytes differ, so a rerun
	// neither rewrites an untouched input nor invalidates a concurrent read.
	mp4Path := filepath.Join(inputRoot, "capture.mp4")
	rtpPath := filepath.Join(inputRoot, "capture.rtp")
	mp4Bytes, cut, err := fixture.BuildMP4(spec, plan)
	if err != nil {
		return nil, err
	}
	capture, err := fixture.BuildRTP(spec, plan)
	if err != nil {
		return nil, err
	}
	rtpBytes, err := capture.Marshal()
	if err != nil {
		return nil, err
	}
	if fault == fixture.FaultTruncate {
		mp4Bytes = mp4Bytes[:cut]
		rtpBytes = rtpBytes[:capture.TruncatedByte]
	}
	if err := writeInputIfChanged(mp4Path, mp4Bytes); err != nil {
		return nil, err
	}
	if err := writeInputIfChanged(rtpPath, rtpBytes); err != nil {
		return nil, err
	}
	inputHash := hashAll(mp4Bytes, rtpBytes, []byte(fault))

	store, err := job.OpenStore(filepath.Join(scenarioDir, "state"))
	if err != nil {
		return nil, err
	}
	j, rec, err := store.Start(TaskKey, inputHash)
	if err != nil {
		return nil, err
	}
	if rec.Recovered && j.State.LastError != nil {
		// A prior hard failure (e.g. truncation) is not auto-resumable: the
		// recorded locatable error is surfaced rather than hidden. A job that
		// reached commit carries no LastError and resumes normally.
		return nil, j.State.LastError
	}

	// 2. Parse: ingest both containers. Truncation fails here with a
	// locatable, stage-tagged error and a non-zero exit at the CLI.
	mp4Res, err := ingest.ReadMP4(mp4Path)
	if err != nil {
		_ = j.Fail(err)
		return nil, err
	}
	rtpRes, err := ingest.ReadRTP(rtpPath)
	if err != nil {
		_ = j.Fail(err)
		return nil, err
	}
	// Reach (not Advance) so a run resumed from a later stage keeps its
	// checkpoint instead of being rejected as a backwards transition.
	if err := j.Reach(model.StageParse); err != nil {
		return nil, err
	}

	// 3. Align and measure.
	sourceStreams := make([]timeline.SourceStream, 0, len(rtpRes.Streams))
	for _, st := range rtpRes.Streams {
		sourceStreams = append(sourceStreams, timeline.SourceStream{
			TrackID: st.TrackID, Kind: st.Kind, Events: st.Events,
		})
	}
	tl := timeline.Build(mp4Res.Tracks, sourceStreams, plan)
	rep := measure.Compute(tl, mp4Res.Tracks)
	ev, err := evidence.Build(mp4Res.Tracks, tl)
	if err != nil {
		return nil, err
	}

	// 4. Artifacts: write the evidence artifact idempotently, then crash on
	// demand before any commit, leaving an uncommitted artifact on disk.
	evBytes, err := ev.Marshal()
	if err != nil {
		return nil, err
	}
	evRec, err := j.WriteArtifact(TaskKey+":evidence", "evidence.json", evBytes)
	if err != nil {
		return nil, err
	}
	if fault == fixture.FaultAfterArtifact && !rec.Recovered {
		// First attempt deliberately stops between artifact and commit.
		return nil, model.NewError(model.StageArtifact, "simulated-exit",
			j.Dir(), TaskKey, 0, fmt.Errorf("injected fault: exit after artifact"))
	}

	// 5. Manifest: merge entries deterministically and commit each key once.
	manifestPath := filepath.Join(scenarioDir, "manifest.txt")
	existing, err := manifest.Parse(manifestPath)
	if err != nil {
		return nil, err
	}
	entries := buildEntries(tl, evRec.Sha256, evRec.Size)

	// republished counts keys that were already committed before this run
	// and whose published content would have to change. Committing such a
	// key again is exactly the duplicate publish the harness must prevent,
	// so it is reported rather than silently applied.
	prior := map[string]manifest.Entry{}
	for _, e := range existing {
		prior[e.Key] = e
	}
	republished := 0
	for _, e := range entries {
		if !j.IsCommitted(e.Key) {
			continue
		}
		if p, ok := prior[e.Key]; !ok || p.ContentSha != e.ContentSha ||
			p.Size != e.Size || p.Tick != e.Tick {
			republished++
		}
	}

	merged := manifest.Merge(existing, entries)
	_, manifestSHA, err := manifest.WriteAtomic(manifestPath, merged)
	if err != nil {
		return nil, err
	}
	// Commit only keys not already published, preserving the no-republish
	// guarantee across restarts, then confirm nothing was left unrecorded.
	for _, e := range entries {
		if err := j.CommitKey(e.Key); err != nil {
			return nil, err
		}
	}
	for _, e := range entries {
		if !j.IsCommitted(e.Key) {
			return nil, model.NewError(model.StageCommit, "commit-incomplete",
				manifestPath, e.Key, 0,
				fmt.Errorf("%w: key not recorded after commit", model.ErrCheckpoint))
		}
	}

	return &Result{
		Measure: rep, Evidence: ev, ManifestSHA: manifestSHA,
		ManifestPath: manifestPath, Recovered: rec.Recovered,
		Republished: republished, Recovery: *rec,
		ArtifactCount: len(j.State.Artifacts),
	}, nil
}

// buildEntries creates one manifest entry per unique aligned event key, in
// the timeline's deterministic order; a summary entry anchors the evidence
// artifact hash so the manifest changes if the evidence bytes change.
func buildEntries(tl *timeline.Timeline, evidenceHash string, evidenceSize int64) []manifest.Entry {
	entries := []manifest.Entry{{
		Key: TaskKey + ":evidence", Track: TaskKey, Kind: model.KindSEI,
		Tick: 0, Size: evidenceSize, ContentSha: evidenceHash,
	}}
	for _, ev := range tl.Unique {
		entries = append(entries, manifest.Entry{
			Key: ev.Key, Track: ev.Track, Kind: ev.Kind,
			Tick: int64(ev.ObservedTick), Size: int64(len(ev.PayloadHash)),
			ContentSha: ev.PayloadHash,
		})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Key < entries[j].Key
	})
	return entries
}

func writeInput(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return model.NewError(model.StageReceive, "write-input", path, "", 0, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return model.NewError(model.StageReceive, "rename-input", path, "", 0, err)
	}
	return nil
}

// writeInputIfChanged writes data only when the file at path is absent or
// differs, so reruns keep an identical fixture on disk untouched.
func writeInputIfChanged(path string, data []byte) error {
	if cur, err := os.ReadFile(path); err == nil && bytesEqual(cur, data) {
		return nil
	} else if err != nil && !os.IsNotExist(err) {
		return model.NewError(model.StageReceive, "read-input", path, "", 0, err)
	}
	return writeInput(path, data)
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func hashAll(parts ...[]byte) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write(p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func validateFault(fault fixture.Fault) error {
	switch fault {
	case fixture.FaultNone, fixture.FaultWrap, fixture.FaultTruncate,
		fixture.FaultAfterArtifact, fixture.FaultEvents:
		return nil
	default:
		return fmt.Errorf("unknown fault %q", fault)
	}
}
