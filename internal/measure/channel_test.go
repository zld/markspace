package measure

import "testing"

func TestChannelWeights(t *testing.T) {
	w, err := ChannelWeights(6)
	if err != nil {
		t.Fatal(err)
	}
	if len(w) != 6 || w[3] != 0 {
		t.Fatalf("5.1 weights wrong: %v", w)
	}
	if w[4] != surroundWeight || w[5] != surroundWeight {
		t.Errorf("surround weights wrong: %v", w)
	}
	for _, n := range []int{1, 2} {
		if _, err := ChannelWeights(n); err != nil {
			t.Errorf("%d channels: %v", n, err)
		}
	}
	for _, n := range []int{0, 3, 4, 5, 7, 8, 64} {
		if _, err := ChannelWeights(n); err == nil {
			t.Errorf("%d channels: expected unknown-layout error", n)
		}
	}
}

func TestParseWeights(t *testing.T) {
	w, err := ParseWeights("1, 1, 1, 0, 1.41, 1.41")
	if err != nil || len(w) != 6 || w[4] != 1.41 {
		t.Fatalf("valid parse failed: %v %v", w, err)
	}
	for _, s := range []string{"1,-1", "1,NaN", "1,Inf", "abc", "", "1,"} {
		if _, err := ParseWeights(s); err == nil {
			t.Errorf("%q: expected error", s)
		}
	}
}
