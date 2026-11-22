package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"mediapipeline/internal/checkpoint"
	"mediapipeline/internal/container"
	"mediapipeline/internal/media"
	"mediapipeline/internal/timeline"
)

func demoOptions(t *testing.T) Options {
	t.Helper()
	opts := Options{
		Seed:     7,
		Duration: media.Nano(12_000_000_000),
		Segment:  media.Nano(2_000_000_000),
		Bitrates: []int{400, 900, 1600},
		Loss:     0.03,
		Jitter:   media.Nano(80_000_000),
		Late:     media.Nano(250_000_000),
		WorkDir:  t.TempDir(),
	}
	opts.Defaults()
	return opts
}

func TestBuildInputIsDeterministic(t *testing.T) {
	opts := demoOptions(t)
	a, err := BuildInput(opts)
	if err != nil {
		t.Fatalf("BuildInput: %v", err)
	}
	b, err := BuildInput(opts)
	if err != nil {
		t.Fatal(err)
	}
	if a.ManifestHA != b.ManifestHA {
		t.Fatalf("manifest hash differs: %s vs %s", a.ManifestHA, b.ManifestHA)
	}
	if string(a.ManifestJS) != string(b.ManifestJS) {
		t.Fatal("manifest JSON differs between identical builds")
	}
	if a.SourceSHA256 != b.SourceSHA256 {
		t.Fatal("input digest differs between identical builds")
	}
}

func TestBuildInputAlignsRepresentations(t *testing.T) {
	in, err := BuildInput(demoOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	want := in.Manifest.SegmentCount
	if want == 0 {
		t.Fatal("no segments produced")
	}
	if in.Manifest.AlignedBoundaries != want {
		t.Fatalf("aligned boundaries = %d, segments = %d", in.Manifest.AlignedBoundaries, want)
	}
	for _, rep := range in.Manifest.Representations {
		if len(rep.Segments) != want {
			t.Errorf("%s has %d segments, want %d", rep.Name, len(rep.Segments), want)
		}
	}
	if in.AVSyncMax > 40*media.Nano(1_000_000) {
		t.Errorf("av sync max = %v ms", media.Millis(in.AVSyncMax))
	}
}

func TestCheckInputFindsNoViolationsForValidInput(t *testing.T) {
	in, err := BuildInput(demoOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	if vs := CheckInput(in); len(vs) != 0 {
		t.Fatalf("unexpected violations: %v", vs)
	}
}

func TestCheckInputDetectsNonMonotonicTrack(t *testing.T) {
	in, err := BuildInput(demoOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	tr := in.Reps[0].Track
	// Corrupt a copy so the shared fixture is untouched.
	corrupt := *tr
	corrupt.Samples = append([]container.Sample(nil), tr.Samples...)
	corrupt.Samples[5].DTSNanos = corrupt.Samples[0].DTSNanos - 1
	in.Reps[0].Track = &corrupt
	vs := CheckInput(in)
	if len(vs) == 0 {
		t.Fatal("expected a monotonicity violation")
	}
	found := false
	for _, v := range vs {
		if v.Category == media.OutOfOrderTimestamp {
			found = true
		}
	}
	if !found {
		t.Fatalf("violations = %v", vs)
	}
}

func TestCheckTimelineDetectsUnsortedEvents(t *testing.T) {
	tl, err := timeline.New(timeline.Config{
		Segment:             media.Nano(1_000_000_000),
		AllowedLateness:     0,
		MaxReplayWindow:     media.Nano(10_000_000_000),
		MaxEventsPerSegment: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := tl.Push(timeline.Event{Time: media.Nano(i) * media.Nano(100_000_000), Track: 1, Seq: uint64(i)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := tl.Flush(); err != nil {
		t.Fatal(err)
	}
	if vs := CheckTimeline(tl, nil); len(vs) != 0 {
		t.Fatalf("clean timeline has violations: %v", vs)
	}
	// Corrupt a sealed segment's order; the checker must notice.
	tl.Segments()[0].Events[0], tl.Segments()[0].Events[1] =
		tl.Segments()[0].Events[1], tl.Segments()[0].Events[0]
	vs := CheckTimeline(tl, nil)
	if len(vs) == 0 {
		t.Fatal("expected ordering violation")
	}
}

func TestRunResumesFromCorruptCheckpoint(t *testing.T) {
	// A truncated checkpoint is a recoverable condition: the run must rebuild
	// from zero rather than trust partial state, and still succeed.
	opts := demoOptions(t)
	if err := os.MkdirAll(filepath.Join(opts.WorkDir, "checkpoint"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(opts.WorkDir, "checkpoint", "state.json")
	if err := os.WriteFile(path, []byte(`{"schema":1,"input_sha`), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Run(opts)
	if err != nil {
		t.Fatalf("Run with corrupt checkpoint: %v", err)
	}
	if len(res.ManifestSHA256) != 64 {
		t.Fatalf("manifest hash = %q", res.ManifestSHA256)
	}
	// The checkpoint must be valid again afterwards.
	st, err := checkpoint.Open(filepath.Join(opts.WorkDir, "checkpoint"))
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := st.Load()
	if err != nil {
		t.Fatalf("checkpoint not repaired: %v", err)
	}
	if loaded == nil || loaded.ManifestSHA256 != res.ManifestSHA256 {
		t.Fatalf("checkpoint = %+v", loaded)
	}
	if err := st.VerifyOrdering(loaded); err != nil {
		t.Fatalf("repaired checkpoint is ahead of outputs: %v", err)
	}
}

func TestRunDetectsConflictingDurableOutput(t *testing.T) {
	// A durable segment whose content disagrees with what this run would
	// produce is an idempotency conflict, never a silent overwrite.
	opts := demoOptions(t)
	opts.InterruptAfter = 1
	if _, err := Run(opts); err != nil {
		t.Fatalf("first run: %v", err)
	}
	st, err := checkpoint.Open(filepath.Join(opts.WorkDir, "checkpoint"))
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := st.Load()
	if err != nil || loaded == nil {
		t.Fatalf("load: %v", err)
	}
	// Forge a prior output claiming different content for a key this run uses.
	first := loaded.Outputs[0]
	loaded.Outputs[0].ContentSHA256 = "forged"
	if err := st.Save(loaded); err != nil {
		t.Fatal(err)
	}
	_, err = Run(opts)
	if err == nil {
		t.Fatalf("forged output %q was accepted", first.Key)
	}
	if media.CategoryOf(err) != media.IdempotencyConflict {
		t.Fatalf("category = %v (%v)", media.CategoryOf(err), err)
	}
}

func TestRunProducesInvariantHoldingResult(t *testing.T) {
	opts := demoOptions(t)
	opts.InterruptAfter = 3
	res, err := Run(opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.AVSyncMaxMS > 40 {
		t.Errorf("av_sync_max_ms = %v", res.AVSyncMaxMS)
	}
	if res.PacketRecoveryRate < 0.85 {
		t.Errorf("recovery = %v", res.PacketRecoveryRate)
	}
	if res.P95LatencyMS > 250 {
		t.Errorf("p95 = %v", res.P95LatencyMS)
	}
	if !res.ResumeOutputsEqual {
		t.Error("resume outputs differ")
	}
	if !res.Interrupted {
		t.Error("expected the run to be interrupted")
	}
	if res.ReusedOutputs == 0 {
		t.Error("resume reused no outputs")
	}
	if len(res.ManifestSHA256) != 64 {
		t.Errorf("manifest hash = %q", res.ManifestSHA256)
	}
	if res.ProvenanceRecords == 0 {
		t.Error("no provenance records")
	}
}

func TestRunRepeatedProducesSameHashes(t *testing.T) {
	opts := demoOptions(t)
	first, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if first.ManifestSHA256 != second.ManifestSHA256 {
		t.Fatalf("manifest hash changed on rerun: %s vs %s",
			first.ManifestSHA256, second.ManifestSHA256)
	}
	if len(first.ProvenanceHashes) != len(second.ProvenanceHashes) {
		t.Fatal("provenance record count changed")
	}
	for i := range first.ProvenanceHashes {
		if first.ProvenanceHashes[i] != second.ProvenanceHashes[i] {
			t.Fatalf("provenance hash %d changed: %s vs %s",
				i, first.ProvenanceHashes[i], second.ProvenanceHashes[i])
		}
	}
	if second.WrittenOutputs != 0 {
		t.Fatalf("rerun wrote %d outputs", second.WrittenOutputs)
	}
}

func TestRunRejectsInvalidOptions(t *testing.T) {
	cases := []Options{
		{Seed: 1, Duration: 0, Segment: media.Nano(2e9), Bitrates: []int{400}, WorkDir: t.TempDir()},
		{Seed: 1, Duration: media.Nano(2e9), Segment: 0, Bitrates: []int{400}, WorkDir: t.TempDir()},
		{Seed: 1, Duration: media.Nano(2e9), Segment: media.Nano(1e9), WorkDir: t.TempDir()},
		{Seed: 1, Duration: media.Nano(2e9), Segment: media.Nano(1e9), Bitrates: []int{400}, Loss: 1.5, WorkDir: t.TempDir()},
	}
	for i, opts := range cases {
		if _, err := Run(opts); err == nil {
			t.Errorf("case %d: expected error", i)
		} else if media.CategoryOf(err) != media.InvalidInput {
			t.Errorf("case %d: category = %v", i, media.CategoryOf(err))
		}
	}
}

func TestVerifyAllFaultsDetected(t *testing.T) {
	report, err := Verify(7, AllFaults())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got := report.ExitCode(); got != 0 {
		t.Fatalf("exit code = %d, lines: %v", got, report.Lines())
	}
	if len(report.Results) != len(AllFaults()) {
		t.Fatalf("results = %d", len(report.Results))
	}
	// Every case must report at least one detection with a category.
	for _, res := range report.Results {
		if res.Detected == 0 || res.Category == "" {
			t.Errorf("%s: detected=%d category=%q", res.Kind, res.Detected, res.Category)
		}
		if len(res.CountsDetail) == 0 {
			t.Errorf("%s: no per-category counts", res.Kind)
		}
	}
	// Late events must name exactly one replayed interval.
	for _, res := range report.Results {
		if res.Kind == FaultLateEvent && len(res.ReplaySpans) != 1 {
			t.Errorf("late-event spans = %v", res.ReplaySpans)
		}
	}
}

func TestVerifyIsDeterministic(t *testing.T) {
	a, err := Verify(11, AllFaults())
	if err != nil {
		t.Fatal(err)
	}
	b, err := Verify(11, AllFaults())
	if err != nil {
		t.Fatal(err)
	}
	la, lb := a.Lines(), b.Lines()
	if len(la) != len(lb) {
		t.Fatalf("line count changed")
	}
	for i := range la {
		if la[i] != lb[i] {
			t.Fatalf("line %d differs:\n%s\n%s", i, la[i], lb[i])
		}
	}
}

func TestParseFaultsRejectsUnknown(t *testing.T) {
	if _, err := ParseFaults("truncated,bogus"); err == nil {
		t.Fatal("unknown fault accepted")
	}
	if _, err := ParseFaults(""); err == nil {
		t.Fatal("empty fault list accepted")
	}
	kinds, err := ParseFaults("burst-loss, truncated")
	if err != nil {
		t.Fatal(err)
	}
	if len(kinds) != 2 || kinds[0] != "burst-loss" || kinds[1] != "truncated" {
		t.Fatalf("parsed = %v", kinds)
	}
}
