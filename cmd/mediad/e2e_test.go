package main

import (
	"context"
	"testing"

	"mediad/internal/model"
)

// TestEndToEndHappyPath runs the full deterministic pipeline and checks every
// numeric acceptance contract.
func TestEndToEndHappyPath(t *testing.T) {
	out, err := buildAndRun(context.Background(), demoFlags{
		seed: 42, lateWindowMS: 120, bufferBytes: 65536, consumerDelayMS: 8,
	}, newRNG(42))
	if err != nil {
		t.Fatalf("buildAndRun: %v", err)
	}
	if out.SyncErrorMS > 20 {
		t.Errorf("sync_error_ms=%v > 20", out.SyncErrorMS)
	}
	if out.PacketRecoveryRate < 0.75 || out.PacketRecoveryRate > 1.0 {
		t.Errorf("recovery rate=%v outside [0.75,1.0]", out.PacketRecoveryRate)
	}
	if out.P95LatencyMS <= 0 {
		t.Errorf("p95 must be positive, got %v", out.P95LatencyMS)
	}
	if out.BufferPeakBytes > 65536 {
		t.Errorf("peak bytes=%d exceeds cap", out.BufferPeakBytes)
	}
	if out.LateRecomputed < 1 || out.LateRejected < 1 {
		t.Errorf("late counters wrong: recomputed=%d rejected=%d", out.LateRecomputed, out.LateRejected)
	}
	if out.MetadataConflicts < 1 || out.TimelineHash == 0 {
		t.Errorf("conflicts=%d hash=%d", out.MetadataConflicts, out.TimelineHash)
	}
	required := map[string]bool{
		"INVALID_CROP": false, "META_UNPARSEABLE": false,
		"RTP_TRUNCATED": false, "LATE_REJECTED": false,
	}
	for _, c := range out.ErrorCodes {
		if _, ok := required[c]; ok {
			required[c] = true
		}
	}
	for code, seen := range required {
		if !seen {
			t.Errorf("expected error code %q in %v", code, out.ErrorCodes)
		}
	}
}

// TestEndToEndDeterministic runs the pipeline twice and requires identical
// summaries, including the timeline hash and audio checksum.
func TestEndToEndDeterministic(t *testing.T) {
	flags := demoFlags{seed: 42, lateWindowMS: 120, bufferBytes: 65536, consumerDelayMS: 8}
	a, err := buildAndRun(context.Background(), flags, newRNG(42))
	if err != nil {
		t.Fatal(err)
	}
	b, err := buildAndRun(context.Background(), flags, newRNG(42))
	if err != nil {
		t.Fatal(err)
	}
	if a.TimelineHash != b.TimelineHash || a.AudioChecksum != b.AudioChecksum {
		t.Fatal("core hashes diverged between identical runs")
	}
	if a.BufferStats.BlockedNS != b.BufferStats.BlockedNS {
		t.Fatal("blocked duration must be deterministic")
	}
	if a.RTPStats != b.RTPStats {
		t.Fatal("RTP stats diverged")
	}
	if a.GeneratedAt != b.GeneratedAt {
		t.Fatal("generated stamp diverged")
	}
}

// TestEndToEndCorruptInputsContinue ensures broken tracks do not abort the
// independent healthy ones, and every failure carries a structured code.
func TestEndToEndCorruptInputsContinue(t *testing.T) {
	res, _ := buildMetadata(newRNG(42))
	if len(res.Tracks) < 3 {
		t.Fatalf("healthy tracks lost after corrupt inputs: %d", len(res.Tracks))
	}
	for _, e := range res.Errors {
		if e.Code == "" {
			t.Fatal("parse error missing structured code")
		}
		if !model.HasCode(e, e.Code) {
			t.Fatalf("error not recognized as MediaError: %v", e)
		}
	}
}

// TestEndToEndDifferentSeedsVary but stay within all acceptance bounds.
func TestEndToEndDifferentSeedsVary(t *testing.T) {
	hashes := map[uint64]int{}
	for _, seed := range []int64{1, 2, 99} {
		out, err := buildAndRun(context.Background(),
			demoFlags{seed: seed, lateWindowMS: 120, bufferBytes: 65536, consumerDelayMS: 4},
			newRNG(seed))
		if err != nil {
			t.Fatal(err)
		}
		if out.SyncErrorMS > 20 || out.PacketRecoveryRate < 0.75 {
			t.Fatalf("seed %d out of bounds: sync=%v recovery=%v", seed,
				out.SyncErrorMS, out.PacketRecoveryRate)
		}
		hashes[out.TimelineHash]++
	}
	if len(hashes) < 2 {
		t.Fatal("different seeds should produce distinct timeline hashes")
	}
}
