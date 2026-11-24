package container

import (
	"os"
	"path/filepath"
	"testing"

	"mediacheck/internal/fixture"
)

func writeTemp(t *testing.T, data []byte) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "sample.mp4")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func twoTrackSpec() fixture.MediaSpec {
	return fixture.MediaSpec{
		MovieScale: 1000,
		Tracks: []fixture.TrackSpec{
			{TrackID: 1, Handler: "vide", Timescale: 24000, SampleCount: 240, Delta: 1000, SyncEvery: 24},
			{TrackID: 2, Handler: "soun", Timescale: 48000, SampleCount: 480, Delta: 1000},
		},
	}
}

func TestScanHappyPath(t *testing.T) {
	p := writeTemp(t, fixture.BuildMP4(twoTrackSpec()))
	res, err := Scan(p)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(res.Tracks) != 2 {
		t.Fatalf("tracks = %d, want 2", len(res.Tracks))
	}
	v := res.Tracks[0]
	if v.TrackID != 1 || v.HandlerType != "vide" || v.Timescale != 24000 {
		t.Fatalf("video track decoded wrong: %+v", v)
	}
	if v.Index.Undecidable {
		t.Fatal("complete index must not be undecidable")
	}
	if len(v.RawSamples) != 240 {
		t.Fatalf("materialized samples = %d, want 240", len(v.RawSamples))
	}
	if !v.RawSamples[0].IsSync || !v.RawSamples[23].IsSync {
		t.Fatal("stss sync flags must mark samples 1 and 24 (index 23)")
	}
	a := res.Tracks[1]
	if a.Kind().String() != "audio" {
		t.Fatalf("handler mapping = %v", a.Kind())
	}
}

func TestScanMissingIndexIsUndecidable(t *testing.T) {
	spec := twoTrackSpec()
	spec.Tracks[0].MissingIndex = true
	p := writeTemp(t, fixture.BuildMP4(spec))
	res, err := Scan(p)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	idx := res.Tracks[0].Index
	if !idx.Undecidable {
		t.Fatal("missing stts must mark the index undecidable, never complete")
	}
	if idx.Reason == "" {
		t.Fatal("undecidable index must carry a reason")
	}
}

func TestScanTruncatedIndexPayload(t *testing.T) {
	spec := twoTrackSpec()
	spec.Tracks[0].TruncatedIndex = true
	p := writeTemp(t, fixture.BuildMP4(spec))
	res, err := Scan(p)
	if err != nil {
		t.Fatalf("cut index payload must degrade, not hard-fail: %v", err)
	}
	idx := res.Tracks[0].Index
	if !idx.Undecidable || !idx.TruncatedPayload {
		t.Fatalf("cut stts must be undecidable+truncated, got %+v", idx)
	}
}

func TestPreScanTruncatedBox(t *testing.T) {
	data := fixture.BuildMP4(twoTrackSpec())
	// Cut the file in the middle of moov: the pre-scan must flag a
	// truncation instead of panicking or claiming success.
	cut := len(data) - 64
	p := writeTemp(t, data[:cut])
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	pre, err := PreScanFile(f)
	if err != nil {
		t.Fatalf("PreScanFile must not hard-fail on truncation: %v", err)
	}
	if len(pre.Truncations) == 0 {
		t.Fatal("truncated tail must produce at least one truncation record")
	}
}

func TestParseRejectsFileWithoutMoov(t *testing.T) {
	// ftyp alone: no moov survives, parse must return ErrUnparseable.
	p := writeTemp(t, fixture.Box("ftyp", []byte("isom\x00\x00\x02\x00isom")))
	_, err := Scan(p)
	if err == nil {
		t.Fatal("asset without moov must be unparseable")
	}
}

func TestPreScanTrefEdges(t *testing.T) {
	spec := twoTrackSpec()
	spec.Trefs = []fixture.TrefSpec{
		{FromTrack: 2, RefType: "hint", ToTrackIDs: []uint32{1}},
	}
	p := writeTemp(t, fixture.BuildMP4(spec))
	f, _ := os.Open(p)
	defer f.Close()
	pre, err := PreScanFile(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(pre.Trefs) != 1 || pre.Trefs[0].ToTrackID != 1 || pre.Trefs[0].RefType != "hint" {
		t.Fatalf("tref edges = %+v", pre.Trefs)
	}
	if pre.Trefs[0].FromTrackID != 2 {
		t.Fatalf("tref source track = %d, want 2", pre.Trefs[0].FromTrackID)
	}
}

func TestInputSizeLimit(t *testing.T) {
	p := writeTemp(t, make([]byte, MaxInputBytes+1))
	if _, err := Scan(p); err == nil {
		t.Fatal("oversized input must be rejected")
	}
}
