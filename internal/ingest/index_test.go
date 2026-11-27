package ingest

import (
	"testing"

	"mediad/internal/model"
)

func TestValidateIndexOK(t *testing.T) {
	entries := []IndexEntry{
		{Offset: 0, Length: 100, Key: true},
		{Offset: 100, Length: 50, Key: true},
		{Offset: 200, Length: 10, Key: true},
	}
	idx, err := ValidateIndex(CodecH264, entries, 210)
	if err != nil {
		t.Fatalf("valid index rejected: %v", err)
	}
	if len(idx.Entries) != 3 || idx.Codec != CodecH264 {
		t.Fatal("index content mismatch")
	}
	// Returned entries must be a defensive copy.
	entries[0].Offset = 999
	if idx.Entries[0].Offset != 0 {
		t.Fatal("ValidateIndex must copy entries")
	}
}

func TestValidateIndexFailures(t *testing.T) {
	base := func() []IndexEntry {
		return []IndexEntry{
			{Offset: 0, Length: 10, Key: true},
			{Offset: 10, Length: 10, Key: true},
		}
	}
	cases := []struct {
		name      string
		codec     string
		entries   []IndexEntry
		streamLen int64
		wantCode  string
	}{
		{"unknown codec", "vp8", base(), 100, model.ErrUnknownCodec},
		{"empty", CodecH264, nil, 100, model.ErrIndexLength},
		{"negative offset", CodecH264, []IndexEntry{{Offset: -1, Length: 10, Key: true}}, 100, model.ErrIndexOutOfRange},
		{"zero length", CodecH264, []IndexEntry{{Offset: 0, Length: 0, Key: true}}, 100, model.ErrIndexLength},
		{"out of range", CodecH264, []IndexEntry{{Offset: 95, Length: 10, Key: true}}, 100, model.ErrIndexOutOfRange},
		{"not key", CodecH264, []IndexEntry{{Offset: 0, Length: 10, Key: false}}, 100, model.ErrIndexOrder},
		{"backwards", CodecH264, []IndexEntry{
			{Offset: 10, Length: 5, Key: true}, {Offset: 9, Length: 5, Key: true},
		}, 100, model.ErrIndexOrder},
		{"overlap", CodecH264, []IndexEntry{
			{Offset: 0, Length: 10, Key: true}, {Offset: 5, Length: 5, Key: true},
		}, 100, model.ErrIndexOrder},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidateIndex(tc.codec, tc.entries, tc.streamLen)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			se, ok := model.AsStructured(err)
			if !ok || se.Code != tc.wantCode {
				t.Fatalf("want %s, got %v", tc.wantCode, err)
			}
			// A negative index offset has no valid stream position; every
			// other in-range failure must report its byte offset.
			if tc.name != "negative offset" && se.ByteOffset < 0 &&
				tc.wantCode != model.ErrIndexLength && tc.wantCode != model.ErrUnknownCodec {
				t.Fatalf("error must carry a byte offset: %+v", se)
			}
		})
	}
}

func TestValidateIndexOverflow(t *testing.T) {
	entries := []IndexEntry{{Offset: 1 << 62, Length: 1 << 62, Key: true}}
	_, err := ValidateIndex(CodecH265, entries, -1)
	se, ok := model.AsStructured(err)
	if !ok || se.Code != model.ErrIndexOutOfRange {
		t.Fatalf("want overflow INDEX_OUT_OF_RANGE, got %v", err)
	}
}

func TestParseIndexMismatch(t *testing.T) {
	if _, err := ParseIndex(CodecAV1, []int64{0}, []int64{1, 2}, nil, 100); err == nil {
		t.Fatal("expected slice-length mismatch error")
	}
}
