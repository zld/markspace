package clock

import (
	"errors"
	"math"
	"testing"

	"mediacheck/internal/model"
)

func TestToMasterExact(t *testing.T) {
	cases := []struct {
		value, scale, want int64
	}{
		{48000, 48000, 90000}, // 1 s audio
		{4410, 44100, 9000},   // 0.1 s
		{1000, 1000, 90000},   // 1 s millisecond scale
		{0, 90000, 0},
		{90, 90000, 90},         // 1 ms
		{240000, 24000, 900000}, // 10 s video
	}
	for _, c := range cases {
		got, err := ToMaster(c.value, c.scale)
		if err != nil || got != c.want {
			t.Errorf("ToMaster(%d,%d) = %d,%v want %d", c.value, c.scale, got, err, c.want)
		}
	}
}

func TestToMasterRejects(t *testing.T) {
	if _, err := ToMaster(1, 0); !errors.Is(err, model.ErrZeroTimescale) {
		t.Fatalf("zero timescale: got %v", err)
	}
	if _, err := ToMaster(1, -9); !errors.Is(err, model.ErrZeroTimescale) {
		t.Fatalf("negative timescale: got %v", err)
	}
	// 44100 -> 90000 is not integral for a single unit: must refuse lossy.
	if _, err := ToMaster(1, 44100); !errors.Is(err, model.ErrLossyTime) {
		t.Fatalf("lossy conversion: got %v", err)
	}
	if _, err := ToMaster(math.MaxInt64, 1); !errors.Is(err, model.ErrOverflow) {
		t.Fatalf("overflow: got %v", err)
	}
	if _, err := ToMaster(-5, 90000); !errors.Is(err, model.ErrLossyTime) {
		t.Fatalf("negative value: got %v", err)
	}
}

func TestFromSeconds(t *testing.T) {
	good := map[string]int64{
		"6.0":   540000,
		"1":     90000,
		"0.96":  86400,
		"0.001": 90,
	}
	for in, want := range good {
		got, err := FromSeconds(in)
		if err != nil || got != want {
			t.Errorf("FromSeconds(%q) = %d,%v want %d", in, got, err, want)
		}
	}
	bad := []string{"", ".", "6.", "abc", "1.2.3", "0.0000000001", "99999999999999999999"}
	for _, in := range bad {
		if _, err := FromSeconds(in); err == nil {
			t.Errorf("FromSeconds(%q) expected error", in)
		}
	}
}

func TestTimelineAdvanceAndDiscontinuity(t *testing.T) {
	tl := NewTimeline()
	start, err := tl.Advance(90000)
	if err != nil || start != 0 || tl.Cursor() != 90000 {
		t.Fatalf("advance: %d %d %v", start, tl.Cursor(), err)
	}
	r := tl.MarkDiscontinuity()
	if r.Start != 90000 || len(tl.Discontinuities()) != 1 {
		t.Fatalf("discontinuity record wrong: %+v", r)
	}
	if _, err := tl.Advance(-1); err == nil {
		t.Fatal("negative advance must fail")
	}
	if _, err := tl.Advance(math.MaxInt64); !errors.Is(err, model.ErrOverflow) {
		t.Fatalf("overflow advance: %v", err)
	}
}
