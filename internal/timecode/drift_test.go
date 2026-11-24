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
