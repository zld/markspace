package core

import (
	"crypto/sha256"
	"errors"
	"testing"
)

const (
	testOff = int64(5000)   // clock offset in ref ticks
	testPPM = int64(80)     // source drift
	delayTk = int64(3600)   // 40 ms arrival delay
	baseP0  = int64(8589720000)
)

// refOf maps source ticks to ref ticks with exact 80ppm drift,
// rounding halves away from zero (mirrors timebase.Clock).
func refOf(x int64) int64 {
	num := x * 1000080
	den := int64(1000000)
	q := num / den
	r := num % den
	if 2*abs64(r) >= den {
		if num >= 0 {
			q++
		} else {
			q--
		}
	}
	return testOff + q
}

func vfrDurs() []uint32 {
	out := make([]uint32, 60)
	for j := range out {
		out[j] = uint32(1500 + (int64(j%5)-2)*3) // sums to 90000
	}
	return out
}

func mkInfo(track uint32, base int64, fpSeed string, size int64) *SegmentInfo {
	raw := base % PTSModulus
	var samples []Sample
	var dur int64
	for _, d := range vfrDurs() {
		samples = append(samples, Sample{Duration: int64(d)})
		dur += int64(d)
	}
	if dur != 90000 {
		panic("vfr durations must sum to 90000")
	}
	return &SegmentInfo{
		TrackID: track, BaseTime: raw, Samples: samples,
		Fingerprint: sha256.Sum256([]byte(fpSeed)), Size: size,
	}
}

// mkEvents builds anchors on the source clock at multiples of 12500
// (so ref values are exact integers under 80ppm drift).
func mkEvents(c0 int64, n int) []Event {
	var evs []Event
	for k := 0; k < n; k++ {
		c := c0 + int64(k)*12500
		evs = append(evs, Event{
			ArrivalRef: testOff + c + c*testPPM/1000000,
			RTPTime:    uint32(c % RTPModulus),
			Seq:        uint16(k),
		})
	}
	return evs
}

func normalInput() Input {
	anchorC0 := int64(8589600000) // multiple of 12500, near baseP0
	in := Input{Events: mkEvents(anchorC0, 40)}
	sizes := []int64{1000, 1100, 1200, 1300, 1400}
	for i := 0; i < 5; i++ {
		c := baseP0 + int64(i)*90000
		in.Records = append(in.Records, Record{
			Index: int64(i), Source: segName(i), SourceID: srcID(i),
			Line: 5 + i, ArrivalRef: refOf(c) + delayTk,
			Info: mkInfo(1, c, segName(i), sizes[i]),
		})
	}
	// duplicate of segment 2 from the backup source, arriving 5ms later
	c2 := baseP0 + 2*90000
	dup := Record{
		Index: 5, Source: "seg2b.m4s", SourceID: "backup",
		Line: 10, ArrivalRef: refOf(c2) + delayTk + 450,
		Info: mkInfo(1, c2, "seg2.m4s", 1200),
	}
	in.Records = append(in.Records, dup)
	return in
}

func segName(i int) string { return "seg" + string(rune('0'+i)) + ".m4s" }
func srcID(i int) string {
	if i >= 3 {
		return "backup"
	}
	return "primary"
}

func TestBuildNormal(t *testing.T) {
	res, err := Build(normalInput())
	if err != nil {
		t.Fatal(err)
	}
	s := res.Summary
	if s.Mapped != 6 || s.Emitted != 5 || s.Duplicate != 1 || s.Lost != 0 {
		t.Fatalf("summary: %+v", s)
	}
	if s.P50DelayMs != 40 {
		t.Fatalf("p50=%d, want 40", s.P50DelayMs)
	}
	if s.DriftPPM != 80 {
		t.Fatalf("drift=%d, want 80", s.DriftPPM)
	}
	if s.MaxAbsTimeDeltaUs > 1000 {
		t.Fatalf("max time delta=%d us", s.MaxAbsTimeDeltaUs)
	}
	if s.MaxAbsByteDelta != 0 {
		t.Fatalf("max byte delta=%d", s.MaxAbsByteDelta)
	}
	var dup *Item
	for i := range res.Items {
		if res.Items[i].Decision == DecDuplicate {
			dup = &res.Items[i]
		}
	}
	if dup == nil || dup.Source != "seg2b.m4s" || dup.Note == "" {
		t.Fatalf("duplicate item: %+v", dup)
	}
}

func TestBuildDeterministic(t *testing.T) {
	a, err := Build(normalInput())
	if err != nil {
		t.Fatal(err)
	}
	b, err := Build(normalInput())
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Items) != len(b.Items) {
		t.Fatal("item count differs")
	}
	for i := range a.Items {
		if a.Items[i] != b.Items[i] {
			t.Fatalf("item %d differs", i)
		}
	}
	if a.Summary != b.Summary {
		t.Fatal("summaries differ")
	}
}

func TestBuildGapResume(t *testing.T) {
	in := Input{Events: mkEvents(8589600000, 100)}
	order := []int{0, 1, 2, 4, 5} // media sequence 3 never arrives
	for idx, i := range order {
		c := baseP0 + int64(i)*180000
		durs := make([]Sample, 0, 120)
		for j := 0; j < 120; j++ {
			durs = append(durs, Sample{Duration: 1500})
		}
		raw := c % PTSModulus
		in.Records = append(in.Records, Record{
			Index: int64(idx), Source: segName(i), SourceID: "primary",
			Line: 5 + idx, ArrivalRef: refOf(c) + delayTk,
			Info: &SegmentInfo{
				TrackID: 1, BaseTime: raw, Samples: durs,
				Fingerprint: sha256.Sum256([]byte(segName(i))), Size: 2000,
			},
		})
	}
	res, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	s := res.Summary
	if s.Lost != 1 || s.GapMs != 2000 || s.Resumed != 1 {
		t.Fatalf("summary: %+v", s)
	}
	found := false
	for _, it := range res.Items {
		if it.Decision == DecUnrecovered {
			found = true
		}
	}
	if !found {
		t.Fatal("no unrecovered interval marked")
	}
}

func TestBuildAmbiguousJump(t *testing.T) {
	in := Input{Events: mkEvents(8589600000, 10)}
	for i, base := range []int64{baseP0, baseP0 + RTPModulus} {
		in.Records = append(in.Records, Record{
			Index: int64(i), Source: segName(i), Line: 5 + i,
			ArrivalRef: refOf(base) + delayTk,
			Info:       mkInfo(1, base, segName(i), 1000),
		})
	}
	_, err := Build(in)
	var ce *Error
	if !errors.As(err, &ce) || ce.Code != CodeAmbiguousJump {
		t.Fatalf("want ambiguous_jump, got %v", err)
	}
}

func TestBuildNoAnchors(t *testing.T) {
	in := Input{Records: []Record{
		{Index: 0, Source: "seg0.m4s", Info: mkInfo(1, baseP0, "x", 1000)},
	}}
	if _, err := Build(in); err == nil {
		t.Fatal("expected error without anchors")
	}
}
