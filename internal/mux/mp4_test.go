package mux

import (
	"testing"

	"mediasync/internal/clock"
	"mediasync/internal/index"
)

func TestIndexMP4RoundTrip(t *testing.T) {
	recs := []index.Record{
		{ID: 0, TimeTicks: 0, TimeMs: 0, ByteOffset: 0, FrameIndex: 0, Length: 1920},
		{ID: 1, TimeTicks: 9000, TimeMs: 100, ByteOffset: 96000, FrameIndex: 24000, Length: 1920, Concealed: true},
	}
	const inputLength = 200000
	data, err := WriteIndexMP4(recs, inputLength)
	if err != nil {
		t.Fatal(err)
	}
	got, gotInput, err := ReadIndexMP4(data)
	if err != nil {
		t.Fatal(err)
	}
	if gotInput != inputLength || len(got) != len(recs) {
		t.Fatalf("header mismatch: input=%d count=%d", gotInput, len(got))
	}
	for i := range recs {
		if got[i] != recs[i] {
			t.Fatalf("record %d changed across MP4 round trip:\n got %+v\nwant %+v", i, got[i], recs[i])
		}
	}
}

func TestWriteRejectsEscapingRecord(t *testing.T) {
	bad := []index.Record{
		{ByteOffset: 199990, FrameIndex: 199990 / 4, Length: 100},
	}
	if _, err := WriteIndexMP4(bad, 200000); err == nil {
		t.Fatal("record crossing input length must be rejected before MP4 write")
	}
}

func TestReadTruncatedMidxReportsMissing(t *testing.T) {
	recs := []index.Record{
		{TimeTicks: clock.Tick(900), ByteOffset: 0, FrameIndex: 0, Length: 8},
	}
	data, err := WriteIndexMP4(recs, 8)
	if err != nil {
		t.Fatal(err)
	}
	// Chop 10 bytes off the end, cutting into the fixed-size record.
	if _, _, err := ReadIndexMP4(data[:len(data)-10]); err == nil {
		t.Fatal("truncated midx must produce a structured truncation error")
	}
}
