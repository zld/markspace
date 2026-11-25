package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/pion/rtp"

	"mediad/internal/gen"
	"mediad/internal/pipeline"
	"mediad/internal/timeline"
	"mediad/internal/txn"
)

// demoReport is the exact acceptance JSON contract plus a few extra provenance
// fields. Required keys: segments, silence_intervals, duplicates, error_file,
// error_offset, error_code, recovery_ms, manifest_sha256, output_sha256.
type demoReport struct {
	Segments         any             `json:"segments"`
	SilenceIntervals any             `json:"silence_intervals"`
	Duplicates       any             `json:"duplicates"`
	ErrorFile        string          `json:"error_file"`
	ErrorOffset      int64           `json:"error_offset"`
	ErrorCode        string          `json:"error_code"`
	RecoveryMS       int64           `json:"recovery_ms"`
	ManifestSHA256   string          `json:"manifest_sha256"`
	OutputSHA256     string          `json:"output_sha256"`
	// Extra provenance (not part of the required contract).
	RTPWrapMonotonic  bool `json:"rtp_wrap_monotonic"`
	TimelineMonotonic bool `json:"timeline_monotonic"`
	CorruptExitCode   int  `json:"corrupt_exit_code"`
}

func baseConfig(work string) pipeline.Config {
	return pipeline.Config{
		WorkDir:          work,
		Timescales:       map[uint32]uint32{1: gen.AudioTimescale, 2: 90000},
		TargetSegmentUS:  20_000,
		DriftPPMMax:      2000,
		SilenceThreshold: 0.01,
		SilenceMinUS:     1,
	}
}

// runNormal processes the healthy group.
func runNormal(outDir string) (*pipeline.Result, error) {
	data := gen.NormalGroup()
	in := []pipeline.InputFile{{Name: "normal.m4s", Data: data}}
	return pipeline.Run(in, baseConfig(filepath.Join(outDir, "normal")))
}

// runCorrupt parses the truncated group and returns the located MediaError.
func runCorrupt(outDir string) *timeline.MediaError {
	data, _ := gen.TruncatedGroup()
	in := []pipeline.InputFile{{Name: "corrupt.m4s", Data: data}}
	_, err := pipeline.Run(in, baseConfig(filepath.Join(outDir, "corrupt")))
	return timeline.AsMediaError(err)
}

// rtpWrapCheck feeds 32-bit-wrapping RTP timestamps through the unwrapper and
// confirms the expanded series is strictly monotonic.
func rtpWrapCheck() bool {
	u := timeline.NewRTPUnwrapper(90000, 0)
	stamps := []uint32{0xFFFFFFF0, 0xFFFFFFF8, 0x00000004, 0x00000010}
	var prev uint64
	first := true
	for _, ts := range stamps {
		v, err := u.Expand(&rtp.Header{Timestamp: ts})
		if err != nil {
			return false
		}
		if !first && v <= prev {
			return false
		}
		prev, first = v, false
	}
	return true
}

// runBoundary exercises tfdt rollover, rollback and VFR, then injects a
// post-commit interruption and resumes. It returns the resumed result,
// recovery timing and whether the valid common axis stayed monotonic.
func runBoundary(outDir string) (resumed *pipeline.Result, monotonic bool, err error) {
	data := gen.BoundaryGroup()
	in := []pipeline.InputFile{{Name: "boundary.m4s", Data: data}}
	work := filepath.Join(outDir, "boundary")

	faultCfg := baseConfig(work)
	faultCfg.FaultAfterStage = txn.StageAnalyze
	if _, ferr := pipeline.Run(in, faultCfg); !errors.Is(ferr, pipeline.ErrFaultInjected) {
		return nil, false, ferr
	}

	// Reference uninterrupted run in an isolated directory.
	ref, rerr := pipeline.Run(in, baseConfig(filepath.Join(outDir, "boundary-ref")))
	if rerr != nil {
		return nil, false, rerr
	}

	res, aerr := pipeline.Run(in, baseConfig(work))
	if aerr != nil {
		return nil, false, aerr
	}
	if res.ManifestSHA256 != ref.ManifestSHA256 {
		return nil, false, errors.New("resumed checksum differs from uninterrupted run")
	}
	return res, mappedIsMonotonic(res), nil
}

// mappedIsMonotonic verifies segment time ordering as a proxy for the common
// axis staying monotonic across rollover and around the invalid rollback.
func mappedIsMonotonic(res *pipeline.Result) bool {
	var last int64 = -1
	for _, s := range res.Segments {
		if s.StartUS < last {
			return false
		}
		last = s.StartUS
	}
	return true
}

// runDemo executes every scenario and assembles the acceptance report. The
// returned int is the process exit code (0 for a successful demonstration).
func runDemo(outDir, scenario string) (demoReport, int) {
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return demoReport{ErrorCode: timeline.ErrInvalidInput}, 1
	}
	rep := demoReport{ErrorOffset: -1}

	if scenario == "all" || scenario == "normal" || scenario == "boundary" {
		normal, err := runNormal(outDir)
		if err != nil {
			me := timeline.AsMediaError(err)
			rep.ErrorFile, rep.ErrorOffset, rep.ErrorCode = me.File, me.Offset, me.Code
			return rep, timeline.ExitCode(err)
		}
		rep.Segments = normal.Segments
		rep.SilenceIntervals = normal.SilenceIntervals
		rep.Duplicates = normal.Duplicates
		rep.ManifestSHA256 = normal.ManifestSHA256
		rep.OutputSHA256 = normal.OutputSHA256
	}

	if scenario == "all" || scenario == "boundary" {
		resumed, mono, err := runBoundary(outDir)
		if err != nil {
			me := timeline.AsMediaError(err)
			rep.ErrorFile, rep.ErrorCode = "boundary.m4s", me.Code
			return rep, timeline.ExitCode(err)
		}
		rep.RecoveryMS = resumed.RecoveryMS
		rep.TimelineMonotonic = mono
		rep.RTPWrapMonotonic = rtpWrapCheck()
	}

	if scenario == "all" || scenario == "corrupt" {
		me := runCorrupt(outDir)
		if me != nil {
			rep.ErrorFile, rep.ErrorOffset, rep.ErrorCode = me.File, me.Offset, me.Code
			rep.CorruptExitCode = timeline.ExitCode(me)
		}
		// A single-scenario corrupt run exits 2; the combined demo keeps going.
		if scenario == "corrupt" {
			return rep, 2
		}
	}
	return rep, 0
}

func printJSON(rep demoReport) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(rep)
}
