package demux

import (
	"testing"

	"mediapipeline/internal/fault"
	"mediapipeline/internal/fixture"
	"mediapipeline/internal/metadata"
)

func TestParseMP4TracksAndEvents(t *testing.T) {
	p := fixture.BuildValid(fixture.Options{EventDuplicates: true})
	media, err := ParseMP4(p.Data, Options{SampleEvents: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(media.Tracks) != 5 {
		t.Fatalf("tracks: %d", len(media.Tracks))
	}
	wantTimescale := map[uint32]uint32{1: 90000, 2: 48000, 3: 1000, 4: 1000, 5: 1000}
	for _, tr := range media.Tracks {
		if tr.Timescale != wantTimescale[tr.ID] {
			t.Fatalf("track %d timescale %d", tr.ID, tr.Timescale)
		}
		if len(tr.Samples) == 0 {
			t.Fatalf("track %d has no samples", tr.ID)
		}
		for _, s := range tr.Samples {
			if int(s.Offset)+int(s.Size) > len(p.Data) || len(s.Data) != int(s.Size) {
				t.Fatalf("track %d sample out of range", tr.ID)
			}
		}
	}
	kinds := map[metadata.Kind]int{}
	for _, e := range media.Events {
		kinds[e.Kind]++
	}
	for _, k := range []metadata.Kind{metadata.KindSubtitle, metadata.KindChapter, metadata.KindID3, metadata.KindSEI, metadata.KindEmsg} {
		if kinds[k] == 0 {
			t.Fatalf("missing %s event: %v", k, kinds)
		}
	}
	merged, err := metadata.Merge(media.Events)
	if err != nil {
		t.Fatal(err)
	}
	if merged.DuplicateCount != 1 {
		t.Fatalf("duplicates: %d", merged.DuplicateCount)
	}
}

func TestTruncatedBoxReportsFirstMissingByte(t *testing.T) {
	valid := fixture.BuildValid(fixture.Options{})
	p := fixture.BuildTruncatedBox(fixture.Options{})
	_, err := ParseMP4(p.Data, Options{SampleEvents: true})
	fe, ok := fault.As(err)
	if !ok {
		t.Fatalf("expected staged fault, got %v", err)
	}
	want := int64(len(p.Data))
	if fe.InputOffset != want || fe.Operation != fault.OpBoxOverrun {
		t.Fatalf("offset=%d op=%s want %d", fe.InputOffset, fe.Operation, want)
	}
	if fe.InputOffset < int64(len(p.Data))-int64(len(valid.Data))/2 {
		t.Fatalf("offset is not near the cut: %d", fe.InputOffset)
	}
}

func TestMissingIndexReportsFirstFragment(t *testing.T) {
	p := fixture.BuildMissingIndex(fixture.Options{})
	_, err := ParseMP4(p.Data, Options{SampleEvents: true})
	fe, ok := fault.As(err)
	if !ok || fe.Operation != fault.OpIndexIncomplete {
		t.Fatalf("expected index fault, got %v", err)
	}
	if p.Damage != nil && fe.InputOffset != p.Damage.Offset {
		t.Fatalf("offset=%d want %d", fe.InputOffset, p.Damage.Offset)
	}
}

func TestSequentialScanRecoversWithoutIndex(t *testing.T) {
	p, _ := fixture.BuildRecoverable(fixture.Options{})
	scan, err := ScanFragments(p.Data, 0, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Segments) == 0 {
		t.Fatal("no segments recovered")
	}
	if len(scan.Incomplete) != 1 || scan.Incomplete[0].Start != int64(len(p.Data)) {
		t.Fatalf("incomplete range: %+v (len=%d)", scan.Incomplete, len(p.Data))
	}
	ids := scan.SegmentIDs()
	for i := 1; i < len(ids); i++ {
		if ids[i] != ids[i-1]+1 {
			t.Fatalf("segment ids not increasing: %v", ids)
		}
	}
}
