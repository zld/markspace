package timecode

import (
	"testing"

	"mediaaudit/internal/model"

	"github.com/pion/rtp"
)

func TestRTPSampleRoundTripThroughPion(t *testing.T) {
	p := &rtp.Packet{Header: rtp.Header{
		Version: 2, PayloadType: 96, SequenceNumber: 7, Timestamp: 4294967295, SSRC: 0x11223344,
	}, Payload: []byte{1, 2, 3}}
	raw, err := p.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var back rtp.Packet
	if err := back.Unmarshal(raw); err != nil {
		t.Fatal(err)
	}
	if RTPTimestamp(&back) != 4294967295 {
		t.Fatalf("timestamp survived marshal: %d", back.Timestamp)
	}
	if RTPTimestamp(nil) != 0 {
		t.Fatal("nil packet must read as zero without panicking")
	}
}

func TestExtendRTPStreamRejectsNil(t *testing.T) {
	if _, _, err := ExtendRTPStream([]*rtp.Packet{{Header: rtp.Header{Timestamp: 1}}, nil}); err == nil {
		t.Fatal("nil packet in the middle must be reported")
	}
}

func TestDriftNegativeDirection(t *testing.T) {
	track := model.Track{ID: "slow", Kind: model.KindRTP}
	samples := []DriftSample{
		{Media: model.RatFromInt(0), Ref: model.RatFromInt(0)},
		{Media: model.MustRat(1000-2, 1000), Ref: model.RatFromInt(1)},
	}
	rep, err := EstimateDrift(track, samples, DriftConfig{MaxPPM: 500})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Exceeded {
		t.Fatalf("-2000 ppm must exceed 500 ppm, drift=%s", rep.Drift)
	}
}

func TestDriftLimitDisabled(t *testing.T) {
	track := model.Track{ID: "x", Kind: model.KindRTP}
	samples := []DriftSample{{}, {Media: model.RatFromInt(2), Ref: model.RatFromInt(1)}}
	rep, err := EstimateDrift(track, samples, DriftConfig{MaxPPM: -1})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Exceeded {
		t.Fatal("negative MaxPPM disables the threshold")
	}
}

func TestUnwrapperWidths(t *testing.T) {
	u, err := NewUnwrapper(16)
	if err != nil {
		t.Fatal(err)
	}
	u.Extend(65530)
	got, wrapped, _ := u.Extend(3)
	if !wrapped || got != 65539 {
		t.Fatalf("16-bit wrap: got %d wrapped=%v", got, wrapped)
	}
	if _, err := NewUnwrapper(0); err == nil {
		t.Fatal("zero width must fail")
	}
	if _, err := NewUnwrapper(64); err == nil {
		t.Fatal("64-bit width must fail")
	}
}

func TestUnwrapperKeepsAnchorAcrossReordering(t *testing.T) {
	u, _ := NewUnwrapper(32)
	// A reordered packet must not advance the anchor: the next in-order
	// timestamp is 1000 forward from 10000, not 2000.
	for _, tc := range []struct {
		raw  uint64
		want uint64
	}{{10000, 10000}, {9000, 10000}, {11000, 11000}, {12000, 12000}} {
		got, _, _ := u.Extend(tc.raw)
		if got != tc.want {
			t.Fatalf("raw %d -> %d, want %d", tc.raw, got, tc.want)
		}
	}
	if u.Wraps() != 0 {
		t.Fatalf("reordering must not count as a wrap: %d", u.Wraps())
	}
}

func TestUnwrapperWrapAfterReordering(t *testing.T) {
	u, _ := NewUnwrapper(32)
	u.Extend(4294967290)
	u.Extend(4294967280) // reordered backwards
	got, wrapped, _ := u.Extend(4)
	if got != 4294967300 || !wrapped {
		t.Fatalf("wrap after reorder: got %d wrapped=%v", got, wrapped)
	}
}
