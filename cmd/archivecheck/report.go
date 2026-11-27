package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"archivecheck/internal/asset"
	"archivecheck/internal/fault"
	"archivecheck/internal/manifest"
	"archivecheck/internal/stream"
	"archivecheck/internal/subtitle"
	"archivecheck/internal/timebase"
)

// printAnalysis renders structure, subtitle and timecode findings.
func printAnalysis(out io.Writer, res *analysis) {
	fmt.Fprintln(out, "== structure =============================================")
	for _, name := range []string{"clip-valid.mp4", "clip-truncated.mp4"} {
		st := res.mp4[name]
		fmt.Fprintf(out, "  %-20s boxes=%d brand=%s damage=%d\n", name, st.BoxCount, st.Brand, len(st.Damaged))
		for _, span := range st.Damaged {
			fmt.Fprintf(out, "    damaged offset=%d..%d kind=%s %s\n", span.Start, span.End, span.Kind, span.Detail)
		}
		for _, tr := range st.Tracks {
			fmt.Fprintf(out, "    track %d handler=%s samples=%d keyframes=%d mean interval=%s\n",
				tr.ID, tr.Handler, tr.SampleCount, len(tr.Keyframes), time.Duration(tr.Interval))
			if len(tr.Keyframes) > 0 {
				fmt.Fprintf(out, "      keyframes=%s\n", keyOffsets(tr.Keyframes))
			}
			if len(tr.Intervals) > 0 {
				fmt.Fprintf(out, "      intervals=%s\n", durations(tr.Intervals))
			}
		}
	}
	fmt.Fprintf(out, "  %-20s packets=%d lost=%d gaps=%d duration=%s\n",
		"capture-loss.rtp", res.rtp.Packets, res.rtp.Lost, len(res.rtp.Gaps), time.Duration(res.rtp.Duration))
	for _, gap := range res.rtp.Gaps {
		fmt.Fprintf(out, "    gap expected=%d got=%d missing=%d at byte %d\n", gap.Expected, gap.Got, gap.Missing, gap.Offset)
	}
	fmt.Fprintln(out)

	fmt.Fprintln(out, "== subtitles ==============================================")
	for _, tr := range []*subtitle.Track{res.srt, res.vtt} {
		fmt.Fprintf(out, "  %-24s format=%s cues=%d problems=%d\n", filepath.Base(tr.Source), tr.Format, len(tr.Cues), len(tr.Problems))
		for _, c := range tr.Cues {
			fmt.Fprintf(out, "    cue %d id=%q %s..%s lines=%d offset=%d\n", c.Index, c.ID, ms(c.Start), ms(c.End), len(c.Lines), c.Offset)
		}
		for _, p := range tr.Problems {
			fe := faultOf(p.Err)
			fmt.Fprintf(out, "    problem offset=%d kind=%s %s\n", p.Offset, fe.Kind, fe.Detail)
		}
	}
	fmt.Fprintln(out)

	fmt.Fprintln(out, "== timecodes ==============================================")
	for _, v := range res.codes {
		fmt.Fprintf(out, "  %-10s frames=%-8d nanos=%d\n", v.Text, v.Frames, int64(v.Nanos))
	}
	for _, err := range res.bad {
		fe := faultOf(err)
		fmt.Fprintf(out, "  rejected offset=%d %s/%s %s\n", fe.Offset, fe.Stage, fe.Kind, fe.Detail)
	}
	fmt.Fprintln(out)
}

// printPipeline renders stage status, resume boundaries and the shared cause.
func printPipeline(out io.Writer, res *analysis) {
	fmt.Fprintln(out, "== stage status ==========================================")
	for i, rep := range res.reports {
		fmt.Fprintf(out, "  run %d: %s\n", i+1, stageLine(rep))
		if cause := rep.Cause(); cause != nil {
			fmt.Fprintf(out, "    shared cause: %v\n", cause)
		}
		fmt.Fprintf(out, "    peak outstanding=%d ceiling=%d backpressure waits=%d\n",
			rep.PeakOutstanding, rep.OutstandingCeiling, rep.BackpressureWaits)
	}
	if len(res.resumeStages) > 0 {
		fmt.Fprintln(out, "  checkpoint resume state:")
		for _, st := range res.resumeStages {
			fmt.Fprintf(out, "    stage=%-6s status=%s offset=%d chunks=%d bytes=%d digest=%s\n",
				st.Name, st.Status, st.Offset, st.Chunks, st.Bytes, short(st.Digest))
		}
	}
	fmt.Fprintf(out, "  cancellation reason: %s\n", res.cancelReason)
	fmt.Fprintf(out, "  resume byte offset: %d\n", res.resumeOffset)
	if !res.pipelineHealthy {
		fmt.Fprintln(out, "  WARNING: bounded-memory expectations were not met")
	}
	fmt.Fprintln(out)
}

func keyOffsets(kfs []stream.Keyframe) string {
	parts := make([]string, 0, len(kfs))
	for _, k := range kfs {
		parts = append(parts, fmt.Sprintf("#%d@%d", k.Number, k.Offset))
	}
	return strings.Join(parts, " ")
}

func durations(ns []timebase.Nanos) string {
	parts := make([]string, 0, len(ns))
	for _, n := range ns {
		parts = append(parts, time.Duration(n).String())
	}
	return strings.Join(parts, " ")
}

// verifyManifest and the stored-checksum helpers live here so both commands
// share one implementation.
func verifyManifest(dir string) (*manifest.Manifest, error) {
	m, err := manifest.Verify(dir)
	if err != nil {
		return nil, err
	}
	return m, nil
}

// faultOf narrows an error to the shared fault model.
func faultOf(err error) *fault.Err {
	if fe := fault.As(err); fe != nil {
		return fe
	}
	return fault.New(fault.StageDemo, fault.KindCorrupt, "", 0, "%v", err)
}

func newFault(offset int64, format string, args ...any) error {
	return fault.New(fault.StageTimecode, fault.KindInvalid, "timecodes.txt", offset, format, args...)
}

// writeFileAtomic writes data through the shared atomic-write helper.
func writeFileAtomic(path string, data []byte) (string, error) {
	return asset.WriteAtomic(path, data)
}

func loadStoredChecksum(stateDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(stateDir, "manifest-checksum.txt"))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func saveStoredChecksum(stateDir, sum string) error {
	path := filepath.Join(stateDir, "manifest-checksum.txt")
	_, err := writeFileAtomic(path, []byte(sum+"\n"))
	return err
}
