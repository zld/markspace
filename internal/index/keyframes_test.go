package index

import (
	"strings"
	"testing"

	"github.com/pion/rtp"

	"archivecheck/internal/media"
)

func fixture() Index {
	return Index{
		Layer:     "mid",
		Timescale: 60000,
		Entries: []Entry{
			{PTS: 0, Offset: 0, Line: 1},
			{PTS: 15000, Offset: 1000, Line: 2},
			{PTS: 30000, Offset: 2000, Line: 3},
		},
	}
}

func TestParseFormatRoundTrip(t *testing.T) {
	b := Format(fixture())
	ix, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if ix.Layer != "mid" || ix.Timescale != 60000 || len(ix.Entries) != 3 {
		t.Fatalf("ix=%+v", ix)
	}
	if ix.Entries[2].PTS != 30000 || ix.Entries[2].Offset != 2000 {
		t.Fatalf("entry=%+v", ix.Entries[2])
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	cases := map[string]string{
		"missing header": "layer=x\ntimescale=1000\nentry pts=0 offset=0\n",
		"bad header":     "v2\nlayer=x\ntimescale=1000\nentry pts=0 offset=0\n",
		"zero timescale": "archive-keyframe-index v1\ntimescale=0\nentry pts=0 offset=0\n",
		"no entries":     "archive-keyframe-index v1\ntimescale=1000\n",
		"bad entry":      "archive-keyframe-index v1\ntimescale=1000\nentry pts=0\n",
		"unknown field":  "archive-keyframe-index v1\ntimescale=1000\nentry pts=0 offset=0 zz=1\n",
		"unknown key":    "archive-keyframe-index v1\ntimescale=1000\nbogus=1\n",
	}
	for name, text := range cases {
		if _, err := Parse([]byte(text)); err == nil {
			t.Fatalf("%s: parsed without error", name)
		}
	}
}

func TestValidateDetectsEachDefect(t *testing.T) {
	boundaries := []int64{0, 1000, 2000, 3000}
	streamLen := int64(4000)

	t.Run("out of range", func(t *testing.T) {
		ix := fixture()
		ix.Entries[1].Offset = 9999
		v := Validate(ix, streamLen, boundaries, ValidationOptions{})
		if len(v.Faults) != 1 || v.Faults[0].Kind != media.KindIndexOutOfRange {
			t.Fatalf("faults=%+v", v.Faults)
		}
		if len(v.Rejected) != 1 || v.Rejected[0].Kind != media.KindIndexOutOfRange {
			t.Fatalf("rejected=%+v", v.Rejected)
		}
		if len(v.Aligned) != 2 || v.Dropped != 1 {
			t.Fatalf("aligned=%d dropped=%d", len(v.Aligned), v.Dropped)
		}
	})

	t.Run("duplicate offset", func(t *testing.T) {
		ix := fixture()
		ix.Entries[2].Offset = 1000
		v := Validate(ix, streamLen, boundaries, ValidationOptions{})
		if !hasKind(v.Faults, media.KindIndexDuplicate) {
			t.Fatalf("faults=%+v", v.Faults)
		}
		if v.Dropped != 1 {
			t.Fatalf("dropped=%d", v.Dropped)
		}
	})

	t.Run("time regression", func(t *testing.T) {
		ix := fixture()
		ix.Entries[2].PTS = 100
		v := Validate(ix, streamLen, boundaries, ValidationOptions{})
		if !hasKind(v.Faults, media.KindIndexRegress) {
			t.Fatalf("faults=%+v", v.Faults)
		}
		for _, f := range v.Faults {
			if f.Kind == media.KindIndexRegress && (!f.HasSpan || f.Span.Start > f.Span.End) {
				t.Fatalf("regress span inverted: %+v", f)
			}
		}
	})

	t.Run("drift within tolerance is informational", func(t *testing.T) {
		ix := fixture()
		ix.Entries[1].Offset = 1002
		v := Validate(ix, streamLen, boundaries, ValidationOptions{DriftTolerance: 4})
		if len(v.Rejected) != 0 || len(v.Aligned) != 3 {
			t.Fatalf("rejected=%+v aligned=%d", v.Rejected, len(v.Aligned))
		}
		if v.Aligned[1].Boundary != 1000 || v.Aligned[1].Drift != -2 {
			t.Fatalf("aligned=%+v", v.Aligned[1])
		}
		info := 0
		for _, f := range v.Faults {
			if f.Kind == media.KindBoundaryDrift && f.Class == media.Informational {
				info++
			}
		}
		if info != 1 {
			t.Fatalf("informational drift faults=%d all=%+v", info, v.Faults)
		}
	})

	t.Run("drift beyond tolerance is rejected", func(t *testing.T) {
		ix := fixture()
		ix.Entries[1].Offset = 1500
		v := Validate(ix, streamLen, boundaries, ValidationOptions{DriftTolerance: 4})
		if len(v.Rejected) != 1 || v.Rejected[0].Kind != media.KindBoundaryDrift {
			t.Fatalf("rejected=%+v", v.Rejected)
		}
	})

	t.Run("no boundaries", func(t *testing.T) {
		v := Validate(fixture(), streamLen, nil, ValidationOptions{})
		if len(v.Aligned) != 0 || len(v.Rejected) != 3 {
			t.Fatalf("aligned=%d rejected=%d", len(v.Aligned), len(v.Rejected))
		}
	})
}

func TestRTCCarriageBoundaries(t *testing.T) {
	ix := fixture()
	pkts, err := EventsToRTP(ix, 0xAABBCCDD, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(pkts) != 3 {
		t.Fatalf("packets=%d", len(pkts))
	}
	// Marshal and re-parse each packet independently: indices must survive
	// packet boundaries intact.
	reparsed := make([]*rtp.Packet, 0, len(pkts))
	for _, p := range pkts {
		b, err := p.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		var q rtp.Packet
		if err := q.Unmarshal(b); err != nil {
			t.Fatal(err)
		}
		reparsed = append(reparsed, &q)
	}
	got, err := EventsFromRTP(reparsed)
	if err != nil {
		t.Fatal(err)
	}
	if got.Layer != "mid" || got.Timescale != 60000 || len(got.Entries) != 3 {
		t.Fatalf("got=%+v", got)
	}
	if got.Entries[2].PTS != 30000 || got.Entries[2].Offset != 2000 {
		t.Fatalf("entry=%+v", got.Entries[2])
	}
}

func TestRTCCarriageRejectsBrokenPackets(t *testing.T) {
	pkts, _ := EventsToRTP(fixture(), 1, 1)

	unmarked := clonePackets(pkts)
	unmarked[1].Marker = false
	if _, err := EventsFromRTP(unmarked); err == nil || !strings.Contains(err.Error(), "marker") {
		t.Fatalf("err=%v", err)
	}

	gapped := clonePackets(pkts)
	gapped[2].SequenceNumber = 9
	if _, err := EventsFromRTP(gapped); err == nil || !strings.Contains(err.Error(), "sequence gap") {
		t.Fatalf("err=%v", err)
	}

	twoEvents := clonePackets(pkts)
	twoEvents[0].Payload = append(twoEvents[0].Payload, []byte("|1|2|3")...)
	if _, err := EventsFromRTP(twoEvents); err == nil || !strings.Contains(err.Error(), "fields") {
		t.Fatalf("err=%v", err)
	}

	mixed := clonePackets(pkts)
	mixed[1].Payload = []byte("low|60000|15000|1000")
	if _, err := EventsFromRTP(mixed); err == nil || !strings.Contains(err.Error(), "disagrees") {
		t.Fatalf("err=%v", err)
	}

	if _, err := EventsFromRTP(nil); err == nil {
		t.Fatal("empty packet list accepted")
	}
}

func clonePackets(in []*rtp.Packet) []*rtp.Packet {
	out := make([]*rtp.Packet, len(in))
	for i, p := range in {
		cp := *p
		cp.Payload = append([]byte(nil), p.Payload...)
		out[i] = &cp
	}
	return out
}

func TestClockMillis(t *testing.T) {
	ix := Index{Timescale: 90000}
	if got := ix.ClockMillis(22500); got != 250 {
		t.Fatalf("got=%d", got)
	}
	if got := (Index{}).ClockMillis(5); got != 0 {
		t.Fatalf("zero timescale got=%d", got)
	}
}

func hasKind(fs []media.Fault, k media.FaultKind) bool {
	for _, f := range fs {
		if f.Kind == k {
			return true
		}
	}
	return false
}
