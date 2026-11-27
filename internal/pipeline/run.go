// Package pipeline orchestrates one release migration.
//
// The run is a strict sequence of five stages, each recording a durable
// status plus a resume position:
//
//	parse → audio → manifest-stage → verify → commit
//
// Parsing and audio processing are pure functions of the input bytes, so a
// resumed run reproduces exactly the same containers.  The manifest stage
// consults whatever is already committed, so released artifacts are reused
// rather than rewritten and never appended twice.  A failure at any stage
// restores the last complete release; a re-run continues from the failed
// stage's recorded boundary.
package pipeline

import (
	"crypto/sha256"
	"encoding/hex"

	"example.com/assetpack/internal/audio"
	"example.com/assetpack/internal/fault"
	"example.com/assetpack/internal/manifest"
	"example.com/assetpack/internal/media"
	"example.com/assetpack/internal/migrate"
	"example.com/assetpack/internal/rollback"
)

// Options configures one migration run.
type Options struct {
	WorkDir    string
	InputDir   string
	ReleaseDir string
	StateDir   string
	Release    string
	Target     audio.Target
	// FailAfter names the stage whose completion is followed by an injected
	// failure.  Empty means a normal run.
	FailAfter    string
	Force        bool
	MaxGapSample int64
	// CreationSeconds is stamped into container headers.  Zero keeps output
	// byte-identical between runs.
	CreationSeconds uint32
}

// AssetResult is the per-asset account of a run.
type AssetResult struct {
	Name         string       `json:"name"`
	SourceRate   int          `json:"source_rate"`
	SourceChan   int          `json:"source_channels"`
	SourceFrames int64        `json:"source_frames"`
	ParseOffset  int64        `json:"parse_offset"`
	ParseSample  int64        `json:"parse_sample"`
	PMTVersion   uint8        `json:"pmt_version"`
	PCRWraps     uint64       `json:"pcr_wraps"`
	Gaps         []audio.Gap  `json:"gaps"`
	Report       audio.Report `json:"audio"`
	Index        media.Index  `json:"index"`
	// Artifact is the content-addressed file name in the release directory.
	Artifact string `json:"artifact"`
	// Display is the human-facing name reported in the terminal.
	Display string `json:"display"`
	// SourceDigest is the SHA-256 of the transport stream actually parsed.
	// The adapter compares it with the digest recorded by the legacy
	// manifest and refuses to commit when they differ.
	SourceDigest string `json:"source_digest"`
	// Container is filled during the parse/audio stage and handed straight
	// to the manifest stage, so verify hashes the exact staged bytes.
	Container []byte `json:"-"`
}

// Digest is the SHA-256 of the produced container.
func (a AssetResult) Digest() string {
	sum := sha256.Sum256(a.Container)
	return hex.EncodeToString(sum[:])
}

// Outcome is the complete, verifiable result of one run.
type Outcome struct {
	Journal *migrate.Journal
	Faults  *fault.List
	Before  *manifest.V2
	After   *manifest.V2
	Diff    manifest.DiffCounts
	Assets  []AssetResult
	// Committed reports that *this* run promoted a release.
	Committed bool
	// Valid reports that the manifest currently on disk passes verification,
	// including the previous release left in place by a rollback.
	Valid bool
	// RunOK reports that this run finished all five stages successfully.
	RunOK       bool
	ManifestSHA string
	Resume      migrate.Position
	Failure     *fault.Fault
	Recovered   *rollback.RecoveryState
	// Previous is the journal from the prior attempt, kept for reporting.
	Previous *migrate.Journal
}

// ExitCode maps an outcome to the process exit status: 0 only for a valid,
// committed release.
func (o *Outcome) ExitCode() int {
	if o != nil && o.RunOK && o.Committed && o.Valid {
		return 0
	}
	return 1
}

// failOutcome records the failure, restores the previous release, and
// returns an outcome describing exactly what was and was not committed.
func failOutcome(opts Options, cp *rollback.Checkpoint, journal *migrate.Journal,
	out *Outcome, cause error) (*Outcome, error) {
	f := asFault(cause)
	if f == nil {
		f = fault.New(fault.StageState, fault.KindInjected, "%v", cause)
	}
	stage := stageForFault(f)
	// The resume point is the last complete boundary.  Only a fault raised
	// inside parsing or audio processing moves it, because only those stages
	// stop mid-input; later stages operate on already-complete material.
	if f.Stage == fault.StageParse || f.Stage == fault.StageAudio {
		if f.Offset > 0 {
			out.Resume.Offset = f.Offset
		}
		if f.Sample > 0 {
			out.Resume.Sample = f.Sample
		}
	}
	journal.Set(stage, migrate.StatusFailed, out.Resume.Offset, out.Resume.Sample, f.Message, f)
	out.Failure = f
	out.Faults.Add(f)
	if j, _ := cp.ReadJournal(); j != nil && j.Phase == rollback.PhaseStaged {
		if rbErr := cp.Rollback(); rbErr != nil {
			out.Faults.Add(fault.New(fault.StageRollback, fault.KindIO, "rollback: %v", rbErr))
		}
	}
	after, err := cp.ReadManifest()
	if err == nil {
		out.After = after
	}
	// This run did not commit.  The release directory, however, still holds
	// the previous release after a successful rollback, and that release
	// remains verifiable.
	out.Committed = false
	out.RunOK = false
	if after != nil {
		out.ManifestSHA = after.Digest
		out.Valid = cp.VerifyDir() == nil
	} else {
		out.Valid = false
	}
	_ = journal.Persist(opts.StateDir)
	return out, cause
}

func stageForFault(f *fault.Fault) migrate.Stage {
	switch f.Stage {
	case fault.StageParse:
		return migrate.StageParse
	case fault.StageAudio:
		return migrate.StageAudio
	case fault.StageManifest:
		return migrate.StageStageManifest
	case fault.StageVerify:
		return migrate.StageVerify
	case fault.StageCommit:
		return migrate.StageCommit
	}
	return migrate.StageStageManifest
}

func maybeInject(opts Options, justFinished migrate.Stage) error {
	if opts.FailAfter == "" {
		return nil
	}
	want, err := migrate.FailAfter(opts.FailAfter)
	if err != nil {
		return err
	}
	if want != justFinished {
		return nil
	}
	return fault.New(fault.StageState, fault.KindInjected,
		"injected failure after stage %s", justFinished).Recover(false)
}

func asFault(err error) *fault.Fault {
	if err == nil {
		return nil
	}
	if f, ok := err.(*fault.Fault); ok {
		return f
	}
	if l, ok := fault.AsList(err); ok {
		return l.FirstNonRecoverable()
	}
	return nil
}

func maxI64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
