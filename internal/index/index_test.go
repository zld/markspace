package index_test

import (
	"path/filepath"
	"strings"
	"testing"

	"mediacheck/internal/fixture"
	"mediacheck/internal/index"
	"mediacheck/internal/manifest"
	"mediacheck/internal/timeline"
)

func loadEvents(t *testing.T, dir string) []manifest.Event {
	t.Helper()
	var events []manifest.Event
	for _, p := range []string{"fixtures/hls/playlist.m3u8", "fixtures/dash/manifest.mpd"} {
		full := filepath.Join(dir, p)
		var evs []manifest.Event
		var err error
		if strings.HasSuffix(p, ".m3u8") {
			evs, err = manifest.ParseHLS(full)
		} else {
			evs, err = manifest.ParseDASH(full)
		}
		if err != nil {
			t.Fatal(err)
		}
		for i := range evs {
			evs[i].ManifestPath, _ = filepath.Rel(dir, evs[i].ManifestPath)
			evs[i].SegmentPath, _ = filepath.Rel(dir, evs[i].SegmentPath)
		}
		events = append(events, evs...)
	}
	return events
}

func build(t *testing.T, c fixture.Case) (string, *index.Index) {
	t.Helper()
	dir := t.TempDir()
	if err := fixture.Generate(dir, c); err != nil {
		t.Fatal(err)
	}
	entries, err := index.MapEvents(loadEvents(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	ix, err := index.Assemble(dir, entries)
	if err != nil {
		t.Fatal(err)
	}
	return dir, ix
}

func TestAssembleNormal(t *testing.T) {
	_, ix := build(t, fixture.CaseNormal)
	if len(ix.Entries) != 6 || len(ix.Anomalies) != 0 {
		t.Fatalf("entries=%d anomalies=%d, want 6/0", len(ix.Entries), len(ix.Anomalies))
	}
	if ix.MediaStartNs != 0 || ix.MediaEndNs != 3_000_000_000 {
		t.Fatalf("media range = [%d,%d)", ix.MediaStartNs, ix.MediaEndNs)
	}
	for _, e := range ix.Entries {
		if e.Anomalous || e.SegmentDigest == "" || e.SampleSize == 0 {
			t.Fatalf("entry %+v not fully resolved", e)
		}
	}
}

func TestAssembleTruncated(t *testing.T) {
	_, ix := build(t, fixture.CaseTruncated)
	if len(ix.Anomalies) != 1 {
		t.Fatalf("anomalies = %+v, want exactly 1", ix.Anomalies)
	}
	a := ix.Anomalies[0]
	if a.Kind != timeline.KindTruncated || a.Source != "fixtures/dash/chunk1.m4s" {
		t.Fatalf("anomaly = %+v", a)
	}
	if a.StartNs != 1_000_000_000 || a.EndNs != 2_000_000_000 {
		t.Fatalf("anomaly range = [%d,%d)", a.StartNs, a.EndNs)
	}
}

func TestMapEventsBackward(t *testing.T) {
	dir := t.TempDir()
	if err := fixture.Generate(dir, fixture.CaseBackward); err != nil {
		t.Fatal(err)
	}
	_, err := index.MapEvents(loadEvents(t, dir))
	if te, ok := timeline.As(err); !ok || te.Kind != timeline.KindBackward {
		t.Fatalf("got %v, want backward_time", err)
	}
}

func TestRead(t *testing.T) {
	_, ix := build(t, fixture.CaseNormal)
	res := ix.Read(0, 2_000_000_000)
	if res.Status != index.StatusOK || len(res.Hits) != 4 {
		t.Fatalf("read [0,2s) = %s with %d hits, want ok/4", res.Status, len(res.Hits))
	}
	if res.FirstBoundaryNs != 0 || res.LastBoundaryNs != 2_000_000_000 {
		t.Fatalf("boundaries = %d..%d", res.FirstBoundaryNs, res.LastBoundaryNs)
	}
	// left-closed right-open: an entry ending exactly at `from` is excluded
	if res := ix.Read(1_000_000_000, 2_000_000_000); len(res.Hits) != 2 {
		t.Fatalf("read [1s,2s) hits = %d, want 2", len(res.Hits))
	}
	if res := ix.Read(5, 5); res.Status != index.StatusEmptyRange {
		t.Fatalf("empty range status = %s", res.Status)
	}
	if res := ix.Read(10_000_000_000, 11_000_000_000); res.Status != index.StatusOutOfMedia {
		t.Fatalf("out of media status = %s", res.Status)
	}
}
