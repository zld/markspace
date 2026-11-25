package app

import (
	"context"
	"fmt"
	"path/filepath"

	"mediacore/internal/fixture"
	"mediacore/internal/manifest"
	"mediacore/internal/pipe"
	"mediacore/internal/provenance"
)

type firstRunFile struct {
	DASH Summary `json:"dash"`
	HLS  Summary `json:"hls"`
}

// Demo generates local fixtures and runs the normal DASH/HLS pipelines once.
func Demo(ctx context.Context, work string, memoryMB int) error {
	work, err := filepath.Abs(work)
	if err != nil {
		return err
	}
	if memoryMB <= 0 {
		memoryMB = 64
	}
	if err := ensureWork(work); err != nil {
		return err
	}
	bundle, err := fixture.GenerateAll(filepath.Join(work, "fixtures"))
	if err != nil {
		return err
	}
	if err := provenance.WriteAtomicJSON(filepath.Join(work, "bundle.json"), bundle); err != nil {
		return err
	}

	dashSummary, err := runAndPrint(ctx, "DASH", bundle.NormalDASH, work, filepath.Join(work, "runs", "dash"), memoryMB)
	if err != nil {
		return err
	}
	hlsSummary, err := runAndPrint(ctx, "HLS", bundle.NormalHLS, work, filepath.Join(work, "runs", "hls"), memoryMB)
	if err != nil {
		return err
	}
	first := firstRunFile{DASH: dashSummary, HLS: hlsSummary}
	if err := provenance.WriteAtomicJSON(filepath.Join(work, "first-run.json"), first); err != nil {
		return err
	}
	fmt.Printf("\nDemo complete. Work directory: %s\n", work)
	fmt.Printf("Next: go run ./cmd/mediacore verify --work %s\n", work)
	return nil
}

func ensureWork(work string) error {
	return mkdirAll(work)
}

func runAndPrint(ctx context.Context, system, manifestPath, workRoot, runDir string, memoryMB int) (Summary, error) {
	cfg := pipe.DefaultConfig(runDir)
	cfg.MaxBufferBytes = int64(memoryMB) << 20

	var entries []manifest.Entry
	switch system {
	case "DASH":
		d, err := manifest.ParseDASH(manifestPath, workRoot)
		if err != nil {
			return Summary{}, err
		}
		entries = d.Entries
	case "HLS":
		h, err := manifest.ParseHLS(manifestPath, workRoot)
		if err != nil {
			return Summary{}, err
		}
		entries = h.Entries
	default:
		return Summary{}, fmt.Errorf("unknown system %s", system)
	}
	inputDigest, _, err := provenance.CombinedInputDigest(manifestPath, entries)
	if err != nil {
		return Summary{}, err
	}
	report, err := pipe.ProcessEntries(ctx, entries, inputDigest, cfg)
	if err != nil {
		return BuildSummary(report), err
	}
	printReport(system, report)
	return BuildSummary(report), nil
}

func printReport(system string, r *pipe.RunReport) {
	fmt.Printf("\n== %s mapping ==\n", system)
	fmt.Printf("input_digest=%s config_digest=%s checkpoint=%s\n", r.InputDigest, r.ConfigDigest[:12], r.CheckpointID[:12])
	for _, o := range r.Outcomes {
		e := o.Entry
		status := "OK"
		detail := ""
		if o.AlignmentErr != nil {
			status = "ALIGNMENT_FAIL"
			p := o.FailurePosition()
			detail = fmt.Sprintf(" kind=%s deviation=%dU limit=20000000U at %s:%d", p.Kind, e.DeviationU, p.Representation, p.EntryIndex)
		}
		if o.Err != nil {
			status = "ERROR"
			p := o.FailurePosition()
			detail = " " + p.Message
		}
		samples := 0
		if o.Index != nil {
			samples = len(o.Index.Samples)
		}
		fmt.Printf("%-28s sys=%s rep=%-6s seq=%-3d U=[%d,%d) samples=%d align=%v dev=%d %s%s\n",
			e.StableKey, e.System, e.Representation, e.OriginalSequence,
			e.ProgramStartU, e.ProgramEndU, samples, e.Aligned, e.DeviationU, status, detail)
		if o.Range != nil {
			fmt.Printf("    range n=%d tickDev=[%d,%d] byteOff=%d byteEnd=%d byteDev=[%d,%d]\n",
				o.Range.SampleCount, o.Range.FirstTickDeviation, o.Range.LastTickDeviation,
				o.Range.FirstByteOffset, o.Range.LastByteEnd,
				o.Range.FirstByteDeviation, o.Range.LastByteDeviation)
		}
	}
	fmt.Printf("queue_peak(job)=%d queue_peak(result)=%d buffer_peak=%d completed=%v\n",
		r.JobQueuePeak, r.ResultQueuePeak, r.BufferPeak, r.Completed)
}
