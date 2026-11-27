package index

import (
	"bytes"
	"testing"

	"github.com/pion/rtp"

	"mediacheck/internal/timeline"
)

func TestReconcileHitsMissesFalsePositives(t *testing.T) {
	base := timeline.TimeBase{Num: 1, Den: 1000}
	kfs := []Keyframe{{TS: 100, Offset: 0}, {TS: 200, Offset: 50}, {TS: 300, Offset: 90}}
	// Two correct entries, one false positive, one real keyframe omitted.
	entries := []Entry{
		{Track: "v", TS: 100, Base: base, ByteOffset: 0},
		{Track: "v", TS: 201, Base: base, ByteOffset: 50}, // within tolerance
		{Track: "v", TS: 999, Base: base, ByteOffset: 400}, // no keyframe
	}
	rep, err := Reconcile("v", entries, kfs, base, Config{ToleranceTicks: 2})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Hits != 2 || rep.Misses != 1 || rep.FalsePositives != 1 {
		t.Fatalf("counts wrong: %+v", rep)
	}
	// Hit rate is measured against the 3 real keyframes.
	if rep.HitRate < 0.66 || rep.HitRate > 0.67 {
		t.Fatalf("hit rate=%f want ~0.667", rep.HitRate)
	}
	// The tolerated hit records its offset and signed delta.
	var sawDelta bool
	for _, r := range rep.Records {
		if r.Kind == KindHit && r.StreamTS == 200 {
			if r.DeltaTicks != -1 || r.StreamOff != 50 {
				t.Fatalf("delta/offset wrong: %+v", r)
			}
			sawDelta = true
		}
	}
	if !sawDelta {
		t.Fatal("tolerated hit not reported")
	}
}

func TestReconcileTimeBaseProjection(t *testing.T) {
	analysis := timeline.TimeBase{Num: 1, Den: 90000}
	indexBase := timeline.TimeBase{Num: 1, Den: 30}
	// Index frame 1 at 30fps == 3000 ticks at 90kHz.
	kfs := []Keyframe{{TS: 3000, Offset: 7}}
	entries := []Entry{{Track: "v", TS: 1, Base: indexBase, ByteOffset: 7}}
	rep, err := Reconcile("v", entries, kfs, analysis, Config{ToleranceTicks: 5})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Hits != 1 {
		t.Fatalf("cross-timebase hit failed: %+v", rep)
	}
}

func TestReconcilePerfectAndEmpty(t *testing.T) {
	base := timeline.TimeBase{Num: 1, Den: 100}
	kfs := []Keyframe{{TS: 10, Offset: 0}, {TS: 20, Offset: 5}}
	entries := []Entry{
		{TS: 10, Base: base}, {TS: 20, Base: base},
	}
	rep, err := Reconcile("v", entries, kfs, base, Config{ToleranceTicks: 0})
	if err != nil {
		t.Fatal(err)
	}
	if rep.HitRate != 1.0 || rep.Hits != 2 || rep.Misses != 0 {
		t.Fatalf("perfect match wrong: %+v", rep)
	}
	empty, err := Reconcile("v", nil, nil, base, Config{})
	if err != nil || empty.HitRate != 1.0 {
		t.Fatalf("empty reconciliation wrong: %+v err=%v", empty, err)
	}
}

func TestReconcileCapRejection(t *testing.T) {
	base := timeline.TimeBase{Num: 1, Den: 1}
	_, err := Reconcile("v", make([]Entry, MaxIndexEntries+1), nil, base, Config{})
	if err == nil {
		t.Fatal("oversized index must be rejected")
	}
	if _, err := Reconcile("v", nil, nil, base, Config{ToleranceTicks: -1}); err == nil {
		t.Fatal("negative tolerance must be rejected")
	}
}

func TestRTPParserAndWrap(t *testing.T) {
	// Build a real RTP packet with pion, then parse it back.
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    96,
			SequenceNumber: 42,
			Timestamp:      0xFFFFFFF0,
			SSRC:           7,
			Marker:         true,
		},
		Payload: []byte{1, 2, 3},
	}
	raw, err := pkt.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	s, err := ParseRTPPacket(raw)
	if err != nil {
		t.Fatal(err)
	}
	if s.Sequence != 42 || s.TS != 0xFFFFFFF0 || s.SSRC != 7 || !s.Marker {
		t.Fatalf("RTP parse wrong: %+v", s)
	}
	if RTPOrdering([]RTPSample{{Sequence: 0}, {Sequence: 1}, {Sequence: 3}}) != 1 {
		t.Fatal("sequence gap not detected")
	}
	// Wrap-aware comparison across the 2^32 boundary.
	if !RTPTimestampMatches(5, 0xFFFFFFFE, 8) {
		t.Fatal("wrapped timestamps should match within tolerance")
	}
	if RTPTimestampMatches(100, 0, 5) {
		t.Fatal("distant timestamps must not match")
	}
	if _, err := ParseRTPPacket([]byte{0x80}); err == nil {
		t.Fatal("truncated RTP packet must error")
	}
}

func TestMarkerEntries(t *testing.T) {
	samples := []RTPSample{
		{Sequence: 1, TS: 10, Marker: true},
		{Sequence: 2, TS: 20, Marker: false},
		{Sequence: 3, TS: 30, Marker: true},
	}
	base := timeline.TimeBase{Num: 1, Den: 90000}
	entries := MarkerEntries("v", samples, base)
	if len(entries) != 2 || entries[0].TS != 10 || entries[1].ByteOffset != -1 {
		t.Fatalf("marker entries wrong: %+v", entries)
	}
}

func TestMP4RoundTrip(t *testing.T) {
	sync := []uint32{1, 5, 9}
	raw, err := WriteMinimalMP4(30000, sync, 12)
	if err != nil {
		t.Fatal(err)
	}
	metas, err := ReadMP4KeyframeMeta(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 {
		t.Fatalf("tracks=%d want 1", len(metas))
	}
	m := metas[0]
	if m.Handler != "vide" || m.Timescale != 30000 {
		t.Fatalf("metadata wrong: %+v", m)
	}
	if len(m.SyncSamples) != 3 || m.SyncSamples[1] != 5 {
		t.Fatalf("sync samples wrong: %+v", m.SyncSamples)
	}
	base, err := m.TimeBase()
	if err != nil || base.Den != 30000 {
		t.Fatalf("time base wrong: %+v err=%v", base, err)
	}
	ts := make([]int64, 12)
	for i := range ts {
		ts[i] = int64(i) * 1001
	}
	entries, err := SyncSampleEntries("v", m, ts)
	if err != nil || len(entries) != 3 || entries[1].TS != 4*1001 {
		t.Fatalf("entries wrong: %+v err=%v", entries, err)
	}
	// Out-of-range sample number must be rejected.
	bad := MP4TrackMeta{Timescale: 1, SyncSamples: []uint32{99}}
	if _, err := SyncSampleEntries("v", bad, []int64{1}); err == nil {
		t.Fatal("out-of-range sync sample must fail")
	}
}
