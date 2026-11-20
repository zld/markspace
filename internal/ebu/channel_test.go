package ebu

import (
	"testing"
)

func TestLayoutWeights(t *testing.T) {
	l, err := NewLayout([]string{"FL", "FR", "FC", "LFE", "BL", "BR"})
	if err != nil {
		t.Fatalf("NewLayout: %v", err)
	}
	got := map[string]float64{}
	for i, c := range l.Channels() {
		got[string(c)] = l.Weight(i)
	}
	want := map[string]float64{"FL": 1, "FR": 1, "FC": 1, "LFE": 0, "BL": 1.41, "BR": 1.41}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("weight(%s) = %v, want %v", k, got[k], v)
		}
	}
	if l.String() != "FL-FR-FC-LFE-BL-BR" {
		t.Errorf("String() = %q", l.String())
	}
}

func TestLFEExcludedFromSum(t *testing.T) {
	l, _ := NewLayout([]string{"FL", "FR", "LFE"})
	frame := []float64{0.5, 0.5, 100}
	if sum := l.SumSquaresWeighted(frame); sum != 0.5 {
		t.Errorf("SumSquaresWeighted = %v, want 0.5 (LFE excluded)", sum)
	}
}

func TestLayoutValidation(t *testing.T) {
	if _, err := NewLayout(nil); err == nil {
		t.Error("empty layout accepted")
	}
	if _, err := NewLayout([]string{"FL", "XX"}); err == nil {
		t.Error("unknown label accepted")
	}
	if _, err := NewLayout([]string{"FL", "FL"}); err == nil {
		t.Error("duplicate label accepted")
	}
	tooMany := []string{"FL", "FR", "FC", "LFE", "BL", "BR", "SL", "SR", "FC"}
	if _, err := NewLayout(tooMany); err == nil {
		t.Error(">8 channels accepted")
	}
}

func TestStandardLayoutsAreValid(t *testing.T) {
	for name, labels := range StandardLayouts {
		if _, err := NewLayout(labels); err != nil {
			t.Errorf("standard layout %s: %v", name, err)
		}
	}
}

func TestSumSquaresWeight(t *testing.T) {
	l, _ := NewLayout([]string{"FC"})
	if s := l.SumSquaresWeighted([]float64{0.25}); s != 0.0625 {
		t.Errorf("mono sum = %v, want 0.0625", s)
	}
}
