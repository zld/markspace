package fixture

import (
	"os"
	"path/filepath"
	"testing"

	"mediacore/internal/demux"
	"mediacore/internal/model"
)

func TestGenerateDeterministic(t *testing.T) {
	d1, d2 := t.TempDir(), t.TempDir()
	s1, err := Generate(d1, 17)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := Generate(d2, 17)
	if err != nil {
		t.Fatal(err)
	}
	if len(s1) != len(s2) || len(s1) != 6 {
		t.Fatalf("samples: %d vs %d", len(s1), len(s2))
	}
	for i := range s1 {
		b1, _ := os.ReadFile(s1[i].Path)
		b2, _ := os.ReadFile(s2[i].Path)
		if len(b1) != len(b2) {
			t.Fatalf("%s: size %d != %d", s1[i].Name, len(b1), len(b2))
		}
		for j := range b1 {
			if b1[j] != b2[j] {
				t.Fatalf("%s: byte %d differs across runs", s1[i].Name, j)
			}
		}
	}
}

func TestGeneratedSamplesBehave(t *testing.T) {
	dir := t.TempDir()
	samples, err := Generate(dir, 17)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]string{}
	for _, s := range samples {
		byName[s.Name] = s.Path
	}
	// normal demuxes cleanly with all event kinds present.
	_, events, err := demux.DemuxMP4(byName["normal"], demux.Options{})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[model.EventKind]int{}
	for _, e := range events {
		kinds[e.Kind]++
	}
	for _, k := range []model.EventKind{model.EventMedia, model.EventSubtitle, model.EventID3, model.EventChapter, model.EventEMSG, model.EventSEI} {
		if kinds[k] == 0 {
			t.Fatalf("normal.mp4 missing %v events", k)
		}
	}
	// vfr: stts durations are not uniform.
	tracks, vevents, err := demux.DemuxMP4(byName["vfr"], demux.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_ = tracks
	seen := map[int64]bool{}
	for _, e := range vevents {
		if e.Kind == model.EventMedia {
			seen[e.Duration] = true
		}
	}
	if len(seen) < 3 {
		t.Fatalf("vfr durations too uniform: %v", seen)
	}
	// truncated and badlen fail at demux.
	for _, name := range []string{"truncated", "badlen"} {
		if _, _, err := demux.DemuxMP4(byName[name], demux.Options{}); err == nil {
			t.Fatalf("%s should fail demux", name)
		}
	}
	// wrap.rtp extends monotonically.
	_, revents, err := demux.DemuxRTP(byName["wrap"], demux.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(revents); i++ {
		if revents[i].Ticks <= revents[i-1].Ticks {
			t.Fatalf("wrap ticks not monotonic at %d", i)
		}
	}
	if len(revents) != 8 {
		t.Fatalf("wrap events = %d, want 8", len(revents))
	}
}

func TestFixtureFilesAreStableNames(t *testing.T) {
	dir := t.TempDir()
	samples, _ := Generate(dir, 1)
	for _, s := range samples {
		if filepath.Dir(s.Path) != dir {
			t.Fatalf("sample outside dir: %s", s.Path)
		}
	}
}
