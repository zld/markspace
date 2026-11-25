package app

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"

	"mediacore/internal/diagnostic"
	"mediacore/internal/fixture"
	"mediacore/internal/manifest"
	"mediacore/internal/pipe"
	"mediacore/internal/provenance"
)

type CheckResult struct {
	Name         string              `json:"name"`
	OK           bool                `json:"ok"`
	ExpectedExit int                 `json:"expected_exit"`
	ObservedExit int                 `json:"observed_exit"`
	Position     diagnostic.Position `json:"position"`
	Message      string              `json:"message,omitempty"`
}

type VerifyReport struct {
	Normal []StableItem  `json:"normal"`
	Checks []CheckResult `json:"checks"`
}

// Verify reruns recovery and negative acceptance cases. It returns nil when all
// expected outcomes are observed; expected corrupt/checkpoint cases are represented
// by CheckResult rather than failing the whole acceptance command.
func Verify(ctx context.Context, work string, memoryMB int, onlyCase string) error {
	work, err := filepath.Abs(work)
	if err != nil {
		return err
	}
	bundle := &fixture.Bundle{}
	if err := provenance.ReadJSON(filepath.Join(work, "bundle.json"), bundle); err != nil {
		return fmt.Errorf("read bundle.json; run demo first: %w", err)
	}
	if onlyCase != "" {
		return runSingleCase(ctx, bundle, work, memoryMB, onlyCase)
	}

	var first firstRunFile
	if err := provenance.ReadJSON(filepath.Join(work, "first-run.json"), &first); err != nil {
		return fmt.Errorf("read first-run.json; run demo first: %w", err)
	}
	report := &VerifyReport{}

	dashItems, err := verifyManifest(ctx, "DASH", bundle.NormalDASH, work, filepath.Join(work, "runs", "dash"), memoryMB, first.DASH, report)
	if err != nil {
		return err
	}
	hlsItems, err := verifyManifest(ctx, "HLS", bundle.NormalHLS, work, filepath.Join(work, "runs", "hls"), memoryMB, first.HLS, report)
	if err != nil {
		return err
	}
	report.Normal = append(report.Normal, dashItems...)
	report.Normal = append(report.Normal, hlsItems...)

	for _, c := range bundle.Cases {
		report.Checks = append(report.Checks, runExpectedCase(ctx, c, work, memoryMB))
	}
	report.Checks = append(report.Checks, runChangedInputExpected(ctx, bundle, work, memoryMB))

	allOK := true
	for _, c := range report.Checks {
		status := "PASS"
		if !c.OK {
			status = "FAIL"
			allOK = false
		}
		fmt.Printf("NEGATIVE %-24s %s exit=%d/%d pos=%s rep=%s entry=%d byte=%d\n",
			c.Name, status, c.ObservedExit, c.ExpectedExit, c.Position.Kind,
			c.Position.Representation, c.Position.EntryIndex, c.Position.ByteOffset)
		if c.Message != "" {
			fmt.Printf("    %s\n", c.Message)
		}
	}
	if err := provenance.WriteAtomicJSON(filepath.Join(work, "verify-report.json"), report); err != nil {
		return err
	}
	if !allOK {
		return diagnostic.New(nil, diagnostic.Location{Stage: "verify"}, "one or more acceptance checks failed")
	}
	fmt.Println("\nVERIFY PASS: normal deviations are zero; negative locations and exit codes matched; checkpoint summaries are stable.")
	return nil
}

func verifyManifest(ctx context.Context, system, manifestPath, work, runDir string, memoryMB int, first Summary, report *VerifyReport) ([]StableItem, error) {
	cfg := pipe.DefaultConfig(runDir)
	if memoryMB > 0 {
		cfg.MaxBufferBytes = int64(memoryMB) << 20
	}
	var entries []manifest.Entry
	switch system {
	case "DASH":
		d, err := manifest.ParseDASH(manifestPath, work)
		if err != nil {
			return nil, err
		}
		entries = d.Entries
	case "HLS":
		h, err := manifest.ParseHLS(manifestPath, work)
		if err != nil {
			return nil, err
		}
		entries = h.Entries
	}
	digest, _, err := provenance.CombinedInputDigest(manifestPath, entries)
	if err != nil {
		return nil, err
	}
	run, err := pipe.ProcessEntries(ctx, entries, digest, cfg)
	if err != nil {
		return nil, err
	}
	current := BuildSummary(run)
	out := make([]StableItem, 0, len(run.Outcomes))
	for _, o := range run.Outcomes {
		item := current.Items[o.Entry.StableKey]
		out = append(out, item)
		want, ok := first.Items[o.Entry.StableKey]
		if !ok {
			return nil, fmt.Errorf("missing first-run summary for %s", o.Entry.StableKey)
		}
		if !reflect.DeepEqual(want, item) {
			return nil, fmt.Errorf("stable summary changed for %s\nfirst=%+v\nnow=%+v", o.Entry.StableKey, want, item)
		}
		if !o.Skipped {
			return nil, fmt.Errorf("checkpoint did not reuse completed entry %s", o.Entry.StableKey)
		}
		if o.Entry.Aligned {
			if item.FirstTickDeviation != 0 || item.LastTickDeviation != 0 ||
				item.FirstByteDeviation != 0 || item.LastByteDeviation != 0 {
				return nil, fmt.Errorf("non-zero normal deviation for %s: tick=[%d,%d] byte=[%d,%d]",
					o.Entry.StableKey, item.FirstTickDeviation, item.LastTickDeviation,
					item.FirstByteDeviation, item.LastByteDeviation)
			}
		} else if o.AlignmentErr == nil {
			return nil, fmt.Errorf("unaligned entry %s was not marked", o.Entry.StableKey)
		}
		printVerifyItem(item, o)
	}
	if len(run.Completed) != len(entries) {
		return nil, fmt.Errorf("checkpoint completed %d entries, want %d", len(run.Completed), len(entries))
	}
	return out, nil
}

func printVerifyItem(item StableItem, o *pipe.ItemOutcome) {
	failure := ""
	if pos := o.FailurePosition(); pos.Kind != "" || o.Err != nil || o.AlignmentErr != nil {
		failure = fmt.Sprintf(" failure={kind=%s rep=%s entry=%d byte=%d}", pos.Kind, pos.Representation, pos.EntryIndex, pos.ByteOffset)
	}
	fmt.Printf("ITEM %s\n", item.StableKey)
	fmt.Printf("  input=%s index=%s U=[%d,%d) n=%d tickDev=[%d,%d] byteOff=%d byteEnd=%d byteDev=[%d,%d]\n",
		short(item.InputSHA256), short(item.IndexDigest), item.UnifiedStartTick, item.UnifiedEndTick,
		item.SampleCount, item.FirstTickDeviation, item.LastTickDeviation,
		item.FirstByteOffset, item.LastByteEnd, item.FirstByteDeviation, item.LastByteDeviation)
	fmt.Printf("  source manifest=%s rep=%s entry=%d seq=%d key=%s aligned=%v%s\n",
		item.Manifest, item.Representation, item.EntryIndex, item.OriginalSequence,
		item.SourceEntryKey, item.Aligned, failure)
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
