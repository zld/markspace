package clock

import "testing"

func TestConvertExact(t *testing.T) {
	out, err := Convert(90000, TBMedia, TBMilli)
	if out != 1000 || err != 0 {
		t.Fatalf("got %d,%v want 1000,0", out, err)
	}
}

func TestConvertFloors(t *testing.T) {
	// 1 media tick = 1/90 ms -> floors to 0 with 1/90 error.
	out, err := Convert(1, TBMedia, TBMilli)
	if out != 0 {
		t.Fatalf("got %d want 0", out)
	}
	if err <= 0 || err >= 1 {
		t.Fatalf("err %v outside [0,1)", err)
	}
	if diff := err - 1.0/90.0; diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("err %v want ~1/90", err)
	}
}

func TestConvertNegativeFloors(t *testing.T) {
	out, err := Convert(-1, TBMedia, TBMilli)
	if out != -1 {
		t.Fatalf("got %d want -1 (floor)", out)
	}
	if err < 0 || err >= 1 {
		t.Fatalf("err %v outside [0,1)", err)
	}
}

func TestConvertZeroTimebase(t *testing.T) {
	out, err := Convert(5, 0, TBMilli)
	if out != 0 || err != 0 {
		t.Fatalf("zero timebase must be a no-op, got %d,%v", out, err)
	}
}

func TestAlignerTracksMax(t *testing.T) {
	var a Aligner
	a.Observe(0.25)
	a.Observe(0.9)
	a.Observe(0.5)
	if a.Max() != 0.9 {
		t.Fatalf("max %v want 0.9", a.Max())
	}
	if a.Count() != 3 {
		t.Fatalf("count %d want 3", a.Count())
	}
	if a.Max() >= 1 {
		t.Fatal("alignment error must stay below 1 tick")
	}
}
