package main

import (
	"fmt"
	"os"
	"path/filepath"

	"rtpreplay/fingerprint"
	"rtpreplay/ingest"
	"rtpreplay/media"
	"rtpreplay/report"
)

// metricTolerances keep the comparison focused on media timing rather than
// record bookkeeping: indices shift when packets were dropped.
const (
	tolSyncMS   = 20.0
	tolP95MS    = 500.0
	tolRate     = 0.1
	tolRegionMS = 20.0
)

// compareReference reruns the pipeline against the pristine reference tree
// and checks the repaired summary against it. Mismatches are printed and
// attached to the summary; the core acceptance metrics use fixed tolerances.
func compareReference(opts options, repaired *report.Summary) {
	refRoot := filepath.Join(opts.input, "reference")
	if _, err := os.Stat(refRoot); err != nil {
		// No reference available (normal fixture): nothing to compare against.
		return
	}
	refOpts := options{input: refRoot, bitrates: opts.bitrates, jsonOut: true}
	refSummary, err := execute(refOpts)
	if err != nil || refSummary == nil {
		repaired.AttachError(media.NewError(media.KindUnrecoverable, "REFERENCE_UNREADABLE",
			"regress", -1, "reference run failed: %v", err))
		return
	}

	var mismatches []string
	check := func(name string, ok bool, detail string) {
		if !ok {
			mismatches = append(mismatches, name+": "+detail)
		}
	}
	check("sync_error_ms", approx(repaired.SyncErrorMS, refSummary.SyncErrorMS, tolSyncMS),
		fmt.Sprintf("%v vs %v", repaired.SyncErrorMS, refSummary.SyncErrorMS))
	check("latency_p95_ms", approx(repaired.LatencyP95MS, refSummary.LatencyP95MS, tolP95MS),
		fmt.Sprintf("%v vs %v", repaired.LatencyP95MS, refSummary.LatencyP95MS))
	check("loss_recovery_rate", approx(repaired.LossRecoveryRate, 1.0, tolRate),
		fmt.Sprintf("%v", repaired.LossRecoveryRate))
	// Segment counts legitimately differ while packets are missing; the
	// repaired tree (make recover) restores them, which is checked separately.
	check("silence_region_count", len(repaired.SilenceRegions) == len(refSummary.SilenceRegions),
		fmt.Sprintf("%d vs %d", len(repaired.SilenceRegions), len(refSummary.SilenceRegions)))
	checkSilenceTiming(repaired, refSummary, &mismatches)
	checkDuplicateFingerprints(repaired, refSummary, &mismatches)

	if len(mismatches) > 0 {
		repaired.AttachError(media.NewError(media.KindUnrecoverable, "REFERENCE_MISMATCH",
			"regress", -1, "%v", mismatches))
		for _, m := range mismatches {
			fmt.Fprintln(os.Stderr, "reference mismatch: "+m)
		}
	}
}

func checkSilenceTiming(a, b *report.Summary, mismatches *[]string) {
	n := len(a.SilenceRegions)
	if n != len(b.SilenceRegions) {
		return
	}
	for i := range a.SilenceRegions {
		x, y := a.SilenceRegions[i], b.SilenceRegions[i]
		if !approx(x.StartMS, y.StartMS, tolRegionMS) {
			*mismatches = append(*mismatches, fmt.Sprintf(
				"silence[%d].start_ms %v vs %v", i, x.StartMS, y.StartMS))
		}
		if !approx(x.EndMS, y.EndMS, tolRegionMS) {
			*mismatches = append(*mismatches, fmt.Sprintf(
				"silence[%d].end_ms %v vs %v", i, x.EndMS, y.EndMS))
		}
	}
}

func checkDuplicateFingerprints(a, b *report.Summary, mismatches *[]string) {
	setA := dupSet(a.Duplicates)
	setB := dupSet(b.Duplicates)
	if len(setA) != len(setB) {
		*mismatches = append(*mismatches, fmt.Sprintf(
			"duplicate fingerprint set size %d vs %d", len(setA), len(setB)))
		return
	}
	for fp := range setA {
		if !setB[fp] {
			*mismatches = append(*mismatches, "duplicate fingerprint missing after repair: "+fp)
		}
	}
}

func dupSet(in []fingerprint.Duplicate) map[string]bool {
	out := map[string]bool{}
	for _, d := range in {
		out[d.Fingerprint] = true
	}
	return out
}

func approx(a, b, tol float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= tol
}

// attachContainerErrors parses MP4/MXF metadata and records any locatable
// error (e.g. truncated box, bad BER) on the summary without aborting.
func attachContainerErrors(root string, summary *report.Summary) {
	metaDir := filepath.Join(root, "meta")
	if _, err := ingest.ReadMP4File(filepath.Join(metaDir, "sample.mp4")); err != nil {
		summary.AttachError(err)
	}
	if _, err := ingest.ReadMXFFile(filepath.Join(metaDir, "sample.mxf")); err != nil {
		summary.AttachError(err)
	}
}
