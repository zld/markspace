package fingerprint

import (
	"bytes"
	"testing"
	"time"

	"github.com/pion/rtp"

	"rtpreplay/ingest"
	"rtpreplay/internal/rtplog"
	"rtpreplay/media"
	"rtpreplay/pipeline"
)

func streamFromPayloads(t *testing.T, payloads [][]byte) *ingest.Stream {
	t.Helper()
	var buf bytes.Buffer
	if err := rtplog.WriteHeader(&buf); err != nil {
		t.Fatal(err)
	}
	base := time.Unix(1_700_000_000, 0).UTC()
	for i, pl := range payloads {
		p := rtp.Packet{
			Header: rtp.Header{
				Version: 2, PayloadType: 8,
				SequenceNumber: uint16(i), Timestamp: uint32(i * 160), SSRC: 1,
			},
			Payload: pl,
		}
		raw, err := p.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		if err := rtplog.WriteRecord(&buf, base.Add(time.Duration(i)*20*time.Millisecond), raw); err != nil {
			t.Fatal(err)
		}
	}
	st, err := ingest.ParseRTPStream(bytes.NewReader(buf.Bytes()), int64(buf.Len()), media.AudioClockRate, 32)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func repOf(st *ingest.Stream) *pipeline.Representation {
	reps, _ := pipeline.Fanout([]*ingest.Stream{st}, pipeline.KindAudio, pipeline.DefaultMaxGap)
	return reps[0]
}

func TestFingerprintStableAndHeaderIndependent(t *testing.T) {
	a := Fingerprint([]byte{1, 2, 3})
	b := Fingerprint([]byte{1, 2, 3})
	if a != b || a == 0 {
		t.Fatal("identical content must hash identically")
	}
	if Fingerprint([]byte{1, 2, 4}) == a {
		t.Fatal("different content must hash differently")
	}
}

func TestDedupeSinglePacket(t *testing.T) {
	a, b, c := []byte{10}, []byte{20}, []byte{30}
	st := streamFromPayloads(t, [][]byte{a, b, c, a, b, c})
	dups := Dedupe(repOf(st), 1)
	if len(dups) != 3 {
		t.Fatalf("want 3 duplicates, got %+v", dups)
	}
	// First occurrence is retained; matches point back to the earlier position.
	want := []struct{ first, match int }{
		{0, 3}, {1, 4}, {2, 5},
	}
	for i, w := range want {
		if dups[i].FirstStartRecord != w.first || dups[i].MatchStartRecord != w.match {
			t.Fatalf("dup %d = %+v", i, dups[i])
		}
		if dups[i].Fingerprint == "" || dups[i].WindowPackets != 1 {
			t.Fatalf("dup metadata missing: %+v", dups[i])
		}
	}
}

func TestDedupeMergesConsecutiveRepeats(t *testing.T) {
	x := []byte{7}
	st := streamFromPayloads(t, [][]byte{x, x, x, x})
	dups := Dedupe(repOf(st), 1)
	if len(dups) != 1 {
		t.Fatalf("consecutive repeats must merge into one fragment: %+v", dups)
	}
	d := dups[0]
	if d.FirstStartRecord != 0 || d.MatchStartRecord != 1 || d.MatchEndRecord != 3 || d.WindowPackets != 3 {
		t.Fatalf("merged range wrong: %+v", d)
	}
}

func TestDedupeWindowedContent(t *testing.T) {
	// Two-packet windows: [A,B] at 0-2 reappears at 3-5.
	p := [][]byte{{1}, {2}, {3}, {1}, {2}, {9}}
	st := streamFromPayloads(t, p)
	dups := Dedupe(repOf(st), 2)
	if len(dups) != 1 {
		t.Fatalf("want one window match, got %+v", dups)
	}
	d := dups[0]
	if d.FirstStartRecord != 0 || d.FirstEndRecord != 1 ||
		d.MatchStartRecord != 3 || d.MatchEndRecord != 4 {
		t.Fatalf("window match wrong: %+v", d)
	}
}

func TestDedupeNoDuplicates(t *testing.T) {
	st := streamFromPayloads(t, [][]byte{{1}, {2}, {3}})
	if dups := Dedupe(repOf(st), 1); len(dups) != 0 {
		t.Fatalf("unique content must produce no duplicates: %+v", dups)
	}
}

func streamWithSeqs(t *testing.T, seqs []uint16, payloads [][]byte) *ingest.Stream {
	t.Helper()
	var buf bytes.Buffer
	if err := rtplog.WriteHeader(&buf); err != nil {
		t.Fatal(err)
	}
	base := time.Unix(1_700_000_000, 0).UTC()
	for i, pl := range payloads {
		p := rtp.Packet{
			Header: rtp.Header{
				Version: 2, PayloadType: 8,
				SequenceNumber: seqs[i], Timestamp: uint32(i * 160), SSRC: 1,
			},
			Payload: pl,
		}
		raw, err := p.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		if err := rtplog.WriteRecord(&buf, base.Add(time.Duration(i)*20*time.Millisecond), raw); err != nil {
			t.Fatal(err)
		}
	}
	st, err := ingest.ParseRTPStream(bytes.NewReader(buf.Bytes()), int64(buf.Len()), media.AudioClockRate, 32)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestDedupeSkipsIsolatedPackets(t *testing.T) {
	// A sequence restart (gap > 3000) isolates the final packet even though
	// its payload repeats the first packet's content.
	st := streamWithSeqs(t,
		[]uint16{0, 1, 5000},
		[][]byte{{1}, {2}, {1}},
	)
	rep := repOf(st)
	if !rep.Stream.Packets[2].Clock.Isolated {
		t.Fatal("fixture must isolate the restart packet")
	}
	if dups := Dedupe(rep, 1); len(dups) != 0 {
		t.Fatalf("isolated packet content must not be fingerprinted: %+v", dups)
	}
}

func TestDedupeOversizedWindow(t *testing.T) {
	st := streamFromPayloads(t, [][]byte{{1}, {1}, {2}})
	if dups := Dedupe(repOf(st), 99); dups != nil {
		t.Fatalf("window larger than stream must yield nil, got %+v", dups)
	}
}
