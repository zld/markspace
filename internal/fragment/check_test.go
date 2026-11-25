package fragment_test

import (
	"testing"

	pionrtp "github.com/pion/rtp"

	"mediacheck/internal/ebml"
	"mediacheck/internal/fragment"
	"mediacheck/internal/report"
	rtpcheck "mediacheck/internal/rtp"
	"mediacheck/internal/sample"
	"mediacheck/internal/tracks"
)

func setup(t *testing.T, name string) (*sample.Bundle, *ebml.Document, *tracks.Graph, *fragment.Result) {
	t.Helper()
	b, err := sample.Generate(name, 7)
	if err != nil {
		t.Fatal(err)
	}
	doc, serr := ebml.ParseDocument(b.EBML)
	if serr != nil {
		t.Fatalf("parse: %v", serr)
	}
	g, gerr := tracks.BuildGraph(b.EBML, doc)
	if gerr != nil {
		t.Fatalf("graph: %v", gerr)
	}
	g.Validate()
	streams, rerr := b.Streams()
	if rerr != nil {
		t.Fatalf("rtp streams: %v", rerr)
	}
	res, ferr := fragment.Check(doc, g, streams)
	if ferr != nil {
		t.Fatalf("fragment check: %v", ferr)
	}
	return b, doc, g, res
}

func TestNormalFragmentMappingAndDeviations(t *testing.T) {
	_, _, _, res := setup(t, sample.CaseNormal)
	if len(res.Fragments) != 1 {
		t.Fatalf("fragments=%d want 1", len(res.Fragments))
	}
	f := res.Fragments[0]
	if f.StartNS != 0 || f.EndNS != 66_000_000 {
		t.Fatalf("fragment time range=[%d,%d] want [0,66ms]", f.StartNS, f.EndNS)
	}
	if f.ByteLength != 191 {
		t.Fatalf("byte length=%d want stable 191", f.ByteLength)
	}

	var blocks []report.Record
	var fragmentRecord *report.Record
	var issues []report.Record
	for _, r := range res.Records {
		switch r.Type {
		case "block":
			blocks = append(blocks, r)
		case "fragment":
			cp := r
			fragmentRecord = &cp
		case "issue":
			issues = append(issues, r)
		}
	}
	if len(issues) != 0 {
		t.Fatalf("normal sample has issues: %+v", issues)
	}
	if len(blocks) != 6 {
		t.Fatalf("block mappings=%d want 6", len(blocks))
	}
	if fragmentRecord == nil || fragmentRecord.TimeDeltaNS != 66_000_000 || fragmentRecord.ByteDelta != 191 {
		t.Fatalf("bad fragment deviation record: %+v", fragmentRecord)
	}

	// Records are appended in raw input order; offsets are therefore increasing.
	for i := 1; i < len(blocks); i++ {
		if blocks[i].InputOffset <= blocks[i-1].InputOffset {
			t.Fatalf("block mapping order not offset-sorted at %d", i)
		}
		if blocks[i].ByteDelta != 30 {
			t.Fatalf("block %d byte_delta=%d want fixed element spacing 30", i, blocks[i].ByteDelta)
		}
	}
	audio := map[int64]int64{}
	video := map[int64]int64{}
	for _, r := range blocks {
		if r.Track == 2 {
			audio[r.TimeNS] = r.TimeDeltaNS
		}
		if r.Track == 1 {
			video[r.TimeNS] = r.TimeDeltaNS
		}
	}
	if audio[20_000_000] != 20_000_000 || audio[40_000_000] != 20_000_000 {
		t.Fatalf("audio same-track deltas wrong: %+v", audio)
	}
	if video[33_000_000] != 33_000_000 || video[66_000_000] != 33_000_000 {
		t.Fatalf("video same-track deltas wrong: %+v", video)
	}
}

func TestRTPGapCreatesOneRecoveryWithTimeRangeAndOffset(t *testing.T) {
	b, _, _, res := setup(t, sample.CaseRTPGap)
	if b.Digest == "" {
		t.Fatal("missing digest")
	}
	var recovered []report.Record
	for _, r := range res.Records {
		if r.Type == "recovery" {
			recovered = append(recovered, r)
		}
		if r.Type == "issue" {
			t.Fatalf("uniquely recoverable gap should not create fatal issue: %+v", r)
		}
	}
	if len(recovered) != 1 {
		t.Fatalf("recovery records=%d want 1", len(recovered))
	}
	rec := recovered[0]
	if rec.Sequence != 2001 || rec.MissingCount != 1 || rec.Track != 2 {
		t.Fatalf("recovery identity wrong: %+v", rec)
	}
	if rec.InputOffset != 248 {
		t.Fatalf("recovery input_offset=%d want EBML block 248", rec.InputOffset)
	}
	if rec.TimeStartNS != 0 || rec.TimeEndNS != 40_000_000 || rec.TimeDeltaNS != 40_000_000 {
		t.Fatalf("recovery time range/delta wrong: %+v", rec)
	}
	if rec.ByteDelta != 0 {
		t.Fatalf("byte_delta=%d want equal neighbor payload lengths 0", rec.ByteDelta)
	}
}

func TestCrossTrackCoverageFailure(t *testing.T) {
	_, doc, g, _ := setup(t, sample.CaseNormal)
	cluster := doc.Clusters[0]
	filtered := cluster.Blocks[:0]
	for _, b := range cluster.Blocks {
		if b.Track != 2 {
			filtered = append(filtered, b)
		}
	}
	cluster.Blocks = filtered

	res, err := fragment.Check(doc, g, nil)
	if err != nil {
		t.Fatal(err)
	}
	var coverage *report.Record
	for i := range res.Records {
		if res.Records[i].ErrorCode == report.FragmentCoverage {
			coverage = &res.Records[i]
		}
	}
	if coverage == nil || coverage.Track != 2 {
		t.Fatalf("expected track 2 coverage failure, records=%+v", res.Records)
	}
	if coverage.InputOffset != cluster.Element.Offset {
		t.Fatalf("coverage offset=%d want cluster %d", coverage.InputOffset, cluster.Element.Offset)
	}
}

func TestFragmentSpanLimitIsReported(t *testing.T) {
	_, doc, g, _ := setup(t, sample.CaseNormal)
	doc.Limits.MaxFragmentSpan = 10_000_000
	res, err := fragment.Check(doc, g, nil)
	if err != nil {
		t.Fatal(err)
	}
	var limit *report.Record
	for i := range res.Records {
		if res.Records[i].ErrorCode == report.FragmentLimit {
			limit = &res.Records[i]
		}
	}
	if limit == nil || limit.InputOffset != doc.Clusters[0].Element.Offset {
		t.Fatalf("expected fragment limit issue, got %+v", res.Records)
	}
}

func TestOversizedRTPGapFailsWholeSegment(t *testing.T) {
	_, doc, g, _ := setup(t, sample.CaseNormal)
	stream := rtpcheck.Stream{
		Track: 9, SSRC: 99, ClockRate: 90_000, FragmentIndex: 0,
		Packets: []pionrtp.Packet{
			{Header: pionrtp.Header{SequenceNumber: 1, Timestamp: 0}, Payload: []byte("a")},
			{Header: pionrtp.Header{SequenceNumber: 40, Timestamp: 900}, Payload: []byte("b")},
		},
	}
	res, err := fragment.Check(doc, g, []rtpcheck.Stream{stream})
	if err != nil {
		t.Fatal(err)
	}
	var discontinuity *report.Record
	for i := range res.Records {
		if res.Records[i].ErrorCode == report.RTPSequenceDiscontinuity {
			discontinuity = &res.Records[i]
		}
	}
	if discontinuity == nil {
		t.Fatalf("expected sequence discontinuity, records=%+v", res.Records)
	}
}
