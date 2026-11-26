// Package pipeline orchestrates the five resumable stages: parse, align,
// verify, write, publish. Every two segments a journal checkpoint is taken,
// artifacts are content-addressed and committed after their bytes hit disk,
// and the final manifest is swapped in atomically.
package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"continuity/internal/clock"
	"continuity/internal/index"
	"continuity/internal/manifest"
	"continuity/internal/timeline"
)

// maxWorkers bounds concurrent segment inspection (project constraint).
const maxWorkers = 4

// Config carries the run-time parameters from the CLI.
type Config struct {
	InputDir       string
	IndexDir       string
	OutputDir      string
	Chunk          int // checkpoint cadence in segments
	CrashAfterWrite int
	Resume         bool
}

// inputIndex mirrors fixture/index.json.
type inputIndex struct {
	DASH struct {
		Primary string `json:"primary"`
		Backup  string `json:"backup"`
	} `json:"dash"`
	HLS struct {
		Primary string `json:"primary"`
		Backup  string `json:"backup"`
	} `json:"hls"`
	SwitchRecords string `json:"switch_records"`
	Frames        struct {
		Reference string `json:"reference"`
		Meta      string `json:"meta"`
	} `json:"frames"`
}

// Runner holds the assembled subsystems for one execution.
type Runner struct {
	cfg       Config
	journal   *index.Journal
	input     inputIndex
	hlsMode   bool // true when HLS, not DASH, is the primary manifest
	prim      *manifest.Manifest
	backup    *manifest.Manifest
	hlsPrim   *manifest.Manifest
	hlsBack   *manifest.Manifest
	timelines map[string]*timeline.Timeline
	verified  []inspectedSegment
	frameW    int
	frameH    int
}

// NewRunner validates paths and opens the journal.
func NewRunner(cfg Config) (*Runner, error) {
	if cfg.Chunk <= 0 {
		cfg.Chunk = 2
	}
	inAbs, err := filepath.Abs(cfg.InputDir)
	if err != nil {
		return nil, clock.New(clock.KindIO, "pipeline", "abs input: %v", err)
	}
	cfg.InputDir = inAbs
	// Reject URL-style inputs outright.
	for _, p := range []string{cfg.InputDir, cfg.OutputDir, cfg.IndexDir} {
		if looksURL(p) {
			return nil, clock.New(clock.KindInvalidInput, "pipeline",
				"URL inputs are not permitted: %q", p)
		}
	}
	j, err := index.NewJournal(cfg.IndexDir, inAbs)
	if err != nil {
		return nil, err
	}
	j.SetCrashConfig(index.CrashConfig{CrashAfterWrite: cfg.CrashAfterWrite})
	return &Runner{cfg: cfg, journal: j, timelines: map[string]*timeline.Timeline{}}, nil
}

func looksURL(p string) bool {
	return strings.Contains(p, "://") || strings.HasPrefix(p, "//")
}

// loadIndex reads and validates the fixture descriptor inside InputDir.
func (r *Runner) loadIndex() error {
	raw, err := os.ReadFile(filepath.Join(r.cfg.InputDir, "index.json"))
	if err != nil {
		return clock.New(clock.KindInvalidInput, "pipeline",
			"missing index.json in input dir: %v", err)
	}
	if err := json.Unmarshal(raw, &r.input); err != nil {
		return clock.New(clock.KindInvalidInput, "pipeline",
			"bad index.json: %v", err)
	}
	return nil
}

// Stage 1: parse manifests and switch records.
func (r *Runner) stageParse() error {
	if r.journal.IsStageDone(index.StageParse) && r.cfg.Resume {
		return r.reloadParsed()
	}
	if err := r.loadIndex(); err != nil {
		return err
	}
	if err := r.parseManifests(); err != nil {
		return err
	}
	// Switch records exercise RTP unwrapping; failure here is invalid input.
	if r.input.SwitchRecords != "" {
		sp, serr := manifest.SafePath(r.cfg.InputDir, r.input.SwitchRecords)
		if serr != nil {
			return serr
		}
		recs, serr := timeline.LoadSwitchRecords(sp)
		if serr != nil {
			return serr
		}
		if err := r.unwrapSwitchStream(recs); err != nil {
			return err
		}
	}
	if err := r.loadFrameMeta(); err != nil {
		return err
	}
	return r.journal.AdvanceStage(index.StageParse)
}

// parseManifests selects DASH when present, otherwise HLS. The chosen
// family populates r.prim/r.backup uniformly for downstream stages.
func (r *Runner) parseManifests() error {
	if r.input.DASH.Primary != "" {
		r.hlsMode = false
		pp, err := manifest.SafePath(r.cfg.InputDir, r.input.DASH.Primary)
		if err != nil {
			return err
		}
		r.prim, err = manifest.ParseDASH(pp)
		if err != nil {
			return err
		}
		if r.input.DASH.Backup != "" {
			bp, berr := manifest.SafePath(r.cfg.InputDir, r.input.DASH.Backup)
			if berr != nil {
				return berr
			}
			r.backup, err = manifest.ParseDASH(bp)
			if err != nil {
				return err
			}
		}
		return nil
	}
	if r.input.HLS.Primary != "" {
		r.hlsMode = true
		hp, err := manifest.SafePath(r.cfg.InputDir, r.input.HLS.Primary)
		if err != nil {
			return err
		}
		r.hlsPrim, err = manifest.ParseHLS(hp)
		if err != nil {
			return err
		}
		r.prim = r.hlsPrim
		if r.input.HLS.Backup != "" {
			hb, berr := manifest.SafePath(r.cfg.InputDir, r.input.HLS.Backup)
			if berr != nil {
				return berr
			}
			r.hlsBack, err = manifest.ParseHLS(hb)
			if err != nil {
				return err
			}
			r.backup = r.hlsBack
		}
		return nil
	}
	return clock.New(clock.KindInvalidInput, "parse",
		"index.json declares neither DASH nor HLS primary manifest")
}

// reloadParsed re-runs parsing after a resume (idempotent, no re-publish).
func (r *Runner) reloadParsed() error {
	if err := r.loadIndex(); err != nil {
		return err
	}
	if err := r.parseManifests(); err != nil {
		return err
	}
	return r.loadFrameMeta()
}

func (r *Runner) loadFrameMeta() error {
	if r.input.Frames.Meta == "" {
		r.frameW, r.frameH = FrameDefaults()
		return nil
	}
	mp, err := manifest.SafePath(r.cfg.InputDir, r.input.Frames.Meta)
	if err != nil {
		return err
	}
	w, h, err := parseFrameMeta(mp)
	if err != nil {
		return err
	}
	r.frameW, r.frameH = w, h
	return nil
}

// worker semaphore for bounded concurrency.
type semaphore struct{ ch chan struct{} }

func newSemaphore(n int) *semaphore {
	return &semaphore{ch: make(chan struct{}, n)}
}

func (s *semaphore) acquire() { s.ch <- struct{}{} }
func (s *semaphore) release() { <-s.ch }
