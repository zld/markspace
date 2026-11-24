package main

import (
	"fmt"
	"time"

	"archive/internal/model"
	"archive/internal/recover"
	"archive/internal/scan"
	"archive/internal/sync"
)

// printReport renders the acceptance report: damage localisation error,
// recovery wall time, packet counters, A/V deviation and the output digest.
func printReport(dir string, res *scan.Result, cp *recover.Checkpoint, elapsed time.Duration) {
	fmt.Println("== archive report ==")
	fmt.Printf("input:  %s/input.mp4 sha256=%s size=%d\n", dir, cp.InputSHA256[:12], cp.InputSize)
	fmt.Printf("format: %s duration=%.1fms\n", res.Format, float64(res.DurationNs)/1e6)
	for _, t := range res.Tracks {
		line := fmt.Sprintf("track #%d %-5s timescale=%d samples=%d", t.ID, t.Type, t.Timescale, len(t.Samples))
		if t.HasRotation {
			line += fmt.Sprintf(" rotation=%d", t.Rotation)
		}
		if t.Crop != nil {
			line += fmt.Sprintf(" crop=%d/%d/%d/%d", t.Crop.Top, t.Crop.Bottom, t.Crop.Left, t.Crop.Right)
		}
		if t.Color != nil {
			line += fmt.Sprintf(" color=%d/%d/%d", t.Color.Primaries, t.Color.Transfer, t.Color.Matrix)
		}
		if t.ColorConflict && t.ColorAlt != nil {
			line += fmt.Sprintf(" CONFLICT(alt=%d/%d/%d)", t.ColorAlt.Primaries, t.ColorAlt.Transfer, t.ColorAlt.Matrix)
		}
		fmt.Println(line)
	}
	for _, e := range res.Errors {
		fmt.Printf("error: %s\n", e)
	}
	if meta := loadMeta(dir); meta != nil {
		reportOffsetError("corrupt", firstOffset(res.Errors, model.ErrEssenceCRC), meta.CorruptAt, meta.CorruptPacketSize)
		reportOffsetError("truncate", minSampleOffset(res.Errors, model.ErrTruncated), meta.TruncateAt, meta.TruncatePacketSize)
	}
	fmt.Printf("packets: recovered=%d skipped=%d lost=%d\n", cp.Recovered, cp.Skipped, cp.Lost)
	fmt.Printf("A/V max deviation: %.3fms (tolerance %.1fms, violations=%d)\n",
		model.DurationMillis(cp.MaxDevNs), model.DurationMillis(sync.DefaultToleranceNs), cp.AVViolations)
	fmt.Printf("recovery time: %d ms\n", elapsed.Milliseconds())
	fmt.Printf("output sha256: %s\n", cp.FinalSHA256)
	if cp.DamagedInput {
		fmt.Println("status: damaged input, partial recovery complete (exit 2)")
	} else {
		fmt.Println("status: clean (exit 0)")
	}
}

func firstOffset(errs model.Errors, code model.ErrCode) int64 {
	for _, e := range errs {
		if e.Code == code {
			return e.Offset
		}
	}
	return -1
}

// minSampleOffset returns the smallest offset among sample-level (track >= 0)
// errors with the given code: the earliest point at which truncation was
// localised.
func minSampleOffset(errs model.Errors, code model.ErrCode) int64 {
	best := int64(-1)
	for _, e := range errs {
		if e.Code == code && e.Track >= 0 && (best < 0 || e.Offset < best) {
			best = e.Offset
		}
	}
	return best
}

// reportOffsetError prints detected-vs-actual damage offset and checks the
// localisation guarantee: error must not exceed one media packet.
func reportOffsetError(kind string, detected, actual, packetSize int64) {
	if actual < 0 {
		return
	}
	if detected < 0 {
		fmt.Printf("%s offset: actual=%d NOT DETECTED\n", kind, actual)
		return
	}
	err := actual - detected
	if err < 0 {
		err = -err
	}
	verdict := "OK"
	if packetSize > 0 && err > packetSize {
		verdict = "FAIL"
	}
	fmt.Printf("%s offset: detected=%d actual=%d error=%dB (<= 1 packet of %dB) %s\n",
		kind, detected, actual, err, packetSize, verdict)
}
