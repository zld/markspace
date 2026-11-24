package timeline_test

import (
	"crypto/sha256"
	"errors"
	"math"
	"testing"

	"github.com/pion/rtp"

	"github.com/zld/mediarc/internal/model"
	"github.com/zld/mediarc/internal/timeline"
)

func TestUnwrapUint32Wrap(t *testing.T) {
	u := timeline.NewUnwrapper()
	start := uint64(math.MaxUint32 - 10)
	raw := []uint64{start, start + 5, 4, 9}
	want := []int64{int64(start), int64(start + 5), int64(start + 15), int64(start + 20)}
	for i, v := range raw {
		got, _, err := u.UnwrapRaw(7, v, 32)
		if err != nil {
			t.Fatal(err)
		}
		if got != want[i] {
			t.Fatalf("sample %d unwrap=%d want %d", i, got, want[i])
		}
	}

	pkt := &rtp.Packet{Header: rtp.Header{Timestamp: uint32(9 + 100)}}
	got, err := u.UnwrapRTPPacket(7, pkt)
	if err != nil {
		t.Fatal(err)
	}
	if got != want[3]+100 {
		t.Fatalf("RTP unwrap=%d want %d", got, want[3]+100)
	}
}

func TestWatermarkWindowClassification(t *testing.T) {
	w := timeline.NewWatermark(100 * model.Millisecond)
	cases := []struct {
		t    model.NS
		want timeline.Disposition
	}{
		{0, timeline.OnTime},
		{200 * model.Millisecond, timeline.OnTime},
		{150 * model.Millisecond, timeline.Buffered},
		{0, timeline.Late},
	}
	for _, tc := range cases {
		if got := w.Observe(tc.t); got != tc.want {
			t.Fatalf("Observe(%d)=%s want %s state=%+v", tc.t, got, tc.want, w.State())
		}
	}
}

func TestDriftCorrectionPreservesTrackOrder(t *testing.T) {
	cfg := model.DefaultConfig()
	cfg.Anchors = []model.Anchor{
		{TrackID: 1, SourceNS: 0, SharedNS: 0, Stable: true},
		{TrackID: 1, SourceNS: 10_010 * model.Millisecond, SharedNS: 10 * model.Second, Stable: true},
	}
	raw := []model.RawEvent{
		{TrackID: 1, SampleNumber: 1, OrderIndex: 0, TimestampBits: 64, Timescale: uint32(model.Second), RawTimestamp: 0},
		{TrackID: 1, SampleNumber: 2, OrderIndex: 1, TimestampBits: 64, Timescale: uint32(model.Second), RawTimestamp: uint64(5_005 * model.Millisecond)},
		{TrackID: 1, SampleNumber: 3, OrderIndex: 2, TimestampBits: 64, Timescale: uint32(model.Second), RawTimestamp: uint64(10_010 * model.Millisecond)},
	}
	built, err := timeline.Build(raw, cfg)
	if err != nil {
		t.Fatal(err)
	}
	var last model.NS
	for i, ev := range built.Continuous {
		if i > 0 && ev.TimeNS < last {
			t.Fatalf("drift correction reordered event %d: %d < %d", i, ev.TimeNS, last)
		}
		last = ev.TimeNS
	}
	if got := built.Continuous[1].TimeNS; (got - 5*model.Second).Abs() > model.Millisecond {
		t.Fatalf("middle correction=%d expected ~5s", got)
	}
}

func TestCheckpointDigestMismatch(t *testing.T) {
	sum := sha256.Sum256([]byte("original"))
	cp := &timeline.Checkpoint{Input: timeline.InputSummary{SHA256: string(rune(0))}}
	cp.Input.SHA256 = hexEncode(sum[:])
	if err := cp.VerifyBytes([]byte("original")); err != nil {
		t.Fatal(err)
	}
	err := cp.VerifyBytes([]byte("tampered"))
	if !errors.Is(err, model.ErrDigestMismatch) {
		t.Fatalf("err=%v want digest mismatch", err)
	}
}

func TestRecomputeMatchesFullOrderedProcessing(t *testing.T) {
	cfg := model.DefaultConfig()
	cfg.SegmentDurationNS = 2 * model.Second
	cfg.Anchors = []model.Anchor{
		{TrackID: 1, SourceNS: 0, SharedNS: 0, Stable: true},
		{TrackID: 1, SourceNS: 8 * model.Second, SharedNS: 8 * model.Second, Stable: true},
	}
	base := make([]model.RawEvent, 0, 9)
	for i := 0; i < 5; i++ {
		base = append(base, model.RawEvent{
			TrackID: 1, SampleNumber: uint32(i + 1), OrderIndex: i,
			TimestampBits: 64, Timescale: uint32(model.Second),
			RawTimestamp: uint64(model.NS(i) * model.Second),
			Keyframe:     i%2 == 0,
		})
	}
	built, err := timeline.Build(base, cfg)
	if err != nil {
		t.Fatal(err)
	}
	cp := built.Checkpoint(timeline.InputSummary{SHA256: "x"}, cfg, "")
	late := model.RawEvent{
		TrackID: 1, SampleNumber: 99, OrderIndex: 1,
		TimestampBits: 64, Timescale: uint32(model.Second),
		RawTimestamp: uint64(model.Second),
	}
	rec, err := timeline.RecomputeFromCheckpoint(cp, base, late)
	if err != nil {
		t.Fatal(err)
	}
	full, err := timeline.Build(append(append([]model.RawEvent(nil), base...), late), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !timeline.SameEventTimes(rec.Result, full) {
		t.Fatal("recomputed timeline differs from full ordered processing")
	}
}

func hexEncode(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, x := range b {
		out[2*i] = digits[x>>4]
		out[2*i+1] = digits[x&0xf]
	}
	return string(out)
}
