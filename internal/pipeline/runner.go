// Package pipeline schedules the ingest stages RECEIVED, SCANNED,
// FINGERPRINTED, NORMALIZED and ARCHIVED. Every block is committed to the
// journal as soon as it is durable, so a crash resumes from the first
// uncommitted block instead of restarting the asset.
package pipeline

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"example.com/mediavault/internal/fault"
	"example.com/mediavault/internal/fingerprint"
	"example.com/mediavault/internal/manifest"
	"example.com/mediavault/internal/model"
	"example.com/mediavault/internal/store"
)

// ErrInterrupted is returned by a deliberately stopped run used to exercise
// recovery. It is a control signal, not a rejection.
var ErrInterrupted = errors.New("pipeline interrupted")

// Runner owns the store and journal for one working directory.
type Runner struct {
	store           *store.Store
	journal         *store.Journal
	policy          fingerprint.Policy
	out             io.Writer
	stopAfterBlocks int

	reports []Report
}

// Options configure a Runner.
type Options struct {
	Store  *store.Store
	Policy fingerprint.Policy
	Out    io.Writer
	// StopAfterBlocks simulates a crash once this many new blocks are
	// committed in this run. Zero disables the simulation.
	StopAfterBlocks int
}

// New creates a Runner and opens its journal.
func New(o Options) (*Runner, error) {
	if o.Store == nil {
		return nil, fault.New(fault.CodeArgs, fault.StageReceived, -1, "runner needs a store")
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	j, err := store.OpenJournal(o.Store.Root())
	if err != nil {
		return nil, err
	}
	if o.Policy.Version == "" || o.Policy.ChunkFrames <= 0 {
		o.Policy = fingerprint.DefaultPolicy(1, 16)
	}
	return &Runner{store: o.Store, journal: j, policy: o.Policy, out: o.Out, stopAfterBlocks: o.StopAfterBlocks}, nil
}

// Policy returns the block policy in force.
func (r *Runner) Policy() fingerprint.Policy { return r.policy }

// Store exposes the content-addressed store.
func (r *Runner) Store() *store.Store { return r.store }

// Reports returns every ingest result in submission order.
func (r *Runner) Reports() []Report { return r.reports }

// Close flushes the journal. It is safe to call more than once.
func (r *Runner) Close() error {
	return r.journal.Close()
}

// Report is the outcome of one ingest.
type Report struct {
	Name          string      `json:"name"`
	Asset         model.Asset `json:"asset"`
	Rejected      bool        `json:"rejected"`
	Err           error       `json:"-"`
	ErrorCode     string      `json:"error_code,omitempty"`
	Interrupted   bool        `json:"interrupted,omitempty"`
	Transitions   []string    `json:"transitions,omitempty"`
	ResumeFrom    int         `json:"resume_from_block"`
	Committed     int         `json:"committed_blocks"`
	Recomputed    int         `json:"recomputed_blocks"`
	Reused        int         `json:"reused_blocks"`
	Wraps         int         `json:"rtp_wraps,omitempty"`
	RTPPackets    int         `json:"rtp_packets,omitempty"`
	DriftPPM      int64       `json:"drift_ppm,omitempty"`
	ArchiveDigest string      `json:"archive_digest,omitempty"`
	Deduplicated  bool        `json:"deduplicated,omitempty"`
}

// Reason renders a rejection for terminal output.
func (rep Report) Reason() string {
	if rep.Err == nil {
		return ""
	}
	return rep.Err.Error()
}

func (r *Runner) replay() (*store.Recorder, error) {
	recs, err := r.journal.Replay()
	if err != nil {
		return nil, err
	}
	return store.NewRecorder(recs), nil
}

func (r *Runner) transition(rep *Report, key string, from, to model.Stage) {
	line := fmt.Sprintf("stage %s %s -> %s", rep.Name, from, to)
	rep.Transitions = append(rep.Transitions, line)
	fmt.Fprintln(r.out, line)
	if err := r.journal.Append(store.Record{Kind: "stage", AssetKey: key, Stage: string(to)}); err != nil {
		fmt.Fprintf(r.out, "stage-journal %s: %v\n", rep.Name, err)
	}
}

// reject records a failed ingest. The fault's own stage wins when it carries
// one, so the terminal, the journal and tests always agree on where a failure
// was observed.
func (r *Runner) reject(rep Report, at model.Stage, err error) Report {
	rep.Rejected = true
	rep.Err = err
	rep.ErrorCode = fault.CodeOf(err)
	stage := fault.StageOf(err)
	if stage == "" {
		stage = fault.Stage(at)
	}
	if stage == "" {
		stage = fault.StageReceived
	}
	rep.Asset.Stage = model.Stage(stage)
	fmt.Fprintf(r.out, "rejected %s stage=%s code=%s: %s\n", rep.Name, stage, rep.ErrorCode, err)
	r.reports = append(r.reports, rep)
	return rep
}

// Finalize builds and seals the manifest from the journal. A repeated run with
// an identical entry set leaves the existing file untouched and reports the
// same digest, so the checklist can never be appended twice.
func (r *Runner) Finalize() (*manifest.File, bool, error) {
	rec, err := r.replay()
	if err != nil {
		return nil, false, err
	}
	entries, err := collectEntries(rec)
	if err != nil {
		return nil, false, err
	}
	f, err := manifest.Build(r.policy.Version, entries)
	if err != nil {
		return nil, false, err
	}
	if err := manifest.VerifySources(f.Entries); err != nil {
		return nil, false, err
	}
	raw, err := f.Marshal()
	if err != nil {
		return nil, false, err
	}
	existing, readErr := r.store.ReadFile(manifest.FileName)
	if readErr == nil {
		if prev, perr := manifest.Unmarshal(existing); perr == nil && prev.Digest == f.Digest {
			return f, false, nil
		}
	}
	if err := r.store.WriteFileAtomic(manifest.FileName, raw); err != nil {
		return nil, false, err
	}
	if err := r.journal.Append(store.Record{
		Kind: "manifest", ManifestDigest: f.Digest, Entries: len(f.Entries),
	}); err != nil {
		return nil, false, err
	}
	if err := r.journal.Sync(); err != nil {
		return nil, false, err
	}
	return f, true, nil
}

// LoadManifest reads the sealed manifest from disk.
func (r *Runner) LoadManifest() (*manifest.File, error) {
	raw, err := r.store.ReadFile(manifest.FileName)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fault.New(fault.CodeManifest, fault.StageArchived, -1, "manifest %s is missing", manifest.FileName)
		}
		return nil, err
	}
	return manifest.Unmarshal(raw)
}

func (r *Runner) appendReport(rep Report) Report {
	r.reports = append(r.reports, rep)
	return rep
}

func (r *Runner) recordArchive(e manifest.Entry) error {
	raw, err := json.Marshal(e)
	if err != nil {
		return fault.Wrap(fault.CodeManifest, fault.StageArchived, -1, err, "encode archive record")
	}
	return r.journal.Append(store.Record{Kind: "archive", AssetKey: e.Key, Digest: e.ArchiveDigest, Blob: raw})
}

// recordCheckpoint appends a block checkpoint and flushes it to stable
// storage before the next block is attempted. The bound is one block's work,
// not the whole asset: a crash can lose at most the block in flight, never the
// frontier the next run resumes from.
func (r *Runner) recordCheckpoint(key string, c model.ChunkRef) error {
	if err := r.journal.Append(store.Record{
		Kind: "checkpoint", AssetKey: key, Block: c.Index, Frames: c.Frames,
		Length: c.Length, ContentHash: c.Digest,
	}); err != nil {
		return err
	}
	return r.journal.Sync()
}

// closeJournal flushes the log at the end of a run.
func (r *Runner) closeJournal() {
	if err := r.journal.Close(); err != nil {
		fmt.Fprintf(r.out, "journal-close: %v\n", err)
	}
}

func collectEntries(rec *store.Recorder) ([]manifest.Entry, error) {
	byKey := make(map[string]manifest.Entry)
	var order []string
	for _, r := range rec.Records() {
		if r.Kind != "archive" || len(r.Blob) == 0 {
			continue
		}
		var e manifest.Entry
		if err := json.Unmarshal(r.Blob, &e); err != nil {
			return nil, fault.Wrap(fault.CodeManifest, fault.StageArchived, -1, err, "decode archive record for %s", r.AssetKey)
		}
		if _, ok := byKey[e.Key]; !ok {
			order = append(order, e.Key)
		}
		byKey[e.Key] = e
	}
	out := make([]manifest.Entry, 0, len(order))
	for _, k := range order {
		out = append(out, byKey[k])
	}
	return out, nil
}
