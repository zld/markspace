package bitstream

import (
	"encoding/json"
	"testing"

	"mediacore/internal/fault"
)

func testStream() *Stream {
	return &Stream{
		Codec: CodecH264, Size: 1000,
		Frames: []Frame{
			{Index: 0, Offset: 0, Size: 400, Keyframe: true},
			{Index: 1, Offset: 400, Size: 300},
			{Index: 2, Offset: 700, Size: 300, Keyframe: true},
		},
	}
}

func TestValidateIndexAcceptsCleanRows(t *testing.T) {
	idx := &Index{Version: IndexVersion, Codec: CodecH264, Timescale: 90000, Entries: []IndexEntry{
		{Offset: 0, Size: 400, Timestamp: 0, Keyframe: true},
		{Offset: 400, Size: 300, Timestamp: 3000},
		{Offset: 700, Size: 300, Timestamp: 6000, Keyframe: true},
	}}
	report, err := ValidateIndex(testStream(), idx)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Accepted) != 3 || report.QuarantinedCount() != 0 {
		t.Fatalf("report = %+v", report)
	}
	keys := report.AcceptedKeyframeOffsets()
	if len(keys) != 2 || keys[0] != 0 || keys[1] != 700 {
		t.Fatalf("keyframes = %v", keys)
	}
}

func TestValidateIndexQuarantinesBadRows(t *testing.T) {
	idx := &Index{Version: IndexVersion, Codec: CodecH264, Timescale: 90000, Entries: []IndexEntry{
		{Offset: 5, Size: 400, Timestamp: 0, Keyframe: true},    // not a boundary
		{Offset: 400, Size: 250, Timestamp: 3000},               // wrong size
		{Offset: 900, Size: 300, Timestamp: 6000},               // out of range
		{Offset: 0, Size: 400, Timestamp: 9000, Keyframe: true}, // accepted
		{Offset: 400, Size: 300, Timestamp: 100},                // time reorder
	}}
	report, err := ValidateIndex(testStream(), idx)
	if err != nil {
		t.Fatal(err)
	}
	if report.QuarantinedCount() != 4 {
		t.Fatalf("quarantined = %d want 4", report.QuarantinedCount())
	}
	want := []fault.Code{
		fault.CodeScanIndexNotBoundary, fault.CodeScanIndexNotBoundary,
		fault.CodeScanIndexOutOfRange, fault.CodeScanIndexTimeReorder,
	}
	for i, q := range report.Quarantined {
		if q.Fault.Code != want[i] || !q.Fault.HasOffset {
			t.Errorf("row %d = %s offset=%v", i, q.Fault.Code, q.Fault.HasOffset)
		}
	}
	if len(report.Accepted) != 1 {
		t.Fatalf("accepted = %d want 1", len(report.Accepted))
	}
}

func TestValidateIndexCodecMismatch(t *testing.T) {
	idx := &Index{Version: IndexVersion, Codec: CodecAV1, Timescale: 1000,
		Entries: []IndexEntry{{Offset: 0, Size: 400, Keyframe: true}}}
	_, err := ValidateIndex(testStream(), idx)
	f, ok := fault.As(err)
	if !ok || f.Code != fault.CodeScanIndexCodec {
		t.Fatalf("err = %v", err)
	}
}

func TestParseIndexRejects(t *testing.T) {
	cases := map[string]fault.Code{
		``:  fault.CodeScanTruncatedHeader,
		`{`: fault.CodeScanTruncatedHeader,
		`{"version":99,"codec":"h264","timescale":90000,"entries":[{"offset":0,"size":4}]}`: fault.CodeScanIndexVersion,
		`{"version":1,"codec":"h264","timescale":90000,"entries":[{"offset":-1,"size":4}]}`: fault.CodeScanIndexOutOfRange,
		`{"version":1,"codec":"h264","timescale":90000,"entries":[]}`:                       fault.CodeScanNoFrames,
	}
	for doc, want := range cases {
		_, err := ParseIndex([]byte(doc))
		f, ok := fault.As(err)
		if !ok || f.Code != want {
			t.Errorf("doc %q err = %v want %s", doc, err, want)
		}
	}
}

func TestIndexRoundTrip(t *testing.T) {
	idx := &Index{Version: IndexVersion, Codec: CodecH264, Timescale: 90000,
		Entries: []IndexEntry{{Offset: 0, Size: 10, Timestamp: 0, Keyframe: true}}}
	b, err := json.Marshal(idx)
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseIndex(b)
	if err != nil {
		t.Fatal(err)
	}
	if back.Codec != idx.Codec || len(back.Entries) != 1 {
		t.Fatalf("round trip = %+v", back)
	}
}
