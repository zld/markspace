package render

import (
	"image"
	"os"
	"testing"

	"avcheck/internal/media"
	"avcheck/internal/timeline"
)

func mkOutputs(n int) []timeline.OutputFrame {
	clock, _ := media.NewClock(90000)
	outs := make([]timeline.OutputFrame, n)
	for i := range outs {
		t, _ := clock.ToSeconds(int64(i) * 3000)
		st := media.StateClean
		if i%7 == 3 {
			st = media.StateRecovered
		}
		if i%11 == 5 {
			st = media.StatePlaceholder
		}
		outs[i] = timeline.OutputFrame{
			Index: uint64(1000 + i),
			Time:  t,
			Primary: &media.Frame{
				StreamID: 1, Seq: uint64(1000 + i), Ticks: int64(i) * 3000,
				Key: i%10 == 0, State: st,
			},
			Sources: []timeline.SourceState{
				{TrackID: 1, Frame: st, Track: timeline.TrackRecovered},
				{TrackID: 2, Frame: media.StateClean, Track: timeline.TrackClean},
			},
		}
	}
	return outs
}

func testRenderer(of timeline.OutputFrame) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 8, 4))
	for i := range img.Pix {
		img.Pix[i] = uint8(of.Index)*3 + uint8(i)
	}
	return img
}

func TestExportReproducible(t *testing.T) {
	outs := mkOutputs(50)
	d1 := t.TempDir()
	d2 := t.TempDir()
	m1, err := Export(d1, 2, outs, 10, testRenderer)
	if err != nil {
		t.Fatal(err)
	}
	m2, err := Export(d2, 2, outs, 10, testRenderer)
	if err != nil {
		t.Fatal(err)
	}
	if m1.Summary != m2.Summary || len(m1.Segments) != 5 {
		t.Fatalf("rerun mismatch: %s vs %s", m1.Summary, m2.Summary)
	}
	for i := range m1.Segments {
		if m1.Segments[i].SHA256 != m2.Segments[i].SHA256 {
			t.Fatalf("segment %d hash mismatch", i)
		}
	}
	if m1.Segments[0].Recovered == 0 || m1.Segments[0].Placeholders == 0 {
		t.Fatalf("manifest must count recovery states: %+v", m1.Segments[0])
	}
	// on-disk verification
	lm, err := LoadManifest(d1)
	if err != nil {
		t.Fatal(err)
	}
	matched, err := VerifySegments(d1, lm)
	if err != nil {
		t.Fatal(err)
	}
	if matched != 5 {
		t.Fatalf("verified %d/5", matched)
	}
}

func TestExportDetectsTamper(t *testing.T) {
	outs := mkOutputs(20)
	d := t.TempDir()
	m, err := Export(d, 2, outs, 10, testRenderer)
	if err != nil {
		t.Fatal(err)
	}
	// corrupt one PNG byte
	p := d + "/frame_00003.png"
	b, _ := os.ReadFile(p)
	b[len(b)/2] ^= 0xFF
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	matched, err := VerifySegments(d, m)
	if err != nil {
		t.Fatal(err)
	}
	if matched != 1 {
		t.Fatalf("tamper must drop one segment: matched=%d", matched)
	}
}

func TestManifestRecordsAndDigest(t *testing.T) {
	clock, _ := media.NewClock(90000)
	outs := mkOutputs(12)
	d := t.TempDir()
	m, err := ExportWith(d, 2, outs, Options{SegFrames: 5, Clock: clock}, testRenderer)
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != 2 || len(m.Records) != 12 || len(m.Segments) != 3 {
		t.Fatalf("manifest shape v=%d rec=%d seg=%d", m.Version, len(m.Records), len(m.Segments))
	}
	// each record carries track provenance, frame time and recovery marks
	r := m.Records[1]
	if r.File != "frame_00001.png" || r.Time != outs[1].Time.String() || r.Tick == "" {
		t.Fatalf("record: %+v", r)
	}
	if r.Primary != 1 || r.States["1"] != "clean" || r.States["2"] != "clean" {
		t.Fatalf("record states: %+v", r)
	}
	if m.Records[3].States["1"] != "recovered" {
		t.Fatalf("recovered mark missing: %+v", m.Records[3])
	}
	if m.Records[5].States["1"] != "placeholder" {
		t.Fatalf("placeholder mark missing: %+v", m.Records[5])
	}
	if want := m.Segments[0].Recovered + m.Segments[1].Recovered + m.Segments[2].Recovered; want != 2 {
		t.Fatalf("recovered total %d", want)
	}
	lm, err := LoadManifest(d)
	if err != nil {
		t.Fatal(err)
	}
	if ManifestDigest(lm) != lm.Digest {
		t.Fatal("digest must reproduce after load")
	}
	// identical input to a second directory yields identical digest
	m2, err := ExportWith(t.TempDir(), 2, outs, Options{SegFrames: 5, Clock: clock}, testRenderer)
	if err != nil {
		t.Fatal(err)
	}
	if m2.Digest != m.Digest || m2.Summary != m.Summary {
		t.Fatal("rerun digest mismatch")
	}
}

func TestExportRejectsBadOptions(t *testing.T) {
	outs := mkOutputs(3)
	if _, err := Export(t.TempDir(), 1, outs, 0, testRenderer); err == nil {
		t.Fatal("zero segment size must be rejected")
	}
	if _, err := Export(t.TempDir(), 1, outs, 2, nil); err == nil {
		t.Fatal("nil renderer must be rejected")
	}
}
