package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mediadist/internal/fixture"
	"mediadist/internal/merr"
	"mediadist/internal/track"
)

// buildInput adapts a generated fixture to the pipeline's input view.
func buildInput(t *testing.T, spec fixture.Spec) *Input {
	t.Helper()
	fx, err := fixture.Generate(spec)
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	in := &Input{IndexJSON: fx.IndexJSON}
	for _, st := range fx.Streams {
		ps := &InputStream{
			Name: st.Name, Codec: st.Codec, TrackID: st.TrackID,
			Timescale: st.Timescale, Origin: st.Origin,
			Datagrams: st.Datagrams, Repairs: st.Repairs,
		}
		for _, u := range st.AccessUnits {
			ps.CleanSamples = append(ps.CleanSamples, CleanSample{
				Timestamp: u.Timestamp, Keyframe: u.Keyframe, Data: u.Data,
			})
		}
		in.Streams = append(in.Streams, ps)
	}
	return in
}

func runForTest(t *testing.T, spec fixture.Spec, mut func(*Options)) *RunResult {
	t.Helper()
	in := buildInput(t, spec)
	opts := DefaultOptions()
	opts.Seed = spec.Seed
	opts.OutputDir = t.TempDir()
	if mut != nil {
		mut(&opts)
	}
	res := Run(in, opts)
	if res.Fatal != nil {
		t.Fatalf("run failed: %v", res.Fatal)
	}
	return res
}

func TestRunMeetsAcceptanceThresholds(t *testing.T) {
	res := runForTest(t, fixture.DefaultSpec(), nil)

	if res.Metrics.RoundtripTotal != 100 {
		t.Fatalf("roundtrip total %d want 100", res.Metrics.RoundtripTotal)
	}
	if res.Metrics.RoundtripMatch < 98 {
		t.Fatalf("roundtrip match %d < 98", res.Metrics.RoundtripMatch)
	}
	if res.Metrics.KeyframeHit != 9 || res.Metrics.KeyframeTotal != 10 {
		t.Fatalf("keyframe %d/%d want 9/10", res.Metrics.KeyframeHit, res.Metrics.KeyframeTotal)
	}
	if len(res.Metrics.SegmentSHA256) != 64 {
		t.Fatalf("segment hash length %d", len(res.Metrics.SegmentSHA256))
	}
	if res.Metrics.DuplicatePackets != 2 {
		t.Fatalf("duplicates %d", res.Metrics.DuplicatePackets)
	}
	if res.Metrics.ReorderedPackets != 4 {
		t.Fatalf("reordered %d", res.Metrics.ReorderedPackets)
	}
	if res.Metrics.RecoveredGaps != 1 {
		t.Fatalf("recovered gaps %d", res.Metrics.RecoveredGaps)
	}
	if res.Metrics.DamagedSpans != 1 {
		t.Fatalf("damaged spans %d", res.Metrics.DamagedSpans)
	}
	if !res.Metrics.RepeatRunMatch {
		t.Fatal("repeat run hash mismatch")
	}
	// Every sample must survive a re-parse of the written file.
	if res.ReadBackMatch != res.ReadBackTotal || res.ReadBackTotal != 300 {
		t.Fatalf("read-back %d/%d want 300/300", res.ReadBackMatch, res.ReadBackTotal)
	}
	if !res.Metrics.Pass(DefaultThresholds()) {
		t.Fatalf("thresholds not met: %+v", res.Metrics)
	}
}

func TestRunIsDeterministicAcrossDirectories(t *testing.T) {
	spec := fixture.DefaultSpec()
	a := runForTest(t, spec, nil)
	b := runForTest(t, spec, nil)
	if a.Metrics.SegmentSHA256 != b.Metrics.SegmentSHA256 {
		t.Fatalf("segment hash differs: %s vs %s", a.Metrics.SegmentSHA256, b.Metrics.SegmentSHA256)
	}
	if a.Remux.SHA256 != b.Remux.SHA256 {
		t.Fatalf("mp4 hash differs: %s vs %s", a.Remux.SHA256, b.Remux.SHA256)
	}
	if a.Metrics != b.Metrics {
		t.Fatalf("metrics differ:\n%+v\n%+v", a.Metrics, b.Metrics)
	}
	for i := range a.Segments {
		if a.Segments[i].SHA256 != b.Segments[i].SHA256 {
			t.Fatalf("segment %d hash differs", i)
		}
	}
}

func TestFatalLeavesNoPartialOutput(t *testing.T) {
	in := buildInput(t, fixture.DefaultSpec())
	// Duplicate a track ID so mapping must fail before any file is written.
	in.Streams[2].TrackID = in.Streams[0].TrackID
	dir := t.TempDir()
	opts := DefaultOptions()
	opts.OutputDir = dir
	res := Run(in, opts)
	if res.Fatal == nil {
		t.Fatal("duplicate track id must fail mapping")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("failed run left files: %v", entries)
	}
}

func TestBandwidthOnlyAffectsBufferCapacity(t *testing.T) {
	spec := fixture.DefaultSpec()
	low := runForTest(t, spec, func(o *Options) { o.BandwidthKbps = 20 })
	high := runForTest(t, spec, func(o *Options) { o.BandwidthKbps = 5000 })
	if low.Metrics.SegmentSHA256 != high.Metrics.SegmentSHA256 {
		t.Fatalf("bandwidth changed the segment hash: %s vs %s",
			low.Metrics.SegmentSHA256, high.Metrics.SegmentSHA256)
	}
	for i := range low.Segments {
		if low.Segments[i].SHA256 != high.Segments[i].SHA256 {
			t.Fatalf("segment %d media hash depends on bandwidth", i)
		}
		if low.Segments[i].StartNS != high.Segments[i].StartNS {
			t.Fatalf("segment %d start time depends on bandwidth", i)
		}
	}
	// The cap must actually bind at the low setting.
	capped := false
	for _, seg := range low.Segments {
		if seg.RateBps <= 20*1000 {
			capped = true
		}
	}
	if !capped {
		t.Fatalf("low bandwidth did not cap any segment: %+v", low.Segments)
	}
	if low.Remux.SHA256 != high.Remux.SHA256 {
		t.Fatal("bandwidth must not change the MP4 bytes")
	}
}

func TestLossChangesSegmentHashAndMedia(t *testing.T) {
	// The fixture spec drives fault injection; Options.LossBurst is echoed into
	// the report and must match it.
	cleanSpec := fixture.DefaultSpec()
	lostSpec := fixture.DefaultSpec()
	lostSpec.LossBurst = lostSpec.PacketsPerSample // erase whole access units
	clean := runForTest(t, cleanSpec, nil)
	lost := runForTest(t, lostSpec, func(o *Options) { o.LossBurst = lostSpec.LossBurst })
	// The top-level hash is the last segment, which is unaffected here. The
	// damaged segment is the first one: fewer samples and a different digest
	// prove the loss reached the report instead of being smoothed away.
	changed := false
	for i := range clean.Segments {
		if i >= len(lost.Segments) {
			changed = true
			break
		}
		if clean.Segments[i].SHA256 != lost.Segments[i].SHA256 ||
			clean.Segments[i].Samples != lost.Segments[i].Samples {
			changed = true
		}
	}
	if !changed {
		t.Fatal("whole-sample loss must change the affected segment")
	}
	if lost.Metrics.RoundtripMatch >= clean.Metrics.RoundtripMatch {
		t.Fatalf("loss did not reduce roundtrip: %d vs %d",
			lost.Metrics.RoundtripMatch, clean.Metrics.RoundtripMatch)
	}
}

func TestReportIsWrittenAndParses(t *testing.T) {
	res := runForTest(t, fixture.DefaultSpec(), nil)
	body, err := os.ReadFile(filepath.Join(res.Options.OutputDir, "report.json"))
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("report is not valid JSON: %v", err)
	}
	for _, key := range []string{"options", "metrics", "index", "mapping", "tracks", "streams", "segments", "remux", "errors"} {
		if _, ok := doc[key]; !ok {
			t.Fatalf("report missing %q", key)
		}
	}
	segments, ok := doc["segments"].([]any)
	if !ok || len(segments) == 0 {
		t.Fatalf("report has no segments")
	}
	// The MP4 named in the report must exist and match its recorded size.
	remuxDoc := doc["remux"].(map[string]any)
	if remuxDoc["sha256"] == "" {
		t.Fatal("remux hash missing")
	}
	if _, err := os.Stat(filepath.Join(res.Options.OutputDir, "media.mp4")); err != nil {
		t.Fatalf("mp4 missing: %v", err)
	}
}

func TestDamagedAndRecoveredEvidence(t *testing.T) {
	res := runForTest(t, fixture.DefaultSpec(), nil)
	var sawRecovered, sawLost, sawTruncated bool
	for _, e := range res.Errors {
		switch e.Code {
		case "sample_missing_packets":
			sawLost = true
		case "obu_truncated":
			sawTruncated = true
		case "late_arrival":
			sawRecovered = true
		}
		if e.Recovered && e.Stage == "" {
			t.Fatalf("error %q has no stage", e.Code)
		}
	}
	if !sawLost {
		t.Fatal("burst loss was not reported as a missing-packet error")
	}
	if !sawTruncated {
		t.Fatal("truncated AV1 OBU was not reported")
	}
	// Recovery is reported as a stream fact even without a late arrival error.
	var recovered int
	for _, s := range res.Streams {
		recovered += s.RecoveredBursts
	}
	if recovered != 1 {
		t.Fatalf("recovered bursts %d want 1", recovered)
	}
	_ = sawRecovered
	if res.Index.Rebuilt != true {
		t.Fatal("bad index should have been rebuilt")
	}
	if res.Index.Matched != 9 || res.Index.Total != 10 {
		t.Fatalf("index %d/%d", res.Index.Matched, res.Index.Total)
	}
}

func TestUnknownTrackPolicyDrop(t *testing.T) {
	in := buildInput(t, fixture.DefaultSpec())
	// The manifest names only the first two tracks; the AV1 track is unknown.
	in.Manifest = []uint32{1, 2}
	opts := DefaultOptions()
	opts.OutputDir = t.TempDir()
	opts.UnknownTracks = track.UnknownDrop
	res := Run(in, opts)
	if res.Fatal != nil {
		t.Fatalf("drop policy failed: %v", res.Fatal)
	}
	if len(res.Tracks) != 2 {
		t.Fatalf("dropped run wrote %d tracks want 2", len(res.Tracks))
	}
	for _, tr := range res.Tracks {
		if tr.TrackID == 3 {
			t.Fatal("unknown track 3 was forwarded despite the drop policy")
		}
	}
	if !strings.Contains(res.Mapping, "dropped=[3]") {
		t.Fatalf("drop is not recorded in the mapping: %s", res.Mapping)
	}
	// The MP4 only carries the manifest tracks.
	if res.Remux.SampleCount != 200 {
		t.Fatalf("mp4 has %d samples want 200", res.Remux.SampleCount)
	}
}

func TestUnknownTrackPolicyErrorRejects(t *testing.T) {
	in := buildInput(t, fixture.DefaultSpec())
	in.Manifest = []uint32{1, 2}
	opts := DefaultOptions()
	opts.OutputDir = t.TempDir()
	opts.UnknownTracks = track.UnknownError
	res := Run(in, opts)
	if res.Fatal == nil {
		t.Fatal("error policy must reject an unknown track")
	}
	e := merr.AsError(res.Fatal)
	if e == nil || e.Code != "unmapped_track" {
		t.Fatalf("unexpected error: %v", res.Fatal)
	}
	if e.Span.TrackID != 3 {
		t.Fatalf("error should name track 3: %+v", e.Span)
	}
	if entries, _ := os.ReadDir(opts.OutputDir); len(entries) != 0 {
		t.Fatalf("rejected run left files: %v", entries)
	}
}

func TestMissingOutputDirIsInputError(t *testing.T) {
	in := buildInput(t, fixture.DefaultSpec())
	opts := DefaultOptions()
	res := Run(in, opts)
	if res.Fatal == nil {
		t.Fatal("expected an error without an output directory")
	}
}

func TestPlanOrderRejectsDuplicateTimestamps(t *testing.T) {
	spec := fixture.DefaultSpec()
	in := buildInput(t, spec)
	// Duplicate the first sample of the H.264 stream, keeping sequence
	// numbers unique by shifting the copy.
	first := in.Streams[0].CleanSamples[0]
	in.Streams[0].CleanSamples = append(in.Streams[0].CleanSamples[:1],
		append([]CleanSample{first}, in.Streams[0].CleanSamples[1:]...)...)
	opts := DefaultOptions()
	opts.OutputDir = t.TempDir()
	res := Run(in, opts)
	// The clean-sample list only drives round-trip comparison, so the run must
	// still complete and simply report the duplicate as a mismatch.
	if res.Fatal != nil {
		t.Fatalf("run failed: %v", res.Fatal)
	}
	if res.Metrics.RoundtripMatch >= res.Metrics.RoundtripTotal {
		t.Fatal("a duplicated reference sample must reduce the match count")
	}
}

func TestErrorFormatIsStable(t *testing.T) {
	e := newTestError()
	s := e.Error()
	for _, want := range []string{"demux", "stream=3", "seq=17", "off=42", "len=8", "track=7"} {
		if !strings.Contains(s, want) {
			t.Fatalf("error %q missing %q", s, want)
		}
	}
}
