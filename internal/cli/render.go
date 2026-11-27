package cli

import (
	"fmt"
	"io"
	"math"
	"strings"

	"example.com/assetpack/internal/manifest"
	"example.com/assetpack/internal/migrate"
	"example.com/assetpack/internal/pipeline"
)

// render prints the report: stage migration counts, resume positions, fault
// summary, per-asset before/after audio figures, re-run diff counts and the
// final manifest state.
func render(out *pipeline.Outcome, runErr error, resumed bool, stdout, stderr io.Writer) {
	if out == nil {
		fmt.Fprintf(stderr, "run failed before any state was produced: %v\n", runErr)
		return
	}
	w := stdout
	sep := strings.Repeat("-", 72)

	fmt.Fprintln(w, "assetpack migration report")
	fmt.Fprintln(w, sep)
	if out.Recovered != nil {
		fmt.Fprintf(w, "recovery       : %s (%s)\n", out.Recovered.OldOrNew, out.Recovered.Detail)
	}
	if resumed {
		fmt.Fprintln(w, "mode           : resumed (existing release consulted)")
	} else {
		fmt.Fprintln(w, "mode           : fresh")
	}

	fmt.Fprintln(w, sep)
	fmt.Fprintln(w, "stage migration")
	for _, line := range out.Journal.Summary() {
		fmt.Fprintf(w, "  %s\n", line)
	}
	completed := 0
	for _, s := range migrate.Order {
		r, _ := out.Journal.Get(s)
		if r.Status == migrate.StatusDone || r.Status == migrate.StatusSkipped {
			completed++
		}
	}
	fmt.Fprintf(w, "  stages completed : %d / %d\n", completed, len(migrate.Order))
	fmt.Fprintf(w, "  resume offset    : %d\n", out.Resume.Offset)
	fmt.Fprintf(w, "  resume sample    : %d\n", out.Resume.Sample)

	kinds := out.Faults.CountByKind()
	fmt.Fprintln(w, sep)
	fmt.Fprintf(w, "faults          : %d kind(s), %d total\n", len(kinds), len(out.Faults.Items))
	for _, k := range kinds {
		fmt.Fprintf(w, "  %-18s x%d\n", k.Kind, k.Count)
	}
	for _, f := range out.Faults.Items {
		fmt.Fprintf(w, "  - %s\n", f.Error())
	}

	fmt.Fprintln(w, sep)
	fmt.Fprintln(w, "audio normalization")
	var before, after int64
	var peakIn, peakOut float64
	var outRate int
	for _, a := range out.Assets {
		before += a.Report.InFrames
		after += a.Report.OutFrames
		outRate = a.Report.OutRate
		if a.Report.InPeak > peakIn {
			peakIn = a.Report.InPeak
		}
		if a.Report.OutPeak > peakOut {
			peakOut = a.Report.OutPeak
		}
		fmt.Fprintf(w, "  %-7s %5d Hz %-7s -> %5d Hz %-6s frames %6d -> %6d peak %.4f -> %.4f lufs %s gaps %d\n",
			a.Name, a.Report.InRate, a.Report.InLayout, a.Report.OutRate, a.Report.OutLayout,
			a.Report.InFrames, a.Report.OutFrames, a.Report.InPeak, a.Report.OutPeak,
			fmtLufs(a.Report.OutLoudnessLUFS), a.Report.GapSamples)
		fmt.Fprintf(w, "  %-7s container %-10s bytes=%d samples=%d chunks=%d pmt-v%d pcr-wraps=%d\n",
			"", a.Display, a.Index.Size, a.Index.Samples, a.Index.Chunks, a.PMTVersion, a.PCRWraps)
		fmt.Fprintf(w, "  %-7s artifact sha256 %s\n", "", a.Digest())
	}
	fmt.Fprintf(w, "  total frames     : %d -> %d at %d Hz\n", before, after, outRate)
	fmt.Fprintf(w, "  peak             : %.4f -> %.4f\n", peakIn, peakOut)

	fmt.Fprintln(w, sep)
	fmt.Fprintln(w, "manifest")
	fmt.Fprintf(w, "  before          : %s\n", manifestLine(out.Before))
	fmt.Fprintf(w, "  added           : %d\n", out.Diff.Added)
	fmt.Fprintf(w, "  reused          : %d\n", out.Diff.Reused)
	fmt.Fprintf(w, "  replaced        : %d\n", out.Diff.Replaced)
	fmt.Fprintf(w, "  removed         : %d\n", out.Diff.Removed)
	fmt.Fprintf(w, "  after           : %s\n", manifestLine(out.After))
	if out.ManifestSHA != "" {
		fmt.Fprintf(w, "  manifest sha256 : %s\n", out.ManifestSHA)
	}
	fmt.Fprintf(w, "  commit point    : %t\n", out.Committed)
	validText := "invalid"
	if out.Valid {
		validText = "valid"
	}
	if out.After == nil {
		validText = "none"
	}
	fmt.Fprintf(w, "  manifest check  : %s\n", validText)

	if out.Failure != nil {
		fmt.Fprintf(w, "failure         : %s\n", out.Failure.Error())
	} else if runErr != nil {
		fmt.Fprintf(w, "failure         : %v\n", runErr)
	}
	fmt.Fprintln(w, sep)
	status := "OK"
	if !out.RunOK || !out.Committed || !out.Valid {
		status = "FAILED"
	}
	fmt.Fprintf(w, "result          : %s\n", status)
}

func manifestLine(m *manifest.V2) string {
	if m == nil {
		return "none"
	}
	return m.Summary()
}

func fmtLufs(v float64) string {
	if math.IsInf(v, -1) {
		return "-inf"
	}
	return fmt.Sprintf("%.1f", v)
}
