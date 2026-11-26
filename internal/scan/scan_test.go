package scan_test

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"regress/internal/fault"
	"regress/internal/fixture"
	"regress/internal/scan"
	"regress/internal/timebase"
)

func TestScanAllCodecs(t *testing.T) {
	ctx := context.Background()
	for _, codec := range []string{scan.CodecH264, scan.CodecH265, scan.CodecAV1} {
		codec := codec
		t.Run(codec, func(t *testing.T) {
			dir := t.TempDir()
			set, err := fixture.Build(ctx, dir, fixture.DefaultSpec(codec))
			if err != nil {
				t.Fatalf("fixture build: %v", err)
			}
			if got := len(set.Scan.Keyframes); got != 4 {
				t.Fatalf("keyframes = %d, want 4", got)
			}
			if len(set.Scan.AccessUnits) == 0 {
				t.Fatal("no access units parsed")
			}
			// First access unit must be a random access point.
			if !set.Scan.AccessUnits[0].IsRAP {
				t.Fatal("first access unit is not a random access point")
			}
			// Every keyframe offset is unique and points at a real AU start.
			seen := map[int64]bool{}
			for _, kfi := range set.Scan.Keyframes {
				off := set.Scan.AccessUnits[kfi].Offset
				if seen[off] {
					t.Fatalf("duplicate keyframe offset %d", off)
				}
				seen[off] = true
			}
		})
	}
}

func TestKeyIndexRoundTrip(t *testing.T) {
	idx := &scan.KeyIndex{
		TimeDen: 24,
		Entries: []scan.IndexEntry{
			{Offset: 10, TimeTicks: 0, FrameOrdinal: 0},
			{Offset: 999, TimeTicks: 12, FrameOrdinal: 12},
		},
	}
	raw, err := scan.MarshalKeyIndex(idx)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(raw, scan.KeyIndexMagic[:]) {
		t.Fatal("missing KIDX magic")
	}
	got, err := scan.UnmarshalKeyIndex(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if got.TimeDen != 24 || len(got.Entries) != 2 {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	if got.Entries[1].Offset != 999 || got.Entries[1].TimeTicks != 12 {
		t.Fatalf("entry round trip mismatch: %+v", got.Entries[1])
	}
}

func TestValidateIndexCleanAndCorrupt(t *testing.T) {
	ctx := context.Background()
	set, err := fixture.Build(ctx, t.TempDir(), fixture.DefaultSpec(scan.CodecH264))
	if err != nil {
		t.Fatal(err)
	}
	// Clean index: zero rejections.
	report, err := scan.ValidateIndex(set.Scan, set.Index)
	if err != nil || report.Rejected != 0 || report.Accepted != 4 {
		t.Fatalf("clean index report=%+v err=%v", report, err)
	}
	// Corrupt one of four entries so it lands on a non-keyframe.
	corruptPath, err := fixture.CorruptIndex(set, 1)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := readFile(corruptPath)
	if err != nil {
		t.Fatal(err)
	}
	cidx, err := scan.UnmarshalKeyIndex(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	report, err = scan.ValidateIndex(set.Scan, cidx)
	if err == nil {
		t.Fatal("corrupt index expected to fail validation")
	}
	if report.Rejected != 1 || report.Accepted != 3 {
		t.Fatalf("corrupt report = %+v, want 1 rejected / 3 accepted", report)
	}
	if !fault.HasCode(err, fault.CodeIndexNotKeyframe) {
		t.Fatalf("want %s, got %v", fault.CodeIndexNotKeyframe, fault.Codes(err))
	}
}

func TestValidateIndexOutOfRangeAndDuplicate(t *testing.T) {
	ctx := context.Background()
	set, err := fixture.Build(ctx, t.TempDir(), fixture.DefaultSpec(scan.CodecH265))
	if err != nil {
		t.Fatal(err)
	}
	rap := set.Scan.RAPOffsets()

	t.Run("out_of_range", func(t *testing.T) {
		bad := &scan.KeyIndex{TimeDen: set.Index.TimeDen, Entries: []scan.IndexEntry{
			{Offset: set.Scan.StreamBytes + 100, TimeTicks: 0, FrameOrdinal: 0},
		}}
		report, err := scan.ValidateIndex(set.Scan, bad)
		if err == nil || report.Rejected != 1 {
			t.Fatalf("report=%+v err=%v", report, err)
		}
		if !fault.HasCode(err, fault.CodeIndexOutOfRange) {
			t.Fatalf("want out-of-range code, got %v", fault.Codes(err))
		}
	})

	t.Run("duplicate_timestamp", func(t *testing.T) {
		// Two well-formed RAP pointers sharing one presentation timestamp.
		bad := &scan.KeyIndex{TimeDen: set.Index.TimeDen, Entries: []scan.IndexEntry{
			{Offset: rap[0], TimeTicks: 5, FrameOrdinal: 0},
			{Offset: rap[1], TimeTicks: 5, FrameOrdinal: 12},
		}}
		report, err := scan.ValidateIndex(set.Scan, bad)
		if err == nil || report.Rejected != 1 || report.Accepted != 1 {
			t.Fatalf("report=%+v err=%v", report, err)
		}
		if !fault.HasCode(err, fault.CodeIndexTimeDuplicate) {
			t.Fatalf("want duplicate code, got %v", fault.Codes(err))
		}
	})

	t.Run("aggregate", func(t *testing.T) {
		bad := &scan.KeyIndex{TimeDen: set.Index.TimeDen, Entries: []scan.IndexEntry{
			{Offset: set.Scan.StreamBytes + 100, TimeTicks: 0, FrameOrdinal: 0},
			{Offset: rap[0], TimeTicks: 5, FrameOrdinal: 1},
			{Offset: rap[1], TimeTicks: 5, FrameOrdinal: 13},
		}}
		report, err := scan.ValidateIndex(set.Scan, bad)
		if err == nil {
			t.Fatal("expected aggregated structured errors")
		}
		if report.Rejected != 2 {
			t.Fatalf("rejected = %d, want 2", report.Rejected)
		}
		codes := fault.Codes(err)
		if !containsCode(codes, fault.CodeIndexOutOfRange) ||
			!containsCode(codes, fault.CodeIndexTimeDuplicate) {
			t.Fatalf("missing expected codes in %v", codes)
		}
	})
}

func TestTruncatedTailRejected(t *testing.T) {
	ctx := context.Background()
	for _, codec := range []string{scan.CodecH264, scan.CodecH265, scan.CodecAV1} {
		set, err := fixture.Build(ctx, t.TempDir(), fixture.DefaultSpec(codec))
		if err != nil {
			t.Fatal(err)
		}
		path, err := fixture.TruncateTail(set)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := readFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := scan.ScanElementary(codec, bytes.NewReader(raw)); err == nil {
			t.Fatalf("%s: truncated stream accepted", codec)
		} else if !fault.HasCode(err, fault.CodeTruncatedTail) {
			t.Fatalf("%s: want truncated-tail code, got %v", codec, fault.Codes(err))
		}
	}
}

func TestTimebaseRational(t *testing.T) {
	// 24 fps, one frame slot expressed in the canonical 90 kHz clock is 3750 ticks.
	r, err := timebase.New(1, 24)
	if err != nil {
		t.Fatal(err)
	}
	ticks, err := r.Ticks()
	if err != nil {
		t.Fatal(err)
	}
	if ticks != 3750 {
		t.Fatalf("ticks = %d, want 3750", ticks)
	}
	ns, err := r.Nanoseconds()
	if err != nil {
		t.Fatal(err)
	}
	if ns != 41_666_667 { // nearest rounding of 1e9/24
		t.Fatalf("ns = %d, want 41666667", ns)
	}
}

func TestStructuredErrorShape(t *testing.T) {
	e := fault.At(fault.StageScan, fault.CodeIndexNotKeyframe, scan.CodecH264, 42, "detail")
	if e.Stage != fault.StageScan || e.Offset != 42 || e.Codec != "h264" {
		t.Fatalf("structured fields lost: %+v", e)
	}
	if !strings.Contains(e.Error(), "index_points_to_non_keyframe") ||
		!strings.Contains(e.Error(), "@offset=42") {
		t.Fatalf("error string missing anchors: %q", e.Error())
	}
}

func containsCode(codes []fault.Code, want fault.Code) bool {
	for _, c := range codes {
		if c == want {
			return true
		}
	}
	return false
}

func readFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}
