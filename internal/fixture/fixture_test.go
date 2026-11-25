package fixture

import (
	"bytes"
	"testing"

	"archive.example/mediakernel/internal/metadata"
	"archive.example/mediakernel/internal/rtp"
)

func TestBuildDeterministic(t *testing.T) {
	for _, kind := range []Kind{KindNormal, KindTruncated, KindDamaged, KindLate, KindBoundary} {
		one, err := Build(kind, 17)
		if err != nil {
			t.Fatal(err)
		}
		two, err := Build(kind, 17)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(one.MP4, two.MP4) {
			t.Fatalf("%s: MP4 differs between runs", kind)
		}
		if len(one.RTP) != len(two.RTP) {
			t.Fatalf("%s: RTP length differs", kind)
		}
		for i := range one.RTP {
			if !bytes.Equal(one.RTP[i].Bytes, two.RTP[i].Bytes) {
				t.Fatalf("%s: RTP packet %d differs", kind, i)
			}
			if one.RTP[i].Offset != two.RTP[i].Offset {
				t.Fatalf("%s: RTP offset %d differs", kind, i)
			}
		}
	}
}

func TestBuildSeedChangesOutput(t *testing.T) {
	a, err := Build(KindNormal, 17)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Build(KindNormal, 18)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a.MP4, b.MP4) {
		t.Fatal("different seeds produced identical MP4")
	}
}

func TestNormalFixtureParsesAllTracks(t *testing.T) {
	arc, err := Build(KindNormal, 17)
	if err != nil {
		t.Fatal(err)
	}
	doc, perr := metadata.ParseBytes(arc.MP4)
	if perr != nil {
		t.Fatalf("normal fixture reported errors: %v", perr)
	}
	if len(doc.Tracks) != len(DefaultTracks()) {
		t.Fatalf("tracks %d want %d", len(doc.Tracks), len(DefaultTracks()))
	}
	covered := map[string]int{}
	for _, e := range doc.Events {
		covered[string(e.Track)]++
	}
	for _, track := range []string{"subtitle", "chapter", "id3", "sei", "emsg"} {
		if covered[track] == 0 {
			t.Fatalf("track %s produced no events (got %v)", track, covered)
		}
	}
}

func TestTruncatedFixtureReportsTrackAndOffset(t *testing.T) {
	arc, err := Build(KindTruncated, 17)
	if err != nil {
		t.Fatal(err)
	}
	if !arc.Truncated || arc.TruncatedAt <= 0 {
		t.Fatal("fixture not marked truncated")
	}
	doc, perr := metadata.ParseBytes(arc.MP4)
	if perr == nil {
		t.Fatal("expected truncation error")
	}
	var attributed bool
	for _, e := range doc.Errors {
		if string(e.Code) == "truncated" && e.Track != "" && e.Offset > 0 {
			attributed = true
		}
	}
	if !attributed {
		t.Fatalf("no track-attributed truncation error: %v", doc.Errors)
	}
}

func TestDamagedFixtureHasHolesAndReordering(t *testing.T) {
	arc, err := Build(KindDamaged, 17)
	if err != nil {
		t.Fatal(err)
	}
	reorder, rerr := rtp.New(rtp.Config{LateWindow: 6, MaxBuffer: 256, Warmup: 4})
	if rerr != nil {
		t.Fatal(err)
	}
	var lost, dup, ooo int
	for _, p := range arc.RTP {
		// Generated payloads use version 1.
		if len(p.Bytes) < 12 {
			t.Fatalf("packet %d too short", p.Offset)
		}
		res, e := reorder.PushRaw(p.Bytes, p.Offset)
		if e != nil {
			t.Fatal(e)
		}
		lost += len(res.Lost)
		if res.Duplicate {
			dup++
		}
		if res.OutOfOrder {
			ooo++
		}
	}
	lost += len(reorder.Flush().Lost)
	if lost == 0 {
		t.Fatal("damaged fixture produced no losses")
	}
	if dup == 0 {
		t.Fatal("damaged fixture produced no duplicates")
	}
	if ooo == 0 {
		t.Fatal("damaged fixture produced no reordering")
	}
}

func TestLateFixtureQueuesLatePackets(t *testing.T) {
	arc, err := Build(KindLate, 17)
	if err != nil {
		t.Fatal(err)
	}
	reorder, rerr := rtp.New(rtp.Config{LateWindow: 6, MaxBuffer: 256, Warmup: 4})
	if rerr != nil {
		t.Fatal(err)
	}
	var late int
	for _, p := range arc.RTP {
		res, e := reorder.PushRaw(p.Bytes, p.Offset)
		if e != nil {
			t.Fatal(e)
		}
		late += len(res.Late)
	}
	late += len(reorder.Flush().Late)
	if late == 0 {
		t.Fatal("late fixture queued no late packets")
	}
	if reorder.Stats().Lost == 0 {
		t.Fatal("late fixture produced no declared losses")
	}
}

func TestBoundaryFixtureHasRangeViolation(t *testing.T) {
	arc, err := Build(KindBoundary, 17)
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := metadata.ParseBytes(arc.MP4)
	var outOfRange bool
	for _, e := range doc.Errors {
		if string(e.Code) == "time_out_of_range" {
			outOfRange = true
		}
	}
	if !outOfRange {
		t.Fatalf("boundary fixture should report an out-of-range timestamp: %v", doc.Errors)
	}
}

func TestRTPPayloadRoundTrip(t *testing.T) {
	arc, err := Build(KindNormal, 17)
	if err != nil {
		t.Fatal(err)
	}
	if len(arc.RTP) == 0 {
		t.Fatal("no RTP packets")
	}
	for i, p := range arc.RTP {
		rec, ok := rtp.DecodePayload(rtpPayloadOf(t, p.Bytes))
		if !ok {
			t.Fatalf("packet %d payload did not decode", i)
		}
		if rec.TrackID == 0 {
			t.Fatalf("packet %d lost track id", i)
		}
	}
}

func rtpPayloadOf(t *testing.T, wire []byte) []byte {
	t.Helper()
	// Header is 12 bytes (no extension, no CSRC).
	if len(wire) <= 12 {
		t.Fatalf("wire packet too short: %d", len(wire))
	}
	return wire[12:]
}

func TestParseKind(t *testing.T) {
	for _, name := range []string{"normal", "truncated", "damaged", "late", "boundary"} {
		if _, err := ParseKind(name); err != nil {
			t.Fatalf("%s rejected: %v", name, err)
		}
	}
	if _, err := ParseKind("bogus"); err == nil {
		t.Fatal("bogus fixture accepted")
	}
}

func TestCorpusSpanCovered(t *testing.T) {
	arc, err := Build(KindNormal, 17)
	if err != nil {
		t.Fatal(err)
	}
	doc, perr := metadata.ParseBytes(arc.MP4)
	if perr != nil {
		t.Fatal(perr)
	}
	var minAt, maxAt int64
	minAt = 1 << 62
	for _, e := range doc.Events {
		if e.At < minAt {
			minAt = e.At
		}
		if e.At > maxAt {
			maxAt = e.At
		}
	}
	if minAt < 0 {
		t.Fatalf("negative event time %d", minAt)
	}
	if maxAt > CorpusSpanNS {
		t.Fatalf("event time %d beyond corpus span %d", maxAt, CorpusSpanNS)
	}
}
