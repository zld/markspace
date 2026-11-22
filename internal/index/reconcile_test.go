package index

import (
	"strings"
	"testing"

	"mediq/internal/bitstream"
	"mediq/internal/timeline"
)

// scanFixture builds a small clean stream with three key frames.
func scanFixture(t *testing.T, codec bitstream.Codec) *bitstream.ScanResult {
	t.Helper()
	var data []byte
	seq := uint32(0)
	for k := 0; k < 3; k++ {
		pts := timeline.Ticks(k * 3600)
		data = append(data, annexB(codec, seq, pts, []byte("body"))...)
		seq += 2 // key + inter occupy consecutive seq numbers
	}
	res, err := bitstream.Scan(codec, data, bitstream.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func annexB(codec bitstream.Codec, seq uint32, pts timeline.Ticks, body []byte) []byte {
	hdr := []byte{5}
	if codec == bitstream.CodecH265 {
		hdr = []byte{19 << 1, 1}
	}
	meta := bitstream.EncodeMeta(seq, pts, uint32(len(body)), bitstream.CRC32(body))
	inner := append(append([]byte{}, meta[:]...), body...)
	out := append([]byte{0, 0, 0, 1}, hdr...)
	return append(out, bitstream.EscapeEPB(inner)...)
}

func trueEntries(r *bitstream.ScanResult) []Entry {
	var out []Entry
	for _, u := range r.Keyframes() {
		out = append(out, Entry{Seq: u.Seq, Offset: u.Offset, PTS: u.PTS})
	}
	return out
}

func TestReconcileCleanIndexAllValid(t *testing.T) {
	scan := scanFixture(t, bitstream.CodecH264)
	rec := Reconcile(&File{Entries: trueEntries(scan)}, scan)
	if len(rec.Quarantine) != 0 || len(rec.Proven) != 3 {
		t.Fatalf("clean index: proven=%d quarantine=%d", len(rec.Proven), len(rec.Quarantine))
	}
	if rec.HitRate() != 1 {
		t.Fatalf("hit rate = %f, want 1", rec.HitRate())
	}
	for _, f := range rec.Proven {
		if f.Status != StatusValid || f.Source != "index" {
			t.Fatalf("entry not marked valid/index: %+v", f)
		}
	}
}

func TestReconcileDuplicateQuarantined(t *testing.T) {
	scan := scanFixture(t, bitstream.CodecH264)
	entries := trueEntries(scan)
	entries = append(entries, entries[0]) // exact duplicate row
	rec := Reconcile(&File{Entries: entries}, scan)
	if len(rec.Quarantine) != 1 || rec.Quarantine[0].Status != StatusDuplicate {
		t.Fatalf("want one duplicate quarantine, got %+v", rec.Quarantine)
	}
	if len(rec.Proven) != 3 {
		t.Fatalf("duplicate must not add a proven keyframe, got %d", len(rec.Proven))
	}
}

func TestReconcileOutOfRangeQuarantined(t *testing.T) {
	scan := scanFixture(t, bitstream.CodecH264)
	entries := append(trueEntries(scan), Entry{Seq: 99, Offset: scan.CompleteBytes + 100, PTS: 999})
	rec := Reconcile(&File{Entries: entries}, scan)
	if len(rec.Quarantine) != 1 || rec.Quarantine[0].Status != StatusOutOfRange {
		t.Fatalf("want one out_of_range, got %+v", rec.Quarantine)
	}
	if rec.Errors[len(rec.Errors)-1].Kind != timeline.KindIndexOutOfRange {
		t.Fatalf("error kind = %v", rec.Errors)
	}
}

func TestReconcileMispointQuarantined(t *testing.T) {
	scan := scanFixture(t, bitstream.CodecH264)
	entries := trueEntries(scan)
	entries[1].PTS += 12345 // offset is real, but PTS no longer matches
	rec := Reconcile(&File{Entries: entries}, scan)
	var mispoint int
	for _, q := range rec.Quarantine {
		if q.Status == StatusMispoint {
			mispoint++
		}
	}
	if mispoint != 1 {
		t.Fatalf("want one mispoint, got %+v", rec.Quarantine)
	}
	// The real keyframe must still be recovered from the bitstream.
	var recovered int
	for _, f := range rec.Proven {
		if f.Status == StatusRecovered && f.Source == "bitstream" {
			recovered++
		}
	}
	if recovered != 1 {
		t.Fatalf("want one bitstream recovery, got %d", recovered)
	}
}

func TestReconcileMissingRowsRecovered(t *testing.T) {
	scan := scanFixture(t, bitstream.CodecH265)
	entries := trueEntries(scan)
	rec := Reconcile(&File{Entries: entries[:1]}, scan) // only first row delivered
	if len(rec.Proven) != 3 {
		t.Fatalf("all keyframes must be proven, got %d", len(rec.Proven))
	}
	recovered := 0
	for _, f := range rec.Proven {
		if f.Status == StatusRecovered {
			recovered++
		}
	}
	if recovered != 2 {
		t.Fatalf("want 2 recovered, got %d", recovered)
	}
	// The single delivered row is valid, so hit rate over delivered
	// rows is 1; the incompleteness shows up as recovered findings.
	if rec.HitRate() != 1 {
		t.Fatalf("hit rate = %f, want 1 over delivered rows", rec.HitRate())
	}
}

func TestParseRejectsBadHeaderAndFields(t *testing.T) {
	bad := "9 0 0\n" // no magic header, malformed row
	_, errs := Parse(strings.NewReader(bad))
	if len(errs) < 2 {
		t.Fatalf("want header and field errors, got %v", errs)
	}
	good := "# mediq keyframe index v1\n# stream=x.raw sha256=ab\n0 10 900\n"
	f, errs := Parse(strings.NewReader(good))
	if len(errs) != 0 || len(f.Entries) != 1 || f.Stream != "x.raw" || f.SHA256 != "ab" {
		t.Fatalf("clean parse failed: %+v errs=%v", f, errs)
	}
}

func TestRenderRoundTrip(t *testing.T) {
	entries := []Entry{{Seq: 1, Offset: 10, PTS: 900}, {Seq: 3, Offset: 80, PTS: 2700}}
	out := Render("x.raw", "deadbeef", entries)
	f, errs := Parse(strings.NewReader(string(out)))
	if len(errs) != 0 || len(f.Entries) != 2 || f.Entries[1].Offset != 80 {
		t.Fatalf("render/parse round trip failed: %+v %v", f, errs)
	}
}
