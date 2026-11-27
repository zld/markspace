package manifest

import (
	"errors"
	"strings"
	"testing"

	"mediapipe/internal/model"
)

func sample() *Manifest {
	return &Manifest{
		Seed: 17,
		Assets: []Asset{{
			Name: "clip",
			Versions: []Version{
				{ID: "v360p", Tracks: []Track{{
					ID: "1", Kind: "video", Timescale: 90_000, EditMediaTime: 1_000,
					Ranges: []Range{
						{StartTicks: 0, DurationTicks: 90_000, Summary: "a"},
						{StartTicks: 90_000, DurationTicks: 45_000, Summary: "b"},
					},
				}}},
				{ID: "v720p", Tracks: []Track{{
					ID: "1", Kind: "video", Timescale: 90_000,
					Ranges: []Range{{StartTicks: 0, DurationTicks: 135_000, Summary: "ab"}},
				}}},
			},
		}},
	}
}

func TestParseValid(t *testing.T) {
	data, err := sample().Canonical()
	if err != nil {
		t.Fatal(err)
	}
	m, err := Parse(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(m.Assets) != 1 || len(m.Assets[0].Versions) != 2 {
		t.Fatalf("unexpected shape: %+v", m)
	}
}

func TestParseRejectsUnknownField(t *testing.T) {
	_, err := Parse([]byte(`{"assets":[{"name":"a","versions":[],"bogus":1}]}`))
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
}

func TestValidateRejectsOverlap(t *testing.T) {
	m := sample()
	m.Assets[0].Versions[0].Tracks[0].Ranges[1].StartTicks = 80_000
	if err := m.Validate(); err == nil {
		t.Fatal("expected overlap rejection")
	}
}

func TestValidateRejectsZeroTimescale(t *testing.T) {
	m := sample()
	m.Assets[0].Versions[0].Tracks[0].Timescale = 0
	err := m.Validate()
	if err == nil || !strings.Contains(err.Error(), "timescale") {
		t.Fatalf("want timescale error, got %v", err)
	}
}

func TestRangeSpanAppliesEditOffset(t *testing.T) {
	track := Track{Timescale: 90_000, EditMediaTime: 90_000}
	r := Range{StartTicks: 90_000, DurationTicks: 180_000}
	span, err := r.Span(track)
	if err != nil {
		t.Fatal(err)
	}
	if span.Start != 0 || span.End != model.Nanos(2*model.NanosPerSecond) {
		t.Fatalf("span = %v", span)
	}
	raw, err := r.RawTime(track)
	if err != nil {
		t.Fatal(err)
	}
	if raw.Ticks != 90_000 || raw.Timescale != 90_000 || raw.EditOffset != 90_000 {
		t.Fatalf("raw timestamp lost fields: %+v", raw)
	}
}

func TestSortedVersionsPriority(t *testing.T) {
	hi, lo := 10, 1
	a := Asset{Versions: []Version{
		{ID: "b"},
		{ID: "c", Priority: &lo},
		{ID: "a", Priority: &hi},
	}}
	got := a.SortedVersions()
	if got[0].ID != "a" || got[1].ID != "c" || got[2].ID != "b" {
		t.Fatalf("order = %v", []string{got[0].ID, got[1].ID, got[2].ID})
	}
	if got[0].HasPriority() == false || got[2].HasPriority() == true {
		t.Fatal("priority flags wrong")
	}
}

func TestCanonicalIsDeterministic(t *testing.T) {
	m := sample()
	first, err := m.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	// Reordering input collections must not change the canonical bytes.
	m.Assets[0].Versions[0], m.Assets[0].Versions[1] = m.Assets[0].Versions[1], m.Assets[0].Versions[0]
	second, err := m.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("canonical form changed:\n%s\n%s", first, second)
	}
}

func TestReadLimit(t *testing.T) {
	big := strings.Repeat(" ", 9<<20)
	_, err := Read(strings.NewReader(big))
	if err == nil {
		t.Fatal("expected size error")
	}
	var me *model.Error
	if !errors.As(err, &me) || me.Kind != model.KindInvalid {
		t.Fatalf("want invalid kind, got %v", err)
	}
}
