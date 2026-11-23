package main

import "testing"

func TestRunDemoExitZeroOnAllPolicies(t *testing.T) {
	code, agg := RunDemo(DemoOptions{
		Seed: 23, InputSet: "generated", Remux: true, Verify: true,
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if agg.RoundtripMatchRate != 1 || agg.KeyframeIndexHitRate != 1 {
		t.Fatalf("rates must be perfect: rt=%f kf=%f",
			agg.RoundtripMatchRate, agg.KeyframeIndexHitRate)
	}
	if agg.TracksChecked < 2 {
		t.Fatalf("tracks_checked = %d, want >= 2", agg.TracksChecked)
	}
	if len(agg.SegmentHashes) == 0 {
		t.Fatal("segment hashes must be present")
	}
}

func TestRunDemoDeterministicAcrossInvocations(t *testing.T) {
	_, a := RunDemo(DemoOptions{Seed: 23, InputSet: "generated", Remux: true, Verify: true})
	_, b := RunDemo(DemoOptions{Seed: 23, InputSet: "generated", Remux: true, Verify: true})
	if len(a.SegmentHashes) != len(b.SegmentHashes) {
		t.Fatal("hash count differs across runs")
	}
	for i := range a.SegmentHashes {
		if a.SegmentHashes[i]["sha256"] != b.SegmentHashes[i]["sha256"] {
			t.Fatalf("hash %d differs: %s vs %s", i,
				a.SegmentHashes[i]["sha256"], b.SegmentHashes[i]["sha256"])
		}
	}
}

func TestRunDemoDifferentSeedsProduceDifferentPayloads(t *testing.T) {
	_, a := RunDemo(DemoOptions{Seed: 1, InputSet: "generated", Remux: true})
	_, b := RunDemo(DemoOptions{Seed: 2, InputSet: "generated", Remux: true})
	if a.SegmentHashes[0]["sha256"] == b.SegmentHashes[0]["sha256"] {
		t.Fatal("different seeds must yield different payload hashes")
	}
}

func TestSafeWorkDirRejectsTraversal(t *testing.T) {
	for _, p := range []string{"../escape", "/abs/path", "a/../../b"} {
		if _, err := safeWorkDir(p); err == nil {
			t.Fatalf("path %q must be rejected", p)
		}
	}
}

func TestRunDemoFlagParser(t *testing.T) {
	if code := runDemo([]string{"--input-set", "external"}); code != 1 {
		t.Fatalf("unsupported input-set exit = %d, want 1", code)
	}
	if code := runDemo([]string{"--bad-flag"}); code != 1 {
		t.Fatalf("bad flag exit = %d, want 1", code)
	}
}
