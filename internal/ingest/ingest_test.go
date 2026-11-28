package ingest

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"releasepipe/internal/errs"
	"releasepipe/internal/fixture"
)

func genGood(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := fixture.Generate(dir, false); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestReadMP4PreservesTiming(t *testing.T) {
	dir := genGood(t)
	tracks, meta, err := ReadMP4(filepath.Join(dir, fixture.MainMP4))
	if err != nil {
		t.Fatal(err)
	}
	if meta["major_brand"] != "isom" || meta["track_count"] != "2" {
		t.Fatalf("bad meta: %v", meta)
	}
	if len(tracks) != 2 {
		t.Fatalf("got %d tracks", len(tracks))
	}
	v := tracks[0]
	if v.Kind != "video" || v.Timebase.Den != 90000 {
		t.Fatalf("video track: kind=%s tb=%v", v.Kind, v.Timebase)
	}
	if len(v.Samples) != 50 {
		t.Fatalf("video samples = %d", len(v.Samples))
	}
	// VFR: per-sample durations must match the source pattern exactly.
	for i, want := range fixture.VideoDeltas() {
		if v.Samples[i].Duration != int64(want) {
			t.Fatalf("sample %d duration %d, want %d (VFR not preserved)",
				i, v.Samples[i].Duration, want)
		}
		if v.Samples[i].PTS != v.Samples[i].DTS {
			t.Fatalf("sample %d PTS != DTS", i)
		}
	}
	// Source positions must be monotonic and inside the file.
	for i := 1; i < len(v.Samples); i++ {
		if v.Samples[i].SourceOffset <= v.Samples[i-1].SourceOffset {
			t.Fatalf("sample %d offset not monotonic", i)
		}
	}
	if tracks[1].Kind != "audio" || tracks[1].Timebase.Den != 48000 {
		t.Fatalf("audio track: %+v", tracks[1].Timebase)
	}
}

func TestReadMP4TruncatedSample(t *testing.T) {
	dir := t.TempDir()
	if err := fixture.Generate(dir, true); err != nil {
		t.Fatal(err)
	}
	_, _, err := ReadMP4(filepath.Join(dir, fixture.MainMP4))
	var pe *errs.Error
	if !errors.As(err, &pe) {
		t.Fatalf("want *errs.Error, got %v", err)
	}
	if pe.Code != errs.CodeTruncatedSample {
		t.Fatalf("code = %s, want %s", pe.Code, errs.CodeTruncatedSample)
	}
	if pe.Stage != errs.StageIngest || pe.Offset <= 0 {
		t.Fatalf("stage=%s offset=%d", pe.Stage, pe.Offset)
	}
}

func TestRTPReorderRecovery(t *testing.T) {
	dir := genGood(t)
	ps, err := ReadRTPFile(filepath.Join(dir, fixture.VideoRTP), "video", 90000)
	if err != nil {
		t.Fatal(err)
	}
	st := ps.Stats
	if st.Recovered != 2 || st.Lost != 0 || st.Duplicated != 1 {
		t.Fatalf("stats = %+v, want 2 recovered (late+swap), 0 lost, 1 dup", st)
	}
	if got := st.RecoveredPct(); got != 100 {
		t.Fatalf("recovered_pct = %v, want 100", got)
	}
	if len(ps.Samples) != 50 {
		t.Fatalf("samples = %d", len(ps.Samples))
	}
	// Expanded timestamps must be monotonic past the wrap point.
	for i := 1; i < len(ps.Samples); i++ {
		if ps.Samples[i].PTS <= ps.Samples[i-1].PTS {
			t.Fatalf("PTS not monotonic at %d", i)
		}
	}
	// First timestamp sits before the 32-bit wrap: expansion crosses 2^32.
	if ps.Samples[0].PTS != 0xFFFFFFF0 {
		t.Fatalf("first PTS = %#x", ps.Samples[0].PTS)
	}
	if ps.Samples[len(ps.Samples)-1].PTS < 1<<32 {
		t.Fatal("timestamp expansion never crossed the wrap")
	}
}

func TestRTPUnrecoverableLoss(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "loss.rtp")
	if err := fixture.WriteRTP(p, 100, 5000, 1,
		fixture.AudioDeltas(), fixture.RTPEdit{Drop: []int{10}}); err != nil {
		t.Fatal(err)
	}
	ps, err := ReadRTPFile(p, "audio", 48000)
	if err != nil {
		t.Fatal(err)
	}
	if ps.Stats.Lost != 1 || ps.Stats.RecoveredPct() != 0 {
		t.Fatalf("stats = %+v", ps.Stats)
	}
	if len(ps.Samples) != 49 {
		t.Fatalf("samples = %d, want 49", len(ps.Samples))
	}
}

func TestRTPAmbiguousOrderFails(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "amb.rtp")
	// A jump of exactly half the sequence modulus is undecidable.
	if err := fixture.WriteRTP(p, 0, 0, 1, []uint32{1, 1}, fixture.RTPEdit{}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	// Rewrite the second packet's sequence number to 0x8000: record 0 is
	// 2+44 bytes, so record 1's RTP header starts at offset 48 and its
	// sequence number (header bytes 2-3) at offset 50.
	raw[50] = 0x80
	raw[51] = 0x00
	if err := os.WriteFile(p, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = ReadRTPFile(p, "video", 90000)
	var pe *errs.Error
	if !errors.As(err, &pe) || pe.Code != errs.CodeAmbiguousOrder {
		t.Fatalf("want AMBIGUOUS_ORDER, got %v", err)
	}
}

func TestRemuxChangesContainerMetaOnly(t *testing.T) {
	dir := t.TempDir()
	specs := []fixture.TrackSpec{
		{Handler: "vide", Timescale: 90000, Deltas: fixture.VideoDeltas()},
	}
	p1 := filepath.Join(dir, "a.mp4")
	p2 := filepath.Join(dir, "b.mp4")
	if err := fixture.WriteMP4(p1, specs); err != nil {
		t.Fatal(err)
	}
	if err := fixture.WriteMP4WithBrand(p2, [4]byte{'m', 'p', '4', '2'}, specs); err != nil {
		t.Fatal(err)
	}
	tr1, meta1, err := ReadMP4(p1)
	if err != nil {
		t.Fatal(err)
	}
	tr2, meta2, err := ReadMP4(p2)
	if err != nil {
		t.Fatal(err)
	}
	// Same media: identical sample timing; different wrapper: meta differs.
	if meta1["major_brand"] == meta2["major_brand"] {
		t.Fatal("remux did not change container metadata")
	}
	if len(tr1[0].Samples) != len(tr2[0].Samples) ||
		tr1[0].Samples[3].PTS != tr2[0].Samples[3].PTS {
		t.Fatal("remux altered sample timing")
	}
}

func TestTimelineSyncAndConflict(t *testing.T) {
	dir := genGood(t)
	tracks, _, err := ReadMP4(filepath.Join(dir, fixture.MainMP4))
	if err != nil {
		t.Fatal(err)
	}
	vp, err := ReadRTPFile(filepath.Join(dir, fixture.VideoRTP), "video", 90000)
	if err != nil {
		t.Fatal(err)
	}
	ap, err := ReadRTPFile(filepath.Join(dir, fixture.AudioRTP), "audio", 48000)
	if err != nil {
		t.Fatal(err)
	}
	tl, err := BuildTimeline(tracks, []*PacketStream{vp, ap})
	if err != nil {
		t.Fatal(err)
	}
	sync, err := tl.SyncErrorMS()
	if err != nil {
		t.Fatal(err)
	}
	if sync > 20 {
		t.Fatalf("sync_ms = %v, want <= 20", sync)
	}
	if _, ok := tl.Corrections["video"]; !ok {
		t.Fatal("no drift correction recorded for video")
	}

	// A packet stream diverging from the container must fail with
	// TIME_CONFLICT at the diverging sample.
	bad := &PacketStream{Kind: "video", ClockRate: 90000}
	for i := range tracks[0].Samples {
		bad.Samples = append(bad.Samples, Sample{PTS: tracks[0].Samples[i].PTS})
	}
	bad.Samples[10].PTS += 90000 * 5 // +5s at sample 10
	if _, err := BuildTimeline(tracks, []*PacketStream{bad}); err == nil {
		t.Fatal("conflict not detected")
	} else {
		var pe *errs.Error
		if !errors.As(err, &pe) || pe.Code != errs.CodeTimeConflict {
			t.Fatalf("want TIME_CONFLICT, got %v", err)
		}
		if pe.Stage != errs.StageTimeline || pe.Offset < 0 {
			t.Fatalf("stage=%s offset=%d", pe.Stage, pe.Offset)
		}
	}
}
