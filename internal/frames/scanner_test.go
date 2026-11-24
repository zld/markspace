package frames

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"mediarun/internal/fault"
)

// buildStream encodes count frames with deterministic payloads.
func buildStream(t *testing.T, count int, keyEvery int) []byte {
	t.Helper()
	var buf bytes.Buffer
	for i := 0; i < count; i++ {
		payload := []byte{0x49, 0x4d, 0x47, 0x31, byte(i)}
		payload = append(payload, bytes.Repeat([]byte{byte(i)}, 16)...)
		flags := uint16(0)
		if keyEvery > 0 && i%keyEvery == 0 {
			flags |= FlagKeyframe
		}
		if i == count-1 {
			flags |= FlagEndOfSeq
		}
		if _, err := Encode(&buf, EncodeOptions{
			Flags: flags,
			Track: 1,
			PTS:   uint64(i) * 3000,
			DTS:   uint64(i) * 3000,
		}, payload); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
	return buf.Bytes()
}

func scanAll(t *testing.T, data []byte) ([]*Record, fault.List) {
	t.Helper()
	sc, err := NewScanner(bytes.NewReader(data), int64(len(data)), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	var recs []*Record
	for {
		rec, err := sc.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("unexpected terminal error: %v", err)
		}
		recs = append(recs, rec)
	}
	return recs, sc.Faults()
}

func TestScanRoundTrip(t *testing.T) {
	data := buildStream(t, 5, 2)
	recs, faults := scanAll(t, data)
	if len(faults) != 0 {
		t.Fatalf("unexpected faults: %v", faults)
	}
	if len(recs) != 5 {
		t.Fatalf("records = %d", len(recs))
	}
	var wantOffset int64
	for i, rec := range recs {
		if rec.Offset != wantOffset {
			t.Fatalf("record %d offset = %d want %d", i, rec.Offset, wantOffset)
		}
		if rec.Meta.PTS != uint64(i)*3000 {
			t.Fatalf("record %d pts = %d", i, rec.Meta.PTS)
		}
		if rec.Keyframe() != (i%2 == 0) {
			t.Fatalf("record %d keyframe flag wrong", i)
		}
		if rec.Meta.MediaKind != MediaImageYCbCr {
			t.Fatalf("record %d kind = %d", i, rec.Meta.MediaKind)
		}
		wantOffset += int64(HeaderSize) + int64(rec.Meta.Size) + DigestSize
	}
	if !recs[4].EndOfSequence() {
		t.Fatal("last record missing end-of-sequence flag")
	}
}

func TestScanTruncatedMidStreamRecoversLaterRecords(t *testing.T) {
	data := buildStream(t, 4, 0)
	// Splice out the payload tail and digest of record 2 while keeping the
	// following records: its declared length now overshoots into record 3.
	const span = HeaderSize + 21 + DigestSize
	second := span
	damaged := append([]byte{}, data...)
	// Record 2 now claims a payload that runs past the end of the spliced
	// stream, so it is reported truncated and skipped.
	damaged[second+26], damaged[second+27], damaged[second+28], damaged[second+29] = 0, 0, 0x10, 0x00
	recs, faults := scanAll(t, damaged)
	if faults.Len() == 0 {
		t.Fatal("expected a truncation fault")
	}
	if faults.CountKind(fault.KindTruncated) == 0 {
		t.Fatalf("expected truncated kind, got %s", faults.Summary())
	}
	if len(recs) < 3 {
		t.Fatalf("scanner did not recover later records: %d", len(recs))
	}
	off, ok := faults.FirstOffset()
	if !ok || off != int64(second) {
		t.Fatalf("first fault offset = %d ok=%v want %d", off, ok, second)
	}
}

func TestScanTruncatedTail(t *testing.T) {
	data := buildStream(t, 3, 0)
	damaged := data[:len(data)-10]
	recs, faults := scanAll(t, damaged)
	if len(recs) != 2 {
		t.Fatalf("records = %d want 2", len(recs))
	}
	if faults.CountKind(fault.KindTruncated) != 1 {
		t.Fatalf("faults = %s", faults.Summary())
	}
	wantOff := int64(2 * (HeaderSize + 21 + DigestSize))
	if faults[0].Offset != wantOff {
		t.Fatalf("tail fault offset = %d want %d", faults[0].Offset, wantOff)
	}
}

func TestScanChecksumFailureReportsOffsetAndContinues(t *testing.T) {
	data := buildStream(t, 3, 0)
	// Flip a payload byte of the middle record.
	second := HeaderSize + len("IMG1\x00") + 16 + DigestSize
	damaged := append([]byte{}, data...)
	damaged[second+HeaderSize+2] ^= 0xFF
	recs, faults := scanAll(t, damaged)
	if faults.CountKind(fault.KindChecksum) != 1 {
		t.Fatalf("checksum faults = %d (%s)", faults.CountKind(fault.KindChecksum), faults.Summary())
	}
	fe := faults[0]
	if fe.Offset != int64(second) {
		t.Fatalf("fault offset = %d want %d", fe.Offset, second)
	}
	if fe.Stage != fault.StageChecksum || fe.Track != 1 {
		t.Fatalf("fault metadata = %+v", fe)
	}
	if len(recs) != 2 {
		t.Fatalf("recovered records = %d want 2", len(recs))
	}
}

func TestScanRejectsOversizedLengthWithoutAllocating(t *testing.T) {
	data := buildStream(t, 1, 0)
	damaged := append([]byte{}, data...)
	// Declare a payload far beyond the cap.
	for i := 26; i < 30; i++ {
		damaged[i] = 0xFF
	}
	sc, err := NewScanner(bytes.NewReader(damaged), int64(len(damaged)), Limits{MaxPayload: 1024})
	if err != nil {
		t.Fatal(err)
	}
	_, err = sc.Next()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF after skipped record, got %v", err)
	}
	if sc.Faults().CountKind(fault.KindLength) != 1 {
		t.Fatalf("faults = %s", sc.Faults().Summary())
	}
}

func TestScanVersionAndTrackValidation(t *testing.T) {
	var buf bytes.Buffer
	if _, err := Encode(&buf, EncodeOptions{Version: 9, Track: 1}, []byte("IMG1abc")); err != nil {
		t.Fatal(err)
	}
	if _, err := Encode(&buf, EncodeOptions{Track: 0}, []byte("IMG1abc")); err != nil {
		t.Fatal(err)
	}
	sc, err := NewScanner(bytes.NewReader(buf.Bytes()), int64(buf.Len()), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := sc.Next(); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if sc.Faults().CountKind(fault.KindVersion) != 1 {
		t.Fatalf("version faults = %s", sc.Faults().Summary())
	}
	if sc.Faults().CountKind(fault.KindTrack) != 1 {
		t.Fatalf("track faults = %s", sc.Faults().Summary())
	}
}

func TestScanRejectsBadTimestamps(t *testing.T) {
	var buf bytes.Buffer
	// DTS after PTS.
	if _, err := Encode(&buf, EncodeOptions{Track: 1, PTS: 100, DTS: 200}, []byte("IMG1abc")); err != nil {
		t.Fatal(err)
	}
	// Timestamp beyond the int64 tick domain.
	if _, err := Encode(&buf, EncodeOptions{Track: 1, PTS: 1 << 63, DTS: 1 << 63}, []byte("IMG1abc")); err != nil {
		t.Fatal(err)
	}
	recs, faults := scanAll(t, buf.Bytes())
	if len(recs) != 0 {
		t.Fatalf("records = %d", len(recs))
	}
	if faults.CountKind(fault.KindTimestamp) != 2 {
		t.Fatalf("faults = %s", faults.Summary())
	}
	if faults[0].Offset != 0 || faults[1].Offset == 0 {
		t.Fatalf("offsets = %d %d", faults[0].Offset, faults[1].Offset)
	}
}

func TestEncodeRejectsEmptyAndHugePayload(t *testing.T) {
	if _, err := Encode(io.Discard, EncodeOptions{}, nil); err == nil {
		t.Fatal("empty payload accepted")
	}
	if _, err := Encode(io.Discard, EncodeOptions{}, make([]byte, MaxPayload+1)); err == nil {
		t.Fatal("oversized payload accepted")
	}
}

func TestScannerLimitRejectsInvalidMaxPayload(t *testing.T) {
	if _, err := NewScanner(bytes.NewReader([]byte{1}), 1, Limits{MaxPayload: MaxPayload + 1}); err == nil {
		t.Fatal("limit above container cap accepted")
	}
	if _, err := NewScanner(nil, 0, Limits{}); err == nil {
		t.Fatal("empty source accepted")
	}
}
