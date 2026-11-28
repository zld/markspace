package demo

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"mediarc/model"
)

// writeManifest records the review checklist next to the artefacts.
func (r *runner) writeManifest() error {
	lines := []string{
		"mediarc acceptance manifest",
		fmt.Sprintf("seed=%d window=%d max-buffer=%d", r.opts.Seed, r.opts.Window, r.opts.MaxBuffer),
	}
	for _, scenario := range r.report.Scenarios {
		lines = append(lines, "", "["+scenario.Name+"] "+scenario.Detail)
		for _, check := range scenario.Checks {
			status := "ok"
			if !check.OK {
				status = "FAIL"
			}
			lines = append(lines, fmt.Sprintf("  %s %s: got %s want %s", status, check.What, check.Got, check.Want))
		}
	}
	lines = append(lines, "",
		fmt.Sprintf("frame-hash=%016x", r.metrics.FrameHash),
		fmt.Sprintf("segment-hash=%016x", r.metrics.SegmentHash),
		fmt.Sprintf("pixel-mean=%.4f", r.metrics.PixelMean),
		fmt.Sprintf("peak-dbfs=%.4f", r.metrics.PeakDBFS),
		fmt.Sprintf("packets-lost=%d", r.metrics.PacketsLost),
		fmt.Sprintf("recovered=%d", r.metrics.Recovered),
		fmt.Sprintf("first-unreadable=%d", r.metrics.FirstBadOff),
		fmt.Sprintf("max-alignment-error-ns=%d", r.metrics.MaxAlignErr),
		fmt.Sprintf("cue-checksum=%016x", r.metrics.CueChecksum),
	)
	if !r.report.OK() {
		lines = append(lines, "", "failures:")
		lines = append(lines, r.report.Failures...)
	}
	sorted := make([]string, len(r.report.Fixtures))
	copy(sorted, r.report.Fixtures)
	sort.Strings(sorted)
	lines = append(lines, "", "fixtures:")
	lines = append(lines, sorted...)
	path := filepath.Join(r.opts.Dir, "manifest.txt")
	if err := os.WriteFile(path, []byte(joinLines(lines)), 0o644); err != nil {
		return model.Fatal(model.CodeInvalidArgument, "demo", "cannot write manifest").WithCause(err)
	}
	r.report.Manifest = path
	return nil
}

func joinLines(lines []string) string {
	out := ""
	for _, line := range lines {
		out += line + "\n"
	}
	return out
}
