package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func runVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	input := fs.String("input", "", "directory produced by 'demo'")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *input == "" {
		return fmt.Errorf("verify requires --input")
	}

	summaryPath := filepath.Join(*input, "summary.json")
	segmentsPath := filepath.Join(*input, "segments.json")

	var summary Summary
	if err := readJSON(summaryPath, &summary); err != nil {
		return err
	}
	var stored []HashEntry
	if err := readJSON(segmentsPath, &stored); err != nil {
		return err
	}

	// Re-derive hashes by re-running the same deterministic generation and
	// repair, then compare every segment digest to the stored one.
	reps, err := rebuildReps(&summary)
	if err != nil {
		return err
	}

	fmt.Printf("verifying %d segments in %s\n", len(stored), *input)
	mismatches := 0
	for _, want := range stored {
		got := findHash(reps, want.Representation, want.SegmentID)
		if got == "" {
			fmt.Printf("MISS rep=%d segment=%d (no recovered segment)\n",
				want.Representation, want.SegmentID)
			mismatches++
			continue
		}
		if got != want.SHA256 {
			fmt.Printf("FAIL rep=%d segment=%d\n  stored=%s\n  recomputed=%s\n",
				want.Representation, want.SegmentID, want.SHA256, got)
			mismatches++
			continue
		}
		fmt.Printf("OK   rep=%d segment=%d sha256=%s\n",
			want.Representation, want.SegmentID, want.SHA256)
	}

	if mismatches > 0 {
		return fmt.Errorf("%d segment hash(es) did not match", mismatches)
	}
	fmt.Printf("all %d segment SHA-256 hashes match\n", len(stored))
	if summary.ParseRoundTripPct != 100 {
		return fmt.Errorf("stored run had a round-trip failure")
	}
	return nil
}

// rebuildReps regenerates and repairs the stream using the parameters recorded
// in the summary, returning the pipeline result for hash comparison.
func rebuildReps(summary *Summary) ([]rebuiltSegment, error) {
	// Imported lazily to keep this file focused on I/O.
	return rebuild(summary)
}

func readJSON(path string, v interface{}) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
