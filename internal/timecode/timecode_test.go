package timecode

import (
	"testing"

	"mediaaudit/internal/model"

	"github.com/pion/rtp"
)

func TestParseAllModes(t *testing.T) {
	cases := []struct {
		mode Mode
		in   string
		want string
	}{
		{Mode24, "01:00:00:23", "86423/24"},
		{Mode25, "00:00:00:24", "24/25"},
		{Mode2997NDF, "00:00:01:00", "1001/1000"},
		{Mode2997DF, "00:01:00;02", "3003/50"},
		{Mode50, "00:00:02:00", "2"},
	}
	for _, c := range cases {
		tc, err := Parse(c.in, c.mode, model.InputPos{File: "f", Line: 1})
		if err != nil {
			t.Fatalf("%s %s: %v", c.mode.Name, c.in, err)
		}
		if got := tc.Time.String(); got != "0/" && got != c.want {
			t.Fatalf("%s %s = %s, want %s", c.mode.Name, c.in, got, c.want)
		}
	}
}

func TestParseExactRationals(t *testing.T) {
	tc, err := Parse("00:00:01:00", Mode2997NDF, model.InputPos{})
	if err != nil {
		t.Fatal(err)
	}
	if !tc.Time.Equal(model.MustRat(1001, 1000)) {
		t.Fatalf("29.97 second = %s, want 1001/1000", tc.Time)
	}
	df, err := Parse("00:01:00;02", Mode2997DF, model.InputPos{})
	if err != nil {
		t.Fatal(err)
	}
	ndf, err := Parse("00:01:00:00", Mode2997NDF, model.InputPos{})
	if err != nil {
		t.Fatal(err)
	}
	if !df.Time.Equal(ndf.Time) {
		t.Fatalf("DF 00:01:00;02 (%s) must equal NDF 00:01:00:00 (%s)", df.Time, ndf.Time)
	}
	if df.Total != 1800 || ndf.Total != 1800 {
		t.Fatalf("frame counts: df=%d ndf=%d want 1800/1800", df.Total, ndf.Total)
	}
}

func TestInvalidFrameNumberCarriesPosition(t *testing.T) {
	pos := model.InputPos{File: "tc.txt", Line: 4, Column: 10}
	_, err := Parse("00:00:00:24", Mode24, pos)
	if err == nil {
		t.Fatal("frame 24 must be invalid for 24 fps")
	}
	e, ok := err.(*model.Error)
	if !ok || e.Code != model.CodeInvalidFrameNumber {
		t.Fatalf("want invalid_frame_number, got %v", err)
	}
	if e.Pos.Line != 4 || e.Pos.Column != 19 {
		t.Fatalf("position wrong: %+v", e.Pos)
	}
	if e.Recoverable() != true {
		t.Fatal("bad frame number must stay recoverable")
	}
}

func TestDropFrameDroppedLabelsRejected(t *testing.T) {
	for _, in := range []string{"00:01:00;00", "00:01:00;01"} {
		_, err := Parse(in, Mode2997DF, model.InputPos{Line: 1})
		if err == nil {
			t.Fatalf("%s must be rejected as a dropped label", in)
		}
		if e := err.(*model.Error); e.Code != model.CodeInvalidFrameNumber {
			t.Fatalf("%s: code %s", in, e.Code)
		}
	}
	if _, err := Parse("00:10:00;00", Mode2997DF, model.InputPos{}); err != nil {
		t.Fatalf("minute 10 may use ;00: %v", err)
	}
	if _, err := Parse("00:00:00;00", Mode2997DF, model.InputPos{}); err != nil {
		t.Fatalf("minute 0 may use ;00: %v", err)
	}
}

func TestSeparatorMustMatchMode(t *testing.T) {
	if _, err := Parse("00:00:00;05", Mode2997NDF, model.InputPos{}); err == nil {
		t.Fatal("';' in NDF must fail")
	}
	if _, err := Parse("00:00:00:05", Mode2997DF, model.InputPos{}); err == nil {
		t.Fatal("':' in DF must fail")
	}
}

func TestFromFramesInverse(t *testing.T) {
	for _, mode := range []Mode{Mode24, Mode25, Mode2997NDF, Mode2997DF, Mode50} {
		for _, f := range []int64{0, 1, 23, 1799, 1800, 17982, 18000, 179820, 1000000} {
			tc, err := FromFrames(f, mode)
			if err != nil {
				t.Fatalf("%s frames %d: %v", mode.Name, f, err)
			}
			back, err := Parse(tc.String(), mode, model.InputPos{})
			if err != nil {
				t.Fatalf("%s frames %d -> %q: %v", mode.Name, f, tc.String(), err)
			}
			if back.Total != f {
				t.Fatalf("%s frames %d -> %q -> %d", mode.Name, f, tc.String(), back.Total)
			}
			if !back.Time.Equal(tc.Time) {
				t.Fatalf("%s frames %d time mismatch %s vs %s", mode.Name, f, tc.Time, back.Time)
			}
		}
	}
}

func TestParseGarbage(t *testing.T) {
	bad := []string{"", "1:2:3", "aa:bb:cc:dd", "00:00:00:00:00", "00:60:00:00", "00:00:61:00", "-00:00:00:01x"}
	for _, in := range bad {
		if _, err := Parse(in, Mode25, model.InputPos{Line: 2}); err == nil {
			t.Fatalf("%q must fail", in)
		}
	}
}

func TestUnwrapRTPRollover(t *testing.T) {
	u, err := NewUnwrapper(32)
	if err != nil {
		t.Fatal(err)
	}
	raw := []uint64{0xFFFFFF00, 0xFFFFFFF0, 0x00000010, 0x00000020}
	want := []uint64{0xFFFFFF00, 0xFFFFFFF0, 0x100000010, 0x100000020}
	for i, r := range raw {
		got, wrapped, _ := u.Extend(r)
		if got != want[i] {
			t.Fatalf("step %d: got %x want %x", i, got, want[i])
		}
		if wrapped != (i == 2) {
			t.Fatalf("step %d wrapped=%v", i, wrapped)
		}
	}
	if u.Wraps() != 1 {
		t.Fatalf("wraps=%d", u.Wraps())
	}
}

func TestUnwrapIgnoresReorderedHalfRange(t *testing.T) {
	u, _ := NewUnwrapper(32)
	u.Extend(1000)
	got, wrapped, _ := u.Extend(900) // small backwards reorder
	if got != 1000 || wrapped {
		t.Fatalf("reorder must hold the clock: got %d wrapped=%v", got, wrapped)
	}
}

func TestExtendRTPStreamUsesPion(t *testing.T) {
	pkts := []*rtp.Packet{
		{Header: rtp.Header{Timestamp: 4294967290}},
		{Header: rtp.Header{Timestamp: 4294967295}},
		{Header: rtp.Header{Timestamp: 5}},
		{Header: rtp.Header{Timestamp: 10}},
	}
	ext, wraps, err := ExtendRTPStream(pkts)
	if err != nil {
		t.Fatal(err)
	}
	if len(wraps) != 1 || wraps[0] != 2 {
		t.Fatalf("wrap positions %v", wraps)
	}
	if ext[2] != 4294967301 || ext[3] != 4294967306 {
		t.Fatalf("extended values %v", ext)
	}
}

func TestDriftEstimation(t *testing.T) {
	track := model.Track{ID: "cam", Kind: model.KindRTP}
	// Media runs 1000 ppm fast against the reference clock.
	var samples []DriftSample
	for i := int64(0); i <= 100; i++ {
		ref := model.RatFromInt(i)
		media := model.MustRat(i*1001, 1000)
		samples = append(samples, DriftSample{Media: media, Ref: ref})
	}
	rep, err := EstimateDrift(track, samples, DriftConfig{MaxPPM: 500})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Exceeded {
		t.Fatalf("expected drift to exceed 500 ppm, got %s", rep.Drift)
	}
	if e := rep.DriftError(); e == nil || e.Code != model.CodeDriftExceeded || e.Track != "cam" {
		t.Fatalf("drift error not classified: %v", e)
	}
	ok, err := EstimateDrift(track, samples, DriftConfig{MaxPPM: 2000})
	if err != nil {
		t.Fatal(err)
	}
	if ok.Exceeded {
		t.Fatal("2000 ppm limit must accept 1000 ppm drift")
	}
	if ok.DriftError() != nil {
		t.Fatal("accepted drift must not produce an error")
	}
	// Correction of the fast media clock pulls the last point back.
	media := []model.Rat{model.RatFromInt(0), model.MustRat(100100, 1000)}
	corr, err := CorrectTimeline(media, rep)
	if err != nil {
		t.Fatal(err)
	}
	if !corr[1].Equal(model.RatFromInt(100)) {
		t.Fatalf("corrected last = %s want 100", corr[1])
	}
}

func TestDriftDegenerate(t *testing.T) {
	track := model.Track{ID: "x", Kind: model.KindRTP}
	if _, err := EstimateDrift(track, []DriftSample{{}}, DriftConfig{}); err == nil {
		t.Fatal("single sample must fail")
	}
	same := []DriftSample{{Media: model.RatFromInt(1), Ref: model.RatFromInt(5)}, {Media: model.RatFromInt(2), Ref: model.RatFromInt(5)}}
	if _, err := EstimateDrift(track, same, DriftConfig{}); err == nil {
		t.Fatal("identical reference times must fail")
	}
}
