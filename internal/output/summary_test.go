package output

import (
	"bytes"
	"strings"
	"testing"

	"replayproof/internal/index"
	"replayproof/internal/media"
	"replayproof/internal/repair"
)

func sampleSummary() *Summary {
	s := &Summary{Seed: 7, Workers: 3, QueueSize: 2, QueuePeak: 2, ResumeEqual: true}
	s.Maps = []SampleMap{
		{TrackID: 1, Number: 1, Source: media.FromMicros(0), Duration: media.FromMicros(33333), ClipName: "clip-01.mp4", ClipTime: media.FromMicros(0), SourceOffset: 100, ClipOffset: 52, Size: 64, Keyframe: true},
		{TrackID: 2, Number: 1, Source: media.FromMicros(0), Duration: media.FromMicros(21333), ClipName: "clip-01.mp4", ClipTime: media.FromMicros(0), SourceOffset: 500, ClipOffset: 116, Size: 32, Keyframe: true},
	}
	s.Clips = []*Clip{{
		Spec:  ClipSpec{EventIndex: 1, Start: media.FromMicros(0), End: media.FromMicros(100000)},
		Path:  "clip-01.mp4",
		Bytes: 100,
		Hash:  strings.Repeat("a", 64),
	}}
	s.BadCases = []BadCase{{Name: "truncated.mp4", Code: media.CodeTruncatedBox, Offset: 16, Detail: "mdat"}}
	s.Fixes = []repair.TimestampFix{{TrackID: 1, SampleNumber: 3, Kind: repair.FixDuplicate,
		BeforeDTS: media.FromMicros(66667), AfterDTS: media.FromMicros(99999)}}
	s.Metadata = []MetadataRecord{{Field: "rotation", Source: "mp4", Value: "90", Candidates: []string{"mxf=180", "mp4=90"}}}
	s.ComputeErrors()
	return s
}

func TestSummaryCanonicalIsStable(t *testing.T) {
	a := sampleSummary()
	b := sampleSummary()
	if !bytes.Equal(a.Canonical(), b.Canonical()) {
		t.Fatal("canonical form differs for identical summaries")
	}
	if a.Digest() != b.Digest() || len(a.Digest()) != 64 {
		t.Fatalf("digest=%q", a.Digest())
	}
}

func TestSummaryDigestChangesWithData(t *testing.T) {
	a := sampleSummary()
	b := sampleSummary()
	b.Maps[0].TimeError = 5
	if a.Digest() == b.Digest() {
		t.Fatal("digest must cover sample maps")
	}
	c := sampleSummary()
	c.QueuePeak = 1
	if a.Digest() != c.Digest() {
		t.Fatal("queue peak changes metrics but should not change the digest")
	}
}

func TestSummaryMetricsAndText(t *testing.T) {
	s := sampleSummary()
	if s.MaxTimeErr != 0 || s.ByteErrors != 0 {
		t.Fatalf("errors=%d/%d", s.MaxTimeErr, s.ByteErrors)
	}
	var buf bytes.Buffer
	if err := s.WriteText(&buf); err != nil {
		t.Fatal(err)
	}
	text := buf.String()
	for _, want := range []string{
		"map track=1 n=01 src_us=0",
		"map track=2 n=01",
		"bad name=truncated.mp4 code=TRUNCATED_BOX offset=16",
		"fix track=1 n=3 kind=duplicate",
		"meta field=rotation source=mp4 value=90",
		"map_count=2 clips=1 max_time_error_us=0 byte_error=0 bad_cases=1 queue_peak=2 resume_digest_equal=true",
		"digest_sha256=",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("text missing %q:\n%s", want, text)
		}
	}
}

func indexEntry(dts, end media.Rational) index.Entry {
	return index.Entry{TrackID: 1, Number: 1, DTS: dts, End: end, PTS: dts, Offset: 10, Size: 20, Keyframe: true}
}

func TestMapFromEntryTimeError(t *testing.T) {
	e := indexEntry(media.FromMicros(1000), media.FromMicros(2000))
	m := MapFromEntry(e, "clip-01.mp4", media.FromMicros(900), 52, media.FromMicros(100))
	if m.TimeError != 0 {
		t.Fatalf("time error=%d", m.TimeError)
	}
	m = MapFromEntry(e, "clip-01.mp4", media.FromMicros(950), 52, media.FromMicros(100))
	if m.TimeError != 50 {
		t.Fatalf("time error=%d want 50", m.TimeError)
	}
}
