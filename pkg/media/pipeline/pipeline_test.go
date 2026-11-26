package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"cmafrepair/pkg/media/clock"
	"cmafrepair/pkg/media/cmaf"
)

// testBox builds a fragment file in dir and returns its path.
func writeRepFiles(t *testing.T, dir, rep string, seqs []uint32, skewMs int64) (string, []string) {
	t.Helper()
	repDir := filepath.Join(dir, rep)
	if err := os.MkdirAll(repDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// init: timescale 90000, trex default duration 9000 (100 ms).
	init := buildInitFile(t, repDir)
	paths := make([]string, 0, len(seqs))
	tfdt := uint64(0)
	for _, seq := range seqs {
		// One sample of 9000 ticks = 100 ms per fragment.
		data := buildOneFragment(uint64(seq), tfdt, 9000, 100)
		p := filepath.Join(repDir, fragName(seq))
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
		tfdt += 9000
	}
	return init, paths
}

func fragName(seq uint32) string {
	return "frag_" + pad4(seq) + ".m4s"
}

func pad4(v uint32) string {
	s := []byte("0000")
	d := []byte{
		'0' + byte(v/1000),
		'0' + byte((v/100)%10),
		'0' + byte((v/10)%10),
		'0' + byte(v%10),
	}
	copy(s, d)
	return string(s)
}

func buildInitFile(t *testing.T, dir string) string {
	mdhd := cmaf.NewBox("mdhd").FullHeader(0, 0).
		U32(0).U32(0).U32(90000).U32(0).Bytes([]byte{0, 0, 0, 0, 0, 0})
	tkhd := cmaf.NewBox("tkhd").FullHeader(0, 0).U32(0).U32(0).U32(1).Bytes(make([]byte, 4))
	mdia := cmaf.NewBox("mdia").Child(mdhd)
	trak := cmaf.NewBox("trak").Child(tkhd).Child(mdia)
	trex := cmaf.NewBox("trex").FullHeader(0, 0).U32(1).U32(1).U32(9000).U32(0).U32(0)
	mvex := cmaf.NewBox("mvex").Child(trex)
	moov := cmaf.NewBox(cmaf.BoxMoov).Child(trak).Child(mvex)
	ftyp := cmaf.NewBox(cmaf.BoxFtyp).Bytes([]byte("iso5dash"))
	p := filepath.Join(dir, "init.mp4")
	if err := os.WriteFile(p, append(ftyp.Build(), moov.Build()...), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// buildOneFragment makes a single-sample fragment with explicit duration.
func buildOneFragment(seq, tfdt uint64, duration, size uint32) []byte {
	trun := cmaf.NewBox(cmaf.BoxTrun).FullHeader(1, 0x000300).U32(1).U32(duration).U32(size)
	tfhd := cmaf.NewBox(cmaf.BoxTfhd).FullHeader(0, 0).U32(1)
	tfdtBox := cmaf.NewBox(cmaf.BoxTfdt).FullHeader(1, 0).U64(tfdt)
	mfhd := cmaf.NewBox(cmaf.BoxMfhd).FullHeader(0, 0).U32(uint32(seq))
	traf := cmaf.NewBox(cmaf.BoxTraf).Child(tfhd).Child(tfdtBox).Child(trun)
	moof := cmaf.NewBox(cmaf.BoxMoof).Child(mfhd).Child(traf)
	mdat := cmaf.NewBox(cmaf.BoxMdat).Bytes(make([]byte, size))
	return append(moof.Build(), mdat.Build()...)
}

func anchorAt(hz uint32, offsetMs int64) clock.ClockAnchor {
	// Anchor shared 0 at RTP 0; a skew is expressed via AnchorSharedMs.
	return clock.ClockAnchor{ClockHz: hz, AnchorRTP: 0, AnchorSharedMs: offsetMs}
}

func TestFanOutRepairsAllRepresentations(t *testing.T) {
	dir := t.TempDir()
	inputs := make([]RepInput, 0, 3)
	for _, rep := range []string{"v0", "v1", "v2"} {
		init, frags := writeRepFiles(t, dir, rep, []uint32{1, 2, 3}, 0)
		inputs = append(inputs, RepInput{
			Name: rep, InitPath: init, FragmentPaths: frags,
			ClockHz: 90000, Anchor: ptr(anchorAt(90000, 0)),
		})
	}
	results := FanOut(context.Background(), inputs, DefaultOptions())
	if len(results) != 3 {
		t.Fatalf("got %d results want 3", len(results))
	}
	for _, r := range results {
		if r.Fatal != nil {
			t.Fatalf("%s fatal: %v", r.Name, r.Fatal)
		}
		if r.Timeline == nil || len(r.Timeline.Entries) != 3 {
			t.Fatalf("%s timeline wrong: %+v", r.Name, r.Timeline)
		}
	}
}

func TestFanOutIsolatesBadRepresentation(t *testing.T) {
	dir := t.TempDir()
	goodInit, goodFrags := writeRepFiles(t, dir, "good", []uint32{1, 2}, 0)
	badDir := filepath.Join(dir, "bad")
	os.MkdirAll(badDir, 0o755)
	badInit := filepath.Join(badDir, "init.mp4")
	// Truncated/garbage init makes this representation unparseable.
	if err := os.WriteFile(badInit, []byte{0, 0, 0, 8, 'm', 'o', 'o', 'v', 1, 2}, 0o600); err != nil {
		t.Fatal(err)
	}
	inputs := []RepInput{
		{Name: "good", InitPath: goodInit, FragmentPaths: goodFrags, ClockHz: 90000},
		{Name: "bad", InitPath: badInit, FragmentPaths: nil, ClockHz: 90000},
	}
	results := FanOut(context.Background(), inputs, DefaultOptions())
	if results[0].Fatal != nil {
		t.Fatalf("good rep must survive: %v", results[0].Fatal)
	}
	if results[1].Fatal == nil || !results[1].Isolated {
		t.Fatalf("bad rep must be isolated: %+v", results[1])
	}
}

func TestJoinDetectsBoundaryMismatch(t *testing.T) {
	dir := t.TempDir()
	// Two aligned reps and one skewed by 5 ms.
	_, f0 := writeRepFiles(t, dir, "v0", []uint32{1, 2}, 0)
	_, f1 := writeRepFiles(t, dir, "v1", []uint32{1, 2}, 0)
	inputs := []RepInput{
		{Name: "v0", InitPath: mustInit(t, dir, "v0"), FragmentPaths: f0, ClockHz: 90000, Anchor: ptr(anchorAt(90000, 0))},
		{Name: "v1", InitPath: mustInit(t, dir, "v1"), FragmentPaths: f1, ClockHz: 90000, Anchor: ptr(anchorAt(90000, 5))},
	}
	results := FanOut(context.Background(), inputs, DefaultOptions())
	report, err := Join(results)
	if err != nil {
		t.Fatal(err)
	}
	if report.MismatchCount() == 0 {
		t.Fatal("skewed representation must produce a boundary mismatch")
	}
	found := false
	for _, m := range report.Mismatches {
		if m.Representation == "v1" && m.DeltaMs == 5 {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected v1 5ms mismatch, got %+v", report.Mismatches)
	}
	if len(report.Isolated) != 1 || report.Isolated[0] != "v1" {
		t.Fatalf("isolation wrong: %+v", report.Isolated)
	}
}

func TestJoinAlignedRepsHaveZeroMismatch(t *testing.T) {
	dir := t.TempDir()
	_, f0 := writeRepFiles(t, dir, "v0", []uint32{1, 2, 3}, 0)
	_, f1 := writeRepFiles(t, dir, "v1", []uint32{1, 2, 3}, 0)
	inputs := []RepInput{
		{Name: "v0", InitPath: mustInit(t, dir, "v0"), FragmentPaths: f0, ClockHz: 90000, Anchor: ptr(anchorAt(90000, 0))},
		{Name: "v1", InitPath: mustInit(t, dir, "v1"), FragmentPaths: f1, ClockHz: 90000, Anchor: ptr(anchorAt(90000, 0))},
	}
	results := FanOut(context.Background(), inputs, DefaultOptions())
	report, err := Join(results)
	if err != nil {
		t.Fatal(err)
	}
	if report.MismatchCount() != 0 {
		t.Fatalf("aligned reps must have 0 mismatches, got %+v", report.Mismatches)
	}
	if report.TotalBoundaries != 3 {
		t.Fatalf("boundaries=%d want 3", report.TotalBoundaries)
	}
}

func TestFanOutCountsRtpWraparound(t *testing.T) {
	dir := t.TempDir()
	init, frags := writeRepFiles(t, dir, "v0", []uint32{1}, 0)
	// Timestamps that cross the 2^32 boundary exactly once.
	ts := []uint32{0xFFFFFFF0, 0x00000010, 0x00000030}
	in := RepInput{
		Name: "v0", InitPath: init, FragmentPaths: frags,
		ClockHz: 90000, RTPTimestamps: ts, Anchor: ptr(anchorAt(90000, 0)),
	}
	results := FanOut(context.Background(), []RepInput{in}, DefaultOptions())
	if results[0].Wraps != 1 {
		t.Fatalf("wraps=%d want 1", results[0].Wraps)
	}
	if results[0].Rollbacks != 0 {
		t.Fatalf("rollbacks=%d want 0", results[0].Rollbacks)
	}
}

func ptr(a clock.ClockAnchor) *clock.ClockAnchor { return &a }

// mustInit recreates an init path helper used when files were written by
// writeRepFiles (which already created the directory).
func mustInit(t *testing.T, dir, rep string) string {
	t.Helper()
	return buildInitFile(t, filepath.Join(dir, rep))
}
