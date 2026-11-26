package timeline

import (
	"testing"

	"mediad/internal/model"
	"mediad/internal/scan"
)

func sampleScan() *scan.ScanResult {
	base90k := model.Rational{Num: 1, Den: 90000}
	base1k := model.Rational{Num: 1, Den: 1000}
	return &scan.ScanResult{
		SourceHash: "h1", SourcePath: "a.mp4",
		Tracks: []scan.TrackSummary{
			{ID: 1, Kind: scan.KindVideo, TimeBase: base90k},
		},
		Samples: []scan.Sample{
			{TrackID: 1, Tick: 0, DurTick: 90000, Offset: 100, Length: 50, IsSync: true},
			{TrackID: 1, Tick: 90000, DurTick: 90000, Offset: 150, Length: 60},
		},
		Events: []scan.TimedEvent{
			{TrackID: 0, Kind: scan.EventEmsg, Tick: 45000, DurTick: 0, Offset: 0,
				Length: 10, TimeBase: base90k, ID: "e1"},
			{TrackID: 1, Kind: scan.EventSEI, Tick: 500, DurTick: 100, Offset: 200,
				Length: 8, TimeBase: base1k, ID: "sei-1"},
		},
	}
}

func TestNormalizeExactTimeBase(t *testing.T) {
	n, err := Normalize(sampleScan())
	if err != nil {
		t.Fatal(err)
	}
	if len(n.Tracks) != 2 { // real track 1 plus virtual track 0
		t.Fatalf("tracks = %d", len(n.Tracks))
	}
	var vt *NormalizedTrack
	for i := range n.Tracks {
		if n.Tracks[i].ID == 1 {
			vt = &n.Tracks[i]
		}
	}
	if vt == nil || len(vt.Samples) != 2 {
		t.Fatalf("video track samples missing: %+v", n.Tracks)
	}
	// 90000 @ 90kHz = exactly 1 second.
	if vt.Samples[1].StartNanos != 1_000_000_000 {
		t.Fatalf("sample start = %d", vt.Samples[1].StartNanos)
	}
	// emsg 45000 @ 90kHz = 500 ms; SEI 500 @ 1kHz = 500 ms — aligned.
	var emsg *NormalizedEvent
	for i := range n.Tracks {
		for j := range n.Tracks[i].Events {
			if n.Tracks[i].Events[j].ID == "e1" {
				emsg = &n.Tracks[i].Events[j]
			}
		}
	}
	if emsg == nil || emsg.StartNanos != 500_000_000 {
		t.Fatalf("emsg normalize wrong: %+v", emsg)
	}
	// Original tick and time base must be retained on the event.
	if emsg.Tick != 45000 || emsg.TimeBase.Den != 90000 {
		t.Fatalf("event lost raw tick/base: %+v", emsg.TimedEvent)
	}
}

func TestNormalizeInRange(t *testing.T) {
	n, _ := Normalize(sampleScan())
	ss, evs := n.InRange(1, 0, 1_500_000_000)
	if len(ss) != 2 || len(evs) != 1 {
		t.Fatalf("range [0,1.5s): samples=%d events=%d", len(ss), len(evs))
	}
	// Narrow window that only intersects the first sample.
	ss2, _ := n.InRange(1, 0, 500_000_000)
	if len(ss2) != 1 {
		t.Fatalf("narrow window samples = %d", len(ss2))
	}
}

func TestNormalizeOverflowAnomaly(t *testing.T) {
	res := sampleScan()
	res.Samples = append(res.Samples, scan.Sample{
		TrackID: 1, Tick: 1 << 62, DurTick: 1, Offset: 999,
	})
	n, err := Normalize(res)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range n.Anomalies {
		if a.Type == model.AnomalyTimestampOverflow && a.SourceOffset == 999 && a.RawTick == 1<<62 {
			found = true
		}
	}
	if !found {
		t.Fatalf("overflow anomaly missing: %+v", n.Anomalies)
	}
}

func TestIndexDeterministicAndSensitive(t *testing.T) {
	n1, _ := Normalize(sampleScan())
	idx1, err := BuildIndex(n1)
	if err != nil {
		t.Fatal(err)
	}
	n2, _ := Normalize(sampleScan())
	idx2, _ := BuildIndex(n2)
	if idx1.MetadataHash != idx2.MetadataHash || idx1.CacheKey != idx2.CacheKey {
		t.Fatal("identical input must yield identical index hashes")
	}
	// A metadata change (same source hash) changes the metadata/cache digests.
	changed := sampleScan()
	changed.Events[0].Tick = 90000
	nc, _ := Normalize(changed)
	idxC, _ := BuildIndex(nc)
	if idxC.MetadataHash == idx1.MetadataHash {
		t.Fatal("metadata change must change metadata hash")
	}
	if idxC.SourceHash != idx1.SourceHash {
		t.Fatal("source hash must be unchanged for metadata-only edit")
	}
	// NeedsRebuild logic.
	if NeedsRebuild(idx1, idx2) {
		t.Fatal("equal indices must not rebuild")
	}
	if !NeedsRebuild(idx1, idxC) {
		t.Fatal("metadata change must require index rebuild")
	}
	if !NeedsRebuild(nil, idx1) {
		t.Fatal("missing cache must rebuild")
	}
}

func TestIndexParserVersionBound(t *testing.T) {
	n, _ := Normalize(sampleScan())
	idx, _ := BuildIndex(n)
	if idx.ParserVersion != scan.ParserVersion || idx.ParserVersion == "" {
		t.Fatalf("parser version not bound: %q", idx.ParserVersion)
	}
	upgraded := *idx
	upgraded.ParserVersion = "mediad-parser-2"
	if !NeedsRebuild(idx, &upgraded) {
		t.Fatal("parser upgrade must force rebuild")
	}
}
