package manifest

import (
	"encoding/json"
	"testing"

	"github.com/local/tracecheck/internal/align"
	"github.com/local/tracecheck/internal/clock"
	"github.com/local/tracecheck/internal/mkv"
	"github.com/local/tracecheck/internal/rtptrace"
)

func fixtures() (*mkv.Document, *rtptrace.Trace, *align.Result) {
	doc := &mkv.Document{
		TimecodeScale: 1_000_000,
		Tracks:        []mkv.Track{{Number: 1, CodecID: "A_OPUS"}},
		Blocks: []*mkv.Block{
			{TrackNumber: 1, SourceOffset: 10, AbsTime: 0},
			{TrackNumber: 1, SourceOffset: 20, AbsTime: 1_000_000_000},
		},
		Anomalies: []mkv.TimeAnomaly{{
			Code: clock.CodeTimeDuplicate, TrackNumber: 1, RawTimecode: 5,
			AbsTime: clock.Tick(5_000_000), SourceOffset: 20,
		}},
	}
	trace := &rtptrace.Trace{
		Stats: map[uint64]*rtptrace.Stats{
			1: {TrackNumber: 1, PacketCount: 2, LostPackets: 1, MaxJitterNS: 7},
		},
	}
	result := &align.Result{
		Aligned:   2,
		MaxSkewNS: clock.Tick(3_000_000),
		Segments: []*align.Segment{
			{Index: 0, StartNS: 0, EndNS: 1_000_000_000, SkewNS: 0, OK: true,
				LayerCoverage: map[uint64]int{1: 1}, BlockSourceOffsets: []int64{10}},
			{Index: 1, StartNS: 1_000_000_000, EndNS: 2_000_000_000,
				SkewNS: clock.Tick(3_000_000), OK: true,
				LayerCoverage: map[uint64]int{1: 1}, BlockSourceOffsets: []int64{20}},
		},
	}
	return doc, trace, result
}

func TestBuildFields(t *testing.T) {
	doc, trace, result := fixtures()
	m := Build(Inputs{
		InputID:        "in",
		Doc:            doc,
		Trace:          trace,
		Result:         result,
		QueuePeak:      3,
		CacheHits:      1,
		SegmentContent: map[int]ContentRef{0: {SHA256: "h0", CacheKey: "k0"}},
	})
	if m.SegmentCount != 2 || m.AlignedCount != 2 || m.PacketLoss != 1 {
		t.Fatalf("counts wrong: %+v", m)
	}
	if m.QueuePeak != 3 || m.CacheHits != 1 {
		t.Fatalf("queue/cache fields wrong: %+v", m)
	}
	if m.MaxSkewNS != 3_000_000 {
		t.Fatalf("max skew = %d", m.MaxSkewNS)
	}
	if m.Segments[0].Status != "aligned" || m.Segments[0].ContentSHA256 != "h0" {
		t.Fatalf("segment 0 wrong: %+v", m.Segments[0])
	}
	if len(m.Anomalies) != 1 || m.Anomalies[0].Code != string(clock.CodeTimeDuplicate) {
		t.Fatalf("anomaly wrong: %+v", m.Anomalies)
	}
	if m.Tracks[0].LostPackets != 1 || m.Tracks[0].CodecID != "A_OPUS" {
		t.Fatalf("track report wrong: %+v", m.Tracks[0])
	}
	if m.ManifestSHA256 == "" {
		t.Fatal("manifest digest must be populated")
	}
}

func TestDigestDeterministic(t *testing.T) {
	doc, trace, result := fixtures()
	build := func() string {
		return Build(Inputs{InputID: "in", Doc: doc, Trace: trace, Result: result}).ManifestSHA256
	}
	first := build()
	second := build()
	if first != second {
		t.Fatal("repeated builds must produce identical manifest digest")
	}
	// A different input identity must change the digest.
	other := Build(Inputs{InputID: "in-CHANGED", Doc: doc, Trace: trace, Result: result}).ManifestSHA256
	if other == first {
		t.Fatal("manifest digest must change when input changes")
	}
}

func TestMarshalIsValidJSON(t *testing.T) {
	doc, trace, result := fixtures()
	m := Build(Inputs{InputID: "in", Doc: doc, Trace: trace, Result: result})
	raw, err := m.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("manifest is not valid JSON: %v", err)
	}
	if back["manifest_sha256"] != m.ManifestSHA256 {
		t.Fatal("serialized digest mismatch")
	}
}

func TestFailedSegmentMarked(t *testing.T) {
	doc := &mkv.Document{Tracks: []mkv.Track{{Number: 1}}, Blocks: []*mkv.Block{{TrackNumber: 1, AbsTime: 0}}}
	trace := &rtptrace.Trace{Stats: map[uint64]*rtptrace.Stats{1: {}}}
	result := &align.Result{
		Aligned: 0,
		Segments: []*align.Segment{{
			Index: 0, OK: false, FailCode: clock.CodeAlignmentSkew,
			LayerCoverage: map[uint64]int{1: 1},
		}},
		FailedRanges: [][2]int64{{0, 100}},
	}
	m := Build(Inputs{InputID: "in", Doc: doc, Trace: trace, Result: result})
	if m.Segments[0].Status != "failed" || m.Segments[0].FailCode != string(clock.CodeAlignmentSkew) {
		t.Fatalf("failed segment wrong: %+v", m.Segments[0])
	}
	if m.AlignedCount != 0 || len(m.FailedRanges) != 1 {
		t.Fatal("aligned count must exclude failed segments")
	}
}
