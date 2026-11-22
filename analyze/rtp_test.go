package analyze

import (
	"testing"

	"mediamux/media"
)

func TestRTPCleanStream(t *testing.T) {
	raw, err := BuildRTP(20, 1000, 90000, 160, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := ValidateRTP(raw, RTPOptions{ClockRate: 90000})
	if err != nil {
		t.Fatal(err)
	}
	if res.Corrupt || len(res.Gaps) != 0 || res.Duplicates != 0 {
		t.Fatalf("clean stream flagged: %+v", res)
	}
	if res.UniquePackets != 20 || res.PayloadBytes != 20*160 {
		t.Fatalf("unique=%d bytes=%d", res.UniquePackets, res.PayloadBytes)
	}
	if res.FirstSeq != 1000 || res.LastSeq != 1019 {
		t.Fatalf("seq range %d..%d", res.FirstSeq, res.LastSeq)
	}
	// 19 packet intervals of 3000/90000s each == 633.3ms.
	if res.TimestampMS != 633 {
		t.Fatalf("timestamp ms=%d", res.TimestampMS)
	}
	if res.Duration.Value != 19*3000 {
		t.Fatalf("duration=%d", res.Duration.Value)
	}
}

func TestRTPSequenceWrapIsModular(t *testing.T) {
	// Start near the rollover so the stream crosses 65535 -> 0.
	raw, err := BuildRTP(12, 65530, 0, 100, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	res := AnalyzeRTP(raw, RTPOptions{})
	if res.Missing != 0 || res.Corrupt {
		t.Fatalf("wrap misread as loss: %+v", res)
	}
	if res.Wraps != 1 {
		t.Fatalf("wraps=%d", res.Wraps)
	}
	if res.LastSeq != 5 {
		t.Fatalf("last seq=%d", res.LastSeq)
	}
}

func TestRTPDuplicatesNotCountedTwice(t *testing.T) {
	raw, err := BuildRTP(10, 1, 0, 128, nil, map[uint16]bool{3: true, 7: true})
	if err != nil {
		t.Fatal(err)
	}
	res := AnalyzeRTP(raw, RTPOptions{})
	if res.Duplicates != 2 {
		t.Fatalf("duplicates=%d", res.Duplicates)
	}
	if res.UniquePackets != 10 {
		t.Fatalf("unique=%d", res.UniquePackets)
	}
	if res.PayloadBytes != 10*128 {
		t.Fatalf("duplicate payload counted: bytes=%d", res.PayloadBytes)
	}
	if res.Corrupt {
		t.Fatal("duplicates alone must not inflate loss")
	}
}

func TestRTPGapIsReportedWithSequenceRange(t *testing.T) {
	raw, err := BuildRTP(20, 100, 0, 64, map[uint16]bool{105: true, 106: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	res := AnalyzeRTP(raw, RTPOptions{})
	if !res.Corrupt || res.Missing != 2 {
		t.Fatalf("missing=%d corrupt=%v", res.Missing, res.Corrupt)
	}
	if len(res.Gaps) != 1 || res.Gaps[0].From != 105 || res.Gaps[0].To != 106 {
		t.Fatalf("gaps=%+v", res.Gaps)
	}
	if res.PayloadBytes != 18*64 {
		t.Fatalf("bytes=%d", res.PayloadBytes)
	}
}

func TestRTPPayloadBoundAndMalformed(t *testing.T) {
	raw, err := BuildRTP(3, 1, 0, 200, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	res := AnalyzeRTP(raw, RTPOptions{MaxPayload: 100})
	if res.Malformed != 3 || !res.Corrupt {
		t.Fatalf("oversized payloads accepted: %+v", res)
	}
	bad, err := BuildRTP(2, 1, 0, 10, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	bad = append([][]byte{{0x80}}, bad...)
	res = AnalyzeRTP(bad, RTPOptions{})
	if res.Malformed != 1 || !res.Corrupt {
		t.Fatalf("malformed header accepted: %+v", res)
	}
	if res.Packets[0].Reason == "" {
		t.Fatal("malformed packet must carry a reason")
	}
}

func TestRTPReorderingDoesNotDoubleCount(t *testing.T) {
	raw, err := BuildRTP(4, 10, 0, 32, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Swap packets 2 and 3 to simulate reordering.
	raw[1], raw[2] = raw[2], raw[1]
	res := AnalyzeRTP(raw, RTPOptions{})
	if res.UniquePackets != 4 {
		t.Fatalf("unique=%d", res.UniquePackets)
	}
	if res.PayloadBytes != 4*32 {
		t.Fatalf("bytes=%d", res.PayloadBytes)
	}
}

func TestValidateRTPRejectsEmpty(t *testing.T) {
	if _, err := ValidateRTP(nil, RTPOptions{}); err == nil {
		t.Fatal("expected input error")
	} else if !media.HasCode(err, media.CodeInput) {
		t.Fatalf("err=%v", err)
	}
}

func TestPercentileNearestRank(t *testing.T) {
	vals := []int64{500, 100, 300, 200, 400}
	if got := Percentile(vals, 0.5); got != 300 {
		t.Fatalf("p50=%d", got)
	}
	if got := Percentile(vals, 0.95); got != 500 {
		t.Fatalf("p95=%d", got)
	}
	if vals[0] != 500 {
		t.Fatal("Percentile must not reorder the caller's slice")
	}
	if Percentile(nil, 0.5) != 0 {
		t.Fatal("empty percentile must be zero")
	}
}
