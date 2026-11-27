package ingest

import (
	"testing"

	merr "medialib/internal/errors"
)

func testSpec() StreamSpec {
	return StreamSpec{
		ClockRate: 32000, Packets: 10, BaseSeq: 100, BaseTS: 7,
		FrameSamples: []uint32{640, 960},
		DupSeq:       []int{3}, DropSeq: []int{5}, Seed: 1,
	}
}

func TestGenRTPAndParse(t *testing.T) {
	raw := GenRTP(testSpec())
	// 10 packets - 1 dropped + 1 duplicate = 10 datagrams.
	if len(raw) != 10 {
		t.Fatalf("datagrams=%d", len(raw))
	}
	seen := map[uint16]int{}
	for i, b := range raw {
		p, err := ParseRTP(b, i)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Payload) == 0 {
			t.Fatal("empty payload")
		}
		seen[p.Seq]++
	}
	if seen[103] != 2 {
		t.Fatalf("dup seq 103 seen %d times", seen[103])
	}
	if seen[105] != 0 {
		t.Fatal("dropped seq 105 present")
	}
}

func TestParseRTPTruncated(t *testing.T) {
	raw := GenRTP(testSpec())
	if _, err := ParseRTP(raw[0][:5], 0); err == nil {
		t.Fatal("expected error")
	} else if st, _ := merr.StageOf(err); st != merr.StageIngest {
		t.Fatalf("stage=%s", st)
	}
}

func TestGenRTPDeterministic(t *testing.T) {
	a, b := GenRTP(testSpec()), GenRTP(testSpec())
	for i := range a {
		if string(a[i]) != string(b[i]) {
			t.Fatalf("datagram %d differs", i)
		}
	}
}
