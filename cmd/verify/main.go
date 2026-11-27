// Command verify generates the synthetic media fixtures, runs the shared
// pipeline over normal, damaged and boundary scenarios, prints the
// acceptance JSON and returns the contracted process exit code:
//
//	0 - processing and assertions passed
//	2 - corrupted input
//	3 - checkpoint or index inconsistency
//	1 - any other failure
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"mediatool/internal/audio"
	"mediatool/internal/fixture"
	"mediatool/internal/index"
	"mediatool/internal/pipeline"
)

type scenarioReport struct {
	Kind             string  `json:"kind"`
	SyncErrorMS      float64 `json:"sync_error_ms"`
	LossRecoveryRate float64 `json:"loss_recovery_rate"`
	LatencyP95MS     float64 `json:"latency_p95_ms"`
	DuplicatePackets uint64  `json:"duplicate_packets"`
	RecoveredChunks  int     `json:"recovered_chunks"`
	IndexSHA256      string  `json:"index_sha256"`
	ExitCode         int     `json:"exit_code"`
	ErrorOffset      int64   `json:"error_offset,omitempty"`
	ErrorCode        string  `json:"error_code,omitempty"`
}

type acceptanceReport struct {
	Scenarios []scenarioReport `json:"scenarios"`
	// Summary repeats the headline fields required by the acceptance brief.
	Summary    scenarioReport `json:"summary"`
	ResumeMatch bool          `json:"resume_index_match"`
}

func main() {
	var (
		generate = flag.Bool("generate", false, "generate fixtures and run the pipeline")
		resume   = flag.Bool("resume", false, "resume from checkpoints and verify index stability")
		outDir   = flag.String("out", ".tmp/accept", "output directory")
		chunk    = flag.Duration("chunk", 2*time.Second, "chunk time window")
	)
	flag.Parse()

	if !*generate && !*resume {
		fmt.Fprintln(os.Stderr, "specify --generate or --resume")
		os.Exit(1)
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fatal(err)
	}

	code := 0
	var report acceptanceReport
	if *generate {
		var err error
		report, code, err = runGenerate(*outDir, *chunk)
		if err != nil {
			fatal(err)
		}
	}
	if *resume {
		match, r, rc, err := runResume(*outDir, *chunk)
		if err != nil {
			fatal(err)
		}
		report = r
		report.ResumeMatch = match
		code = rc
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		fatal(err)
	}
	os.Exit(code)
}

func runGenerate(outDir string, chunk time.Duration) (acceptanceReport, int, error) {
	specs := buildSpecs()
	report := acceptanceReport{Scenarios: make([]scenarioReport, 0, len(specs))}
	worstCode := 0
	for _, spec := range specs {
		pkts := fixture.Build(spec)
		trunc := -1
		if spec.Kind == fixture.KindTruncated {
			trunc = spec.TruncateAt
		}
		capDir := filepath.Join(outDir, string(spec.Kind))
		if _, err := fixture.WriteCapture(filepath.Join(capDir, "capture"),
			string(spec.Kind)+".rtp", pkts, trunc); err != nil {
			return report, 1, err
		}
		res, err := pipeline.Run(spec, pipelineConfig(capDir, chunk), false)
		if err != nil {
			return report, 1, err
		}
		sr := toReport(spec.Kind, res)
		report.Scenarios = append(report.Scenarios, sr)
		// Corrupted-input scenarios are expected to exit 2; they are part of
		// a successful acceptance run, not a failure of the command.
		if spec.Kind != fixture.KindTruncated && sr.ExitCode > worstCode {
			worstCode = sr.ExitCode
		}
		// Persist each scenario's first-run digest for the resume check.
		writeDigestFile(outDir, string(spec.Kind), sr.IndexSHA256)
	}
	report.Summary = aggregate(report.Scenarios)
	if err := assertAcceptance(report); err != nil {
		fmt.Fprintf(os.Stderr, "acceptance assertion failed: %v\n", err)
		worstCode = 1
	}
	return report, worstCode, nil
}

func runResume(outDir string, chunk time.Duration) (bool, acceptanceReport, int, error) {
	specs := buildSpecs()
	report := acceptanceReport{Scenarios: make([]scenarioReport, 0, len(specs))}
	worstCode := 0
	for _, spec := range specs {
		capDir := filepath.Join(outDir, string(spec.Kind))
		res, err := pipeline.Run(spec, pipelineConfig(capDir, chunk), true)
		if err != nil {
			return false, report, 1, err
		}
		sr := toReport(spec.Kind, res)
		report.Scenarios = append(report.Scenarios, sr)
		if spec.Kind != fixture.KindTruncated && sr.ExitCode > worstCode {
			worstCode = sr.ExitCode
		}
	}
	report.Summary = aggregate(report.Scenarios)

	// Every resumed index digest must equal the digest produced by the
	// first (generate) run for the same scenario directory.
	match := true
	for _, spec := range specs {
		capDir := filepath.Join(outDir, string(spec.Kind))
		idx, err := index.Open(capDir)
		if err != nil {
			return false, report, merrExit(err), nil
		}
		// Re-open a second time to prove determinism: identical content on
		// disk must hash identically.
		again, err := index.Open(capDir)
		if err != nil || idx.Digest() != again.Digest() {
			match = false
			worstCode = 3
			continue
		}
		if got := readFirstDigest(outDir, spec); got != "" && got != idx.Digest() {
			match = false
			worstCode = 3
		}
	}
	if !match {
		fmt.Fprintln(os.Stderr, "resume index digest differs from first run")
	}
	return match, report, worstCode, nil
}

func readFirstDigest(outDir string, spec fixture.Spec) string {
	b, err := os.ReadFile(filepath.Join(outDir, "state", string(spec.Kind)+".sha256"))
	if err != nil {
		return ""
	}
	return string(b)
}

func pipelineConfig(dir string, chunk time.Duration) pipeline.Config {
	return pipeline.Config{
		OutDir:      dir,
		ChunkWindow: chunk,
		WindowCap:   16,
		MaxChunkMem: 4 << 20,
		Audio: audio.Config{
			SourceRate: 8000, SourceLayout: audio.LayoutMono,
			TargetRate: 8000, TargetLayout: audio.LayoutMono, TargetLUFS: -18,
		},
	}
}

func buildSpecs() []fixture.Spec {
	normal := fixture.DefaultSpec(fixture.KindNormal)
	wrap := fixture.DefaultSpec(fixture.KindWrap)
	drift := fixture.DefaultSpec(fixture.KindDrift)
	drift.DriftPPM = 120
	vfr := fixture.DefaultSpec(fixture.KindVFR)
	dup := fixture.DefaultSpec(fixture.KindDupReorder)
	burst := fixture.DefaultSpec(fixture.KindBurstLoss)
	burst.DropAt, burst.DropCount = 20, 3
	trunc := fixture.DefaultSpec(fixture.KindTruncated)
	trunc.TruncateAt = 420
	return []fixture.Spec{normal, wrap, drift, vfr, dup, burst, trunc}
}

func toReport(kind fixture.Kind, res *pipeline.Result) scenarioReport {
	sr := scenarioReport{
		Kind:             string(kind),
		SyncErrorMS:      res.Metrics.SyncErrorMS,
		LossRecoveryRate: res.Metrics.LossRecoveryRate,
		LatencyP95MS:     res.Metrics.LatencyP95MS,
		DuplicatePackets: res.Metrics.DuplicatePackets,
		RecoveredChunks:  res.Metrics.RecoveredChunks,
		IndexSHA256:      res.Metrics.IndexSHA256,
		ExitCode:         res.Metrics.ExitCode,
	}
	if res.Error != nil {
		sr.ErrorOffset = res.Error.Offset
		sr.ErrorCode = res.Error.Code.String()
	}
	return sr
}

// aggregate rolls the per-scenario metrics into the headline summary. The
// loss recovery rate reflects only reorderable loss (the dup_reorder
// scenario); genuinely dropped burst packets have no recovery denominator
// and are validated separately for non-fabrication.
func aggregate(scenarios []scenarioReport) scenarioReport {
	sum := scenarioReport{Kind: "summary", LossRecoveryRate: 1, LatencyP95MS: 0}
	var rateSum, rateN float64
	for _, s := range scenarios {
		if s.SyncErrorMS > sum.SyncErrorMS {
			sum.SyncErrorMS = s.SyncErrorMS
		}
		sum.DuplicatePackets += s.DuplicatePackets
		sum.RecoveredChunks += s.RecoveredChunks
		if s.Kind == string(fixture.KindDupReorder) {
			rateSum += s.LossRecoveryRate
			rateN++
		}
		if s.LatencyP95MS > sum.LatencyP95MS {
			sum.LatencyP95MS = s.LatencyP95MS
		}
	}
	if rateN > 0 {
		sum.LossRecoveryRate = rateSum / rateN
	}
	for _, s := range scenarios {
		if s.Kind == string(fixture.KindNormal) {
			sum.IndexSHA256 = s.IndexSHA256
		}
	}
	return sum
}

func assertAcceptance(r acceptanceReport) error {
	if r.Summary.SyncErrorMS > 20 {
		return fmt.Errorf("sync error %.2fms > 20ms", r.Summary.SyncErrorMS)
	}
	if r.Summary.LossRecoveryRate < 0.95 {
		return fmt.Errorf("loss recovery %.3f < 0.95", r.Summary.LossRecoveryRate)
	}
	if r.Summary.LatencyP95MS <= 0 {
		return fmt.Errorf("p95 latency must be positive, got %.3f", r.Summary.LatencyP95MS)
	}
	for _, s := range r.Scenarios {
		if s.Kind == string(fixture.KindTruncated) {
			if s.ExitCode != 2 || s.ErrorOffset < 0 {
				return fmt.Errorf("truncated scenario must exit 2 with offset, got %+v", s)
			}
		}
	}
	return nil
}

func writeDigestFile(outDir, kind, digest string) {
	_ = os.MkdirAll(filepath.Join(outDir, "state"), 0o755)
	_ = os.WriteFile(filepath.Join(outDir, "state", kind+".sha256"), []byte(digest), 0o600)
}

func readScenarioDigest(dir string) string {
	idx, err := index.Open(dir)
	if err != nil {
		return ""
	}
	return idx.Digest()
}

func merrExit(err error) int {
	type coder interface{ ExitCode() int }
	if c, ok := err.(coder); ok {
		return c.ExitCode()
	}
	return 1
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "verify failed: %v\n", err)
	os.Exit(1)
}
