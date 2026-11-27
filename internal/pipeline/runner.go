// Package pipeline orchestrates the scan, source normalization, ladder
// alignment, MP4 muxing, validation and atomic commit stages into one
// recoverable job. It wires the integer clock, error model, WAL and manifest
// packages together and exposes a library entry point reusable by other Go
// programs, with the CLI being a thin wrapper.
package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gamepack/internal/fixture"
	perr "gamepack/internal/errors"
	"gamepack/internal/manifest"
	"gamepack/internal/recovery"
	"gamepack/internal/stream"
	"gamepack/internal/txn"
)

// Config parameterizes one packing job.
type Config struct {
	Root      string
	Codecs    []stream.Codec
	Bitrates  []int
	Units     int
	KeyEvery  int
	WrapBits  uint
	Inject    map[fixture.Injection]bool
	// CrashAfter, when set, simulates a crash by returning immediately after
	// the given stage records a started/done record, before it is verified.
	CrashAfter txn.Stage
}

// Report is the terminal summary printed by the CLI.
type Report struct {
	ResumeStage   txn.Stage
	ManifestPath  string
	ManifestSHA   string
	EntryCount    int
	Diff          manifest.Diff
	Rollbacks     int
	StageLog      []txn.Record
	ZeroTickDiffs int
}

// Runner holds the working directories and WAL for a job.
type Runner struct {
	cfg  Config
	dirs dirs
	wal  *txn.WAL
	plan *recovery.Plan
}

type dirs struct {
	root   string
	inputs string
	segs   string
	txnDir string
}

// New prepares directory layout and opens the WAL.
func New(cfg Config) (*Runner, error) {
	if cfg.Units <= 0 {
		cfg.Units = 12
	}
	if cfg.KeyEvery <= 0 {
		cfg.KeyEvery = 4
	}
	if cfg.WrapBits == 0 {
		cfg.WrapBits = 16
	}
	d := dirs{
		root:   cfg.Root,
		inputs: filepath.Join(cfg.Root, "inputs"),
		segs:   filepath.Join(cfg.Root, "segments"),
		txnDir: filepath.Join(cfg.Root, "txn"),
	}
	for _, p := range []string{d.root, d.inputs, d.segs, d.txnDir} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			return nil, perr.New("pipeline", perr.CodeStage, "mkdir %s: %v", p, err)
		}
	}
	w, err := txn.Open(d.txnDir)
	if err != nil {
		return nil, err
	}
	return &Runner{cfg: cfg, dirs: d, wal: w}, nil
}

// inputDigest hashes every input that can affect the output: the bitstream,
// its keyframe index, the RTP source (so wrap/switch changes count), and a
// small configuration fingerprint. Any change invalidates entry reuse.
func (r *Runner) inputDigest(arts []*fixture.Artifact) (string, error) {
	h := sha256.New()
	// Configuration fingerprint first.
	fmt.Fprintf(h, "units=%d key=%d wrap=%d codecs=%v bitrates=%v inject=%v\n",
		r.cfg.Units, r.cfg.KeyEvery, r.cfg.WrapBits,
		r.cfg.Codecs, r.cfg.Bitrates, sortedInjections(r.cfg.Inject))
	paths := make([]string, 0, len(arts)*3)
	for _, a := range arts {
		paths = append(paths, a.StreamPath, a.IndexPath, a.RTPPath)
	}
	sort.Strings(paths)
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return "", err
		}
		h.Write([]byte(filepath.Base(p)))
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func sortedInjections(in map[fixture.Injection]bool) []string {
	out := make([]string, 0, len(in))
	for k, v := range in {
		if v {
			out = append(out, string(k))
		}
	}
	sort.Strings(out)
	return out
}

// record is a small helper for WAL transitions.
func (r *Runner) record(stage txn.Stage, state txn.State, resumeAt, inputHash string,
	detail map[string]any) error {
	_, err := r.wal.Append(txn.Record{
		Stage: stage, State: state, ResumeAt: resumeAt,
		InputHash: inputHash, Detail: detail,
	})
	return err
}

// maybeCrash simulates an injected crash after a named stage.
func (r *Runner) maybeCrash(stage txn.Stage) error {
	if r.cfg.CrashAfter != "" && r.cfg.CrashAfter == stage {
		return &SimulatedCrash{Stage: stage}
	}
	return nil
}

// SimulatedCrash signals an injected, recoverable interruption.
type SimulatedCrash struct{ Stage txn.Stage }

func (e *SimulatedCrash) Error() string {
	return fmt.Sprintf("simulated crash after stage %s", e.Stage)
}

// IsSimulatedCrash reports whether err is an injected crash.
func IsSimulatedCrash(err error) bool {
	_, ok := err.(*SimulatedCrash)
	return ok
}

// hashFile returns the SHA-256 of a file's bytes.
func hashFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
