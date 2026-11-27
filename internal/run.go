package rtpreplay

import (
	"fmt"
	"os"
	"strings"
)

// RunOptions is the CLI-facing request.
type RunOptions struct {
	Case  string
	Seed  int64
	Depth int
}

// RunOutcome is what the adapter prints and returns.
type RunOutcome struct {
	Summary  string
	ExitCode int
	Report   *Report
	Err      error
}

// DefaultConfigDigest fixes the configuration half of the idempotency key for
// the demo pipeline. The reorder depth is the only per-run knob that changes
// results, so it participates in the digest.
func demoConfig(caseName string, seed int64, depth int) PipelineConfig {
	return PipelineConfig{
		ReorderDepth: depth,
		Case:         caseName,
		Seed:         seed,
		Version:      "1",
		FailNode:     blockedNodeFor(caseName),
		FaultMode:    "permanent",
		FaultTimes:   1,
	}
}

// blockedNodeFor selects the injected failure node for the blocked-dag case.
func blockedNodeFor(caseName string) string {
	if caseName == CaseBlockedDag {
		return "metrics"
	}
	return ""
}

// Execute generates the fixture, runs the DAG, and formats the terminal
// summary. It never panics: construction and processing failures become a
// non-zero exit code with a structured message.
func Execute(opts RunOptions) (out RunOutcome) {
	defer func() {
		if r := recover(); r != nil {
			out.Summary = ""
			out.ExitCode = 1
			out.Err = fmt.Errorf("internal panic: %v", r)
		}
	}()
	fx, err := Generate(opts.Case, opts.Seed)
	if err != nil {
		return RunOutcome{ExitCode: 1, Err: err}
	}
	traceJSON, err := marshalTrace(fx.Trace)
	if err != nil {
		return RunOutcome{ExitCode: 1, Err: err}
	}
	pt, err := ParseTrace(traceJSON)
	if err != nil {
		return RunOutcome{ExitCode: 1, Err: err, Summary: failureSummary(opts, err)}
	}
	var mp4 []*MP4Track
	if len(fx.MP4Bytes) > 0 {
		mt, rerr := ReadMP4Tracks(fx.MP4Bytes)
		if rerr != nil {
			return RunOutcome{ExitCode: 1, Err: rerr, Summary: failureSummary(opts, rerr)}
		}
		mp4 = mt
	}
	cfg := demoConfig(opts.Case, opts.Seed, opts.Depth)
	p := &Pipeline{Trace: pt, Config: cfg, MP4: mp4, Notes: fx.Notes}
	d, err := p.BuildDag()
	if err != nil {
		return RunOutcome{ExitCode: 1, Err: err, Summary: failureSummary(opts, err)}
	}
	worker := NewWorker(cfg.Digest())
	res := worker.Run(d)
	sum := FormatSummary(opts, pt, res)
	if !res.Completed {
		var report *Report
		if r := findArtifact(res, "report"); r != nil {
			report = r.Value.(*Report)
		}
		if report == nil {
			report = failureReport(opts, cfg, pt, res)
		}
		return RunOutcome{Summary: sum, ExitCode: 1, Report: report, Err: firstBlockerError(res)}
	}
	report := findArtifact(res, "report").Value.(*Report)
	report.Nodes = res.Results
	report.ArtifactSHA256 = res.ArtifactDigest
	return RunOutcome{Summary: sum, ExitCode: 0, Report: report}
}

func findArtifact(res *RunResult, node NodeID) *Artifact {
	for _, a := range res.Artifacts {
		if a.Node == node {
			return a
		}
	}
	return nil
}

// failureReport preserves the partial DAG outcome so the JSON always records
// the first blocker and never claims completion.
func failureReport(opts RunOptions, cfg PipelineConfig, pt *ParsedTrace, res *RunResult) *Report {
	rep := &Report{
		Version: "1", Case: opts.Case, Seed: opts.Seed,
		Status: "failed", ExitCode: 1,
		Timebase:     fmt.Sprintf("%d/1", pt.Raw.Clock.RTPRateHz),
		InputDigest:  pt.InputDigest,
		ConfigDigest: cfg.Digest(),
		Units: ReportUnits{
			Bitrate: UnitBitsPerSec, Latency: UnitMilliseconds, Duration: UnitMilliseconds,
			Alignment: UnitMilliseconds, Quality: UnitScore,
		},
		Nodes: res.Results, GeneratedBy: "rtpreplay",
	}
	rep.Blocked = res.FirstBlocker
	for _, r := range res.Results {
		if r.State == StateFailed {
			rep.Failures = append(rep.Failures, r)
		}
	}
	rep.ArtifactSHA256 = res.ArtifactDigest
	return rep
}

// firstBlockerError renders the terminal failure cause for stderr.
func firstBlockerError(res *RunResult) error {
	if res.FirstBlocker == nil {
		return NewError(CodeInvalidInput, "run", "DAG did not complete but reported no blocker")
	}
	b := res.FirstBlocker
	if b.Error != nil {
		return b.Error
	}
	return NewError(CodeNodeBlocked, string(b.Node), b.Reason)
}

func marshalTrace(tr *Trace) ([]byte, error) { return marshalCanonical(tr) }

// failureSummary prints the fields that identify a failed run without
// pretending any metric is available.
func failureSummary(opts RunOptions, err error) string {
	var b strings.Builder
	fmt.Fprintf(&b, "case=%s seed=%d\n", opts.Case, opts.Seed)
	fmt.Fprintf(&b, "status=failed\nexit_code=1\n")
	se := AsError(err)
	fmt.Fprintf(&b, "error_code=%s\n", se.Code)
	if se.Track != "" {
		fmt.Fprintf(&b, "error_track=%s\n", se.Track)
	}
	if se.PacketSeq != nil {
		fmt.Fprintf(&b, "error_packet_seq=%d\n", *se.PacketSeq)
	}
	fmt.Fprintf(&b, "error=%s\n", se.Error())
	return b.String()
}

// WriteReport writes the JSON report to path atomically enough for a local
// acceptance run: write to a temp file in the same directory, then rename.
func WriteReport(path string, rep *Report) error {
	b, err := MarshalReport(rep)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
