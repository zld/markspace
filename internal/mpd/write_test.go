package mpd

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dashcheck/internal/align"
	"dashcheck/internal/fixture"
	"dashcheck/internal/manifest"
	"dashcheck/internal/model"
)

func buildTestManifest(t *testing.T) *model.Manifest {
	t.Helper()
	res, err := align.Align(fixture.DemoDescription(), mustRat(t, "1/1000"), false)
	if err != nil {
		t.Fatalf("align: %v", err)
	}
	m, err := manifest.Build(fixture.DemoDescription().ContentID, res)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return m
}

func mustRat(t *testing.T, s string) model.Rat {
	t.Helper()
	r, err := model.ParseRat(s)
	if err != nil {
		t.Fatalf("parse rat %q: %v", s, err)
	}
	return r
}

func TestMarshalTimelineCoverageSumsAndNumbers(t *testing.T) {
	data, err := Marshal(buildTestManifest(t))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var doc xmlMPD
	if err := xml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal produced MPD: %v", err)
	}
	if doc.Type != "static" {
		t.Errorf("type = %q, want static", doc.Type)
	}
	if doc.MediaDur != "PT6S" {
		t.Errorf("mediaPresentationDuration = %q, want PT6S", doc.MediaDur)
	}
	if len(doc.Period.AdaptationSets) != 1 {
		t.Fatalf("adaptation sets = %d, want 1", len(doc.Period.AdaptationSets))
	}
	as := doc.Period.AdaptationSets[0]
	if as.ContentType != "video" || len(as.Representations) != 2 {
		t.Fatalf("unexpected adaptation set: %+v", as)
	}
	for _, rep := range as.Representations {
		var sum int64
		for _, s := range rep.Template.Timeline.Entries {
			sum += s.D
		}
		if sum != 6000 {
			t.Errorf("rep %s timeline sum = %d ticks, want 6000", rep.ID, sum)
		}
		if len(rep.Template.Timeline.Entries) != 4 {
			t.Errorf("rep %s entries = %d, want 4", rep.ID,
				len(rep.Template.Timeline.Entries))
		}
		if rep.Template.StartNumber != 1 || rep.Template.Timescale != 1000 {
			t.Errorf("rep %s template = %+v", rep.ID, rep.Template)
		}
	}
}

func TestMarshalRoundTripsExplicitTOnGap(t *testing.T) {
	m := buildTestManifest(t)
	rep := m.Period.AdaptationSets[0].Representations[0]
	// Force a gap before segment 3: its T must be emitted explicitly.
	rep.Template.Timeline[2].HasT = true
	rep.Template.Timeline[2].T = 4100
	data, err := Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, `t="4100"`) {
		t.Errorf("expected explicit t=4100 in MPD:\n%s", text)
	}
}

func TestWriteRejectsCoverageMismatch(t *testing.T) {
	m := buildTestManifest(t)
	rep := m.Period.AdaptationSets[0].Representations[0]
	// Corrupt the recorded coverage so the independent re-sum must catch it.
	rep.Template.CoverageTick = rep.Template.CoverageTick + 1
	_, err := Marshal(m)
	if err == nil {
		t.Fatal("expected coverage mismatch error")
	}
	me, ok := model.AsError(err)
	if !ok || me.Category != model.CategoryIO {
		t.Fatalf("expected IO-category error, got %v", err)
	}
}

func TestWriteRejectsNonPositiveDuration(t *testing.T) {
	m := buildTestManifest(t)
	m.Period.AdaptationSets[0].Representations[1].Template.Timeline[0].D = 0
	if _, err := Marshal(m); err == nil {
		t.Fatal("expected error for non-positive timeline duration")
	}
}

func TestWriteCreatesFileAndFailsOnDirectoryTarget(t *testing.T) {
	dir := t.TempDir()
	mpdPath := filepath.Join(dir, "nested", "out.mpd")
	if err := Write(buildTestManifest(t), mpdPath); err != nil {
		t.Fatalf("write: %v", err)
	}
	if info, err := os.Stat(mpdPath); err != nil || info.Size() == 0 {
		t.Fatalf("MPD not written: %v size info %v", err, info)
	}
	// A directory target cannot be opened as a file and must surface as
	// a stable IO error rather than a panic.
	dirTarget := filepath.Join(t.TempDir(), "adir.mpd")
	if err := os.MkdirAll(dirTarget, 0o755); err != nil {
		t.Fatal(err)
	}
	err := Write(buildTestManifest(t), dirTarget)
	me, ok := model.AsError(err)
	if !ok || me.Category != model.CategoryIO || me.Code != model.CodeWriteFailed {
		t.Fatalf("expected WRITE_FAILED IO error, got %v", err)
	}
}

func TestISODurationRational(t *testing.T) {
	cases := map[string]string{
		"6":      "PT6S",
		"1/2":    "PT0.5S",
		"1/3":    "PT0.333333333S",
		"72/10":  "PT7.2S",
		"1/1000": "PT0.001S",
	}
	for in, want := range cases {
		r := mustRat(t, in)
		if got := ISODuration(r); got != want {
			t.Errorf("ISODuration(%s) = %q, want %q", in, got, want)
		}
	}
}
