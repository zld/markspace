// Package pipeline wires the three subsystems together: parse+timeline,
// media analysis (split/silence/fingerprint), and transactional recovery.
// Every stage runs behind the WAL so an interrupted run resumes only when the
// input digest and all committed outputs still match.
package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"

	"mediad/internal/fingerprint"
	"mediad/internal/ingest"
	"mediad/internal/mp4"
	"mediad/internal/silence"
	"mediad/internal/splitter"
	"mediad/internal/timeline"
	"mediad/internal/txn"
)

// Config controls a pipeline run.
type Config struct {
	WorkDir          string
	Timescales       map[uint32]uint32
	TargetSegmentUS  int64
	DriftPPMMax      int64
	SilenceThreshold float64
	SilenceMinUS     int64
	Limits           mp4.Limits
}

// InputFile is one fMP4 fragment-group file with its raw bytes already read.
type InputFile struct {
	Name string
	Data []byte
}

// Result is the serialisable acceptance summary.
type Result struct {
	Segments         []splitter.Segment    `json:"segments"`
	SilenceIntervals []silence.Interval    `json:"silence_intervals"`
	Duplicates       []fingerprint.Fingerprint `json:"duplicates"`
	ManifestSHA256   string                `json:"manifest_sha256"`
	OutputSHA256     string                `json:"output_sha256"`
	Gaps             []ingest.Gap          `json:"sequence_gaps"`
	RecoveryMS       int64                 `json:"recovery_ms"`
}

// stageOutputs defines the committed files per stage.
func stageOutputs() []txn.StageOutput {
	return []txn.StageOutput{
		{Stage: txn.StageParse, Name: "parse.json"},
		{Stage: txn.StageTimeline, Name: "timeline.json"},
		{Stage: txn.StageAnalyze, Name: "analyze.json"},
		{Stage: txn.StageFingerprint, Name: "fingerprint.json"},
		{Stage: txn.StageCommit, Name: "manifest.json"},
	}
}

// Run executes the full pipeline for one group of input files.
func Run(inputs []InputFile, cfg Config) (*Result, error) {
	inputSHA, err := digestInputs(inputs)
	if err != nil {
		return nil, err
	}
	wal, err := txn.CreateWAL(cfg.WorkDir, inputSHA)
	if err != nil {
		return nil, err
	}
	defer wal.Close()
	mgr := txn.NewManager(wal, stageOutputs())

	resumedStage, err := mgr.Recover(inputSHA)
	if err != nil {
		return nil, err
	}
	recoveryStart := walMillis()

	// In-memory results are always recomputed deterministically so a resumed
	// process can rebuild its summary; the WAL gates the durable side effects
	// and validates that committed outputs still match.
	frags, groups, err := parseAll(inputs, cfg.Limits)
	if err != nil {
		return nil, err
	}
	if err := mgr.RunStage(txn.StageParse, func(temp string) error {
		return writeJSON(temp, struct {
			Groups []ingest.Report `json:"groups"`
		}{Reports(groups)})
	}); err != nil {
		return nil, err
	}

	mapped, segments, err := splitter.Run(flattenFrags(groups, frags), splitter.Config{
		TargetSegmentUS: cfg.TargetSegmentUS,
		Timescales:      cfg.Timescales,
		DriftPPMMax:     cfg.DriftPPMMax,
	})
	if err != nil {
		return nil, err
	}
	if err := mgr.RunStage(txn.StageTimeline, func(temp string) error {
		return writeJSON(temp, segments)
	}); err != nil {
		return nil, err
	}

	silences := runSilence(inputs, frags, mapped, cfg)
	if err := mgr.RunStage(txn.StageAnalyze, func(temp string) error {
		return writeJSON(temp, silences)
	}); err != nil {
		return nil, err
	}

	fingerprints := runFingerprint(inputs, frags)
	if err := mgr.RunStage(txn.StageFingerprint, func(temp string) error {
		return writeJSON(temp, fingerprints)
	}); err != nil {
		return nil, err
	}

	manifest := buildManifest(segments, silences, fingerprints, groups)
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	manifestSHA := txn.BytesSHA256(manifestBytes)
	if err := mgr.RunStage(txn.StageCommit, func(temp string) error {
		return os.WriteFile(temp, manifestBytes, 0o600)
	}); err != nil {
		return nil, err
	}
	// On resume the commit stage is skipped, so verify the on-disk manifest
	// still hashes to the deterministically recomputed value.
	onDiskSHA, err := txn.FileSHA256(filepath.Join(cfg.WorkDir, "manifest.json"))
	if err != nil {
		return nil, err
	}
	if onDiskSHA != manifestSHA {
		return nil, timeline.New(timeline.ErrRecovery,
			filepath.Join(cfg.WorkDir, "manifest.json"), -1,
			"recomputed manifest %s differs from committed %s", manifestSHA, onDiskSHA)
	}
	outputSHA := onDiskSHA

	_ = resumedStage
	res := &Result{
		Segments:         flattenSegments(segments),
		SilenceIntervals: silences,
		Duplicates:       fingerprints,
		ManifestSHA256:   manifestSHA,
		OutputSHA256:     outputSHA,
		Gaps:             flattenGaps(groups),
		RecoveryMS:       walMillis() - recoveryStart,
	}
	return res, nil
}

func parseAll(inputs []InputFile, lim mp4.Limits) ([]*mp4.Fragment, []*ingest.Group, error) {
	var frags []*mp4.Fragment
	var groups []*ingest.Group
	for _, in := range inputs {
		r := mp4.NewSeeker(in.Data)
		boxes, err := mp4.ScanTopLevel(r, in.Name, lim)
		if err != nil {
			return nil, nil, err
		}
		var fileFrags []*mp4.Fragment
		for i := 0; i < len(boxes); i++ {
			if boxes[i].Type != "moof" {
				continue
			}
			var mdat *mp4.Box
			if i+1 < len(boxes) && boxes[i+1].Type == "mdat" {
				mdat = boxes[i+1]
			}
			f, err := mp4.DecodeFragment(r, in.Name, boxes[i], mdat, lim)
			if err != nil {
				// Ensure the error carries this file name.
				return nil, nil, timeline.AsMediaError(err)
			}
			fileFrags = append(fileFrags, f)
			frags = append(frags, f)
		}
		g, err := ingest.BuildGroup(in.Name, fileFrags, in.Data)
		if err != nil {
			return nil, nil, err
		}
		groups = append(groups, g)
	}
	return frags, groups, nil
}

func runSilence(inputs []InputFile, frags []*mp4.Fragment,
	mapped map[uint32][]splitter.MappedSample, cfg Config) []silence.Interval {
	var out []silence.Interval
	dataByFile := map[string][]byte{}
	for _, in := range inputs {
		dataByFile[in.Name] = in.Data
	}
	// Use decoded amplitudes derived from sample payload bytes treated as
	// 16-bit PCM in the demo fixtures.
	for trackID, samples := range mapped {
		frames := buildPCMFrames(trackID, samples, frags, dataByFile)
		ivs := silence.Detect(frames, silence.Config{
			AmplitudeThreshold: cfg.SilenceThreshold,
			MinDurationUS:      cfg.SilenceMinUS,
		})
		out = append(out, ivs...)
	}
	return out
}

func runFingerprint(inputs []InputFile, frags []*mp4.Fragment) []fingerprint.Fingerprint {
	dedup := fingerprint.NewDedup()
	dataByFile := map[string][]byte{}
	for _, in := range inputs {
		dataByFile[in.Name] = in.Data
	}
	for _, f := range frags {
		data := dataByFile[f.File]
		for _, tf := range f.Tracks {
			if len(tf.Samples) == 0 {
				continue
			}
			// Add every candidate; Dedup collapses equal content to one
			// fingerprint regardless of its input offset.
			dedup.Add(fingerprint.Candidate{
				Key: fingerprint.HashFragment(data, tf),
				ContentOK: true,
				Offset:    tf.Samples[0].DataOffset,
			})
		}
	}
	return dedup.Fingerprints()
}
