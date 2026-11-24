package verify

import (
	"testing"

	"mediarc/internal/model"
)

func TestPercentile95NearestRank(t *testing.T) {
	values := []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 100}
	// ceil(0.95*10)=10, so nearest-rank P95 is the maximum.
	if got := Percentile95NearestRank(values); got != 100 {
		t.Fatalf("p95 = %d, want 100", got)
	}
	hundred := make([]int64, 100)
	for i := range hundred {
		hundred[i] = int64(i + 1)
	}
	// ceil(95)=95 -> 95.
	if got := Percentile95NearestRank(hundred); got != 95 {
		t.Fatalf("p95 = %d, want 95", got)
	}
}

func TestAVSyncDriftUsesTimeBases(t *testing.T) {
	v := []model.Sample{{PTS: 0}, {PTS: 1000}, {PTS: 2000}} // 0, 33.33, 66.67ms at 30k
	a := []model.Sample{{PTS: 0}, {PTS: 1600}, {PTS: 3200}} // 0, 33.33, 66.67ms at 48k
	drift := AVSyncDriftMS(v, model.TimeBase{Num: 1, Den: 30000}, a, model.TimeBase{Num: 1, Den: 48000})
	if drift != 0 {
		t.Fatalf("drift = %dms, want 0", drift)
	}
}

func TestColorConflictFields(t *testing.T) {
	tr := model.Track{Color: &model.ColorMeta{Primaries: 1, Transfer: 1, Matrix: 1}}
	tr.ColorAlternatives = []model.ColorMeta{{Primaries: 9, Transfer: 1, Matrix: 1}}
	fields := ColorConflictFields(tr)
	if len(fields) != 1 || fields[0] != "color_primaries" {
		t.Fatalf("fields = %v", fields)
	}
}
