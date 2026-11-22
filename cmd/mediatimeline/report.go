package main

import (
	"encoding/json"
	"fmt"
	"os"

	"example.com/mediatimeline/internal/fixture"
	"example.com/mediatimeline/internal/pipeline"
)

// printReport renders the terminal demo output. Every value printed here is
// derived from the run report; nothing is rounded for presentation except the
// fixed three-decimal fractions the specification asks for.
func printReport(w *os.File, rep *pipeline.Report, runErr error, flags demoFlags, faults []fixture.Fault) {
	fmt.Fprintf(w, "mediatimeline demo seed=%d ladder=%d segment_ms=%d\n", flags.seed, flags.ladder, flags.segmentMS)
	if len(faults) == 0 {
		fmt.Fprintf(w, "faults=none\n")
	} else {
		names := make([]string, 0, len(faults))
		for _, f := range faults {
			names = append(names, string(f))
		}
		fmt.Fprintf(w, "faults=%s\n", joinComma(names))
	}

	fmt.Fprintf(w, "roundtrip_match=%.3f\n", rep.RoundTripMatch)
	fmt.Fprintf(w, "keyframe_hit_rate=%.3f\n", rep.KeyframeHitRate)
	fmt.Fprintf(w, "segment_hash_match=%.3f\n", rep.SegmentHashMatch)
	fmt.Fprintf(w, "recovered_layers=%d/%d\n", rep.RecoveredLayers, rep.TotalLayers)
	fmt.Fprintf(w, "boundary_errors=%d\n", rep.BoundaryErrors)

	fmt.Fprintf(w, "video_samples=%d\n", rep.VideoSamples)
	fmt.Fprintf(w, "audio_samples=%d\n", rep.AudioSamples)
	fmt.Fprintf(w, "events=%d\n", rep.EventCount)
	fmt.Fprintf(w, "segments=%d\n", rep.Segments)
	fmt.Fprintf(w, "keyframes=%d\n", rep.Keyframes)
	fmt.Fprintf(w, "input_bytes=%d event_index_bytes=%d\n", rep.InputBytes, rep.EventMemoryBytes)
	if len(rep.UnavailableLayers) > 0 {
		fmt.Fprintf(w, "unavailable_layers=%s\n", joinInts(rep.UnavailableLayers))
	} else {
		fmt.Fprintf(w, "unavailable_layers=none\n")
	}
	fmt.Fprintf(w, "color_primaries=%d color_transfer=%d color_matrix=%d color_conflicts=%d\n",
		rep.Color.Selected.Primaries, rep.Color.Selected.Transfer, rep.Color.Selected.Matrix, len(rep.Color.Conflicts))
	fmt.Fprintf(w, "audio_layout=%s audio_rate=%d\n", rep.AudioLayout.Name, rep.AudioRate.Selected)
	for _, c := range rep.Color.Conflicts {
		fmt.Fprintf(w, "  color conflict %s: kept %d (%s), saw %d (%s)\n", c.Field, c.Chosen, c.ChosenFrom, c.Other, c.OtherFrom)
	}
	for _, line := range rep.RepairLog {
		fmt.Fprintf(w, "  repair: %s\n", line)
	}
	if runErr != nil {
		fmt.Fprintf(w, "result=FAIL reason=%s\n", pipeline.SummarizeError(runErr))
		return
	}
	fmt.Fprintf(w, "result=PASS\n")
}

func printJSON(w *os.File, rep *pipeline.Report, runErr error) error {
	type envelope struct {
		Report *pipeline.Report `json:"report"`
		Exit   int              `json:"exit"`
		Error  string           `json:"error,omitempty"`
	}
	env := envelope{Report: rep, Exit: pipeline.ExitCode(runErr)}
	if runErr != nil {
		env.Error = pipeline.SummarizeError(runErr)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(env)
}

func joinComma(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out
}

func joinInts(ns []int) string {
	out := ""
	for i, n := range ns {
		if i > 0 {
			out += ","
		}
		out += fmt.Sprintf("%d", n)
	}
	return out
}
