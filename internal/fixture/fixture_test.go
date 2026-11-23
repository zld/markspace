package fixture

import (
	"bytes"
	"testing"
)

func TestGenerateMP4Deterministic(t *testing.T) {
	spec := DefaultSpec(42, 24)
	a, la, err := GenerateMP4(spec)
	if err != nil {
		t.Fatal(err)
	}
	b, lb, err := GenerateMP4(spec)
	if err != nil || !bytes.Equal(a, b) {
		t.Fatal("repeated generation must be byte-identical")
	}
	if la.MdatOffset != lb.MdatOffset || len(la.Tracks) != 3 {
		t.Fatalf("layout mismatch: %+v", la)
	}
	// Every recorded sample lies inside the container and inside mdat.
	for id, tl := range la.Tracks {
		for i, off := range tl.SampleOffset {
			if off < la.MdatOffset+8 || off+tl.SampleSize[i] > int64(len(a)) {
				t.Fatalf("track %d sample %d outside mdat: off=%d", id, i, off)
			}
		}
	}
}

func TestSamplePayloadRoundTrip(t *testing.T) {
	p := SamplePayload(2, 7, 99, 128)
	if !ValidPayload(p, 2, 7) {
		t.Fatal("freshly generated payload must validate")
	}
	p[0] = 0
	if ValidPayload(p, 2, 7) {
		t.Fatal("destroyed magic must fail validation")
	}
}

func TestDamageVariants(t *testing.T) {
	spec := DefaultSpec(42, 24)
	data, layout, err := GenerateMP4(spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("truncated", func(t *testing.T) {
		out, dmg, err := TruncateBox(data, layout, 64)
		if err != nil {
			t.Fatal(err)
		}
		if dmg.Case != CaseTruncated || dmg.Offset != int64(len(out)) {
			t.Fatalf("damage metadata wrong: %+v", dmg)
		}
	})
	t.Run("out-of-range stco", func(t *testing.T) {
		out, dmg, err := ShiftChunkOffsets(data, layout, 1)
		if err != nil {
			t.Fatal(err)
		}
		if dmg.Case != CaseOutOfRange || dmg.Offset < 0 || !bytes.Equal(out[:dmg.Offset], data[:dmg.Offset]) {
			t.Fatalf("shift damage wrong: %+v", dmg)
		}
	})
	t.Run("corrupt sample", func(t *testing.T) {
		out, dmg, err := CorruptSample(data, layout, 2, 3)
		if err != nil {
			t.Fatal(err)
		}
		if dmg.Case != CaseCorrupt || string(out[dmg.Offset:dmg.Offset+4]) != "\xff\xff\xff\xff" {
			t.Fatalf("corruption not applied at offset: %+v", dmg)
		}
	})
	t.Run("missing index", func(t *testing.T) {
		mspec, dmg := MissingIndex(spec, 1)
		out, layout, err := GenerateMP4(mspec)
		if err != nil {
			t.Fatal(err)
		}
		// Track 1 loses stco/stsc; the other two tracks keep their tables.
		count := map[string]int{}
		types, err := BoxTypes(out)
		if err != nil {
			t.Fatal(err)
		}
		for _, typ := range types {
			count[typ]++
		}
		if count["stco"] != 2 || count["stsc"] != 2 {
			t.Fatalf("expected exactly 2 stco/stsc boxes, got %+v", count)
		}
		if _, ok := layout.Tracks[1]; !ok {
			t.Fatal("track 1 layout missing")
		}
		if dmg.Case != CaseMissingIndex {
			t.Fatalf("wrong damage case: %s", dmg.Case)
		}
	})
	t.Run("clock overflow spec", func(t *testing.T) {
		ospec := ClockOverflow(spec)
		for _, tr := range ospec.Tracks {
			if tr.Kind == "soun" && tr.Delta != 1<<63-1 {
				t.Fatal("audio delta not set to overflow value")
			}
		}
	})
}

func TestGenerateRTPDeterministicAndDamaged(t *testing.T) {
	cfg := DefaultRTPConfig(42, 120, 0.08)
	s1 := GenerateRTP(cfg)
	s2 := GenerateRTP(cfg)
	if len(s1.Packets) != len(s2.Packets) {
		t.Fatal("packet count must be stable")
	}
	for i := range s1.Packets {
		a, _ := s1.Packets[i].Marshal()
		b, _ := s2.Packets[i].Marshal()
		if !bytes.Equal(a, b) {
			t.Fatalf("packet %d differs between runs", i)
		}
	}
	if len(s1.PermanentLost) == 0 || len(s1.Late) == 0 || len(s1.Duplicates) == 0 {
		t.Fatalf("expected loss, late and duplicate events: %+v",
			[]int{len(s1.PermanentLost), len(s1.Late), len(s1.Duplicates)})
	}
	// Duplicates really appear twice on the wire.
	seen := map[uint16]int{}
	for _, p := range s1.Packets {
		seen[p.SequenceNumber]++
	}
	dupCount := 0
	for _, n := range seen {
		if n > 1 {
			dupCount++
		}
	}
	if dupCount != len(s1.Duplicates) {
		t.Fatalf("duplicate count mismatch: wire=%d declared=%d", dupCount, len(s1.Duplicates))
	}
	win := GenerateRTPWindowLoss(cfg, 8)
	if len(win.PermanentLost) < 8 {
		t.Fatal("window loss must record at least 8 permanently lost sequences")
	}
}
