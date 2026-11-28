package ingest

import (
	"bytes"
	"testing"

	"medcheck/internal/model"
)

func buildRecords(t *testing.T, ins []EncodeInput) []byte {
	t.Helper()
	var out bytes.Buffer
	for _, in := range ins {
		rec, err := EncodeRecord(in)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		out.Write(rec)
	}
	return out.Bytes()
}

func TestRoundTripValidFrames(t *testing.T) {
	ins := []EncodeInput{
		{Track: 1, Source: 7, Seq: 0, EventTicks: 0, Base: model.Rational{Num: 90000, Den: 1}, FrameType: model.TypeKeyFrame, Payload: []byte("key0")},
		{Track: 1, Source: 7, Seq: 1, EventTicks: 3000, Base: model.Rational{Num: 90000, Den: 1}, FrameType: model.TypeDelta, Payload: []byte("delta1")},
	}
	frames, err := NewReader(bytes.NewReader(buildRecords(t, ins)), "rt").ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(frames) != 2 {
		t.Fatalf("got %d frames want 2", len(frames))
	}
	var wantOff int64
	for i, f := range frames {
		if !f.Valid || f.Err != nil {
			t.Fatalf("frame %d should be valid: %+v", i, f.Err)
		}
		if f.Offset.Start != wantOff {
			t.Fatalf("frame %d offset = %d want %d", i, f.Offset.Start, wantOff)
		}
		wantOff += int64(HeaderSize + len(ins[i].Payload) + crcSize)
	}
	if frames[1].EventTime.Micros != 33333 { // 3000/90000 s
		t.Fatalf("micros = %d want 33333", frames[1].EventTime.Micros)
	}
}

func TestBadChecksumIsolated(t *testing.T) {
	ins := []EncodeInput{
		{Track: 1, Seq: 0, Base: model.Rational{Num: 90000, Den: 1}, FrameType: model.TypeKeyFrame, Payload: []byte("good")},
		{Track: 1, Seq: 1, Base: model.Rational{Num: 90000, Den: 1}, FrameType: model.TypeDelta, Payload: []byte("bad!"), Corrupt: true},
		{Track: 1, Seq: 2, Base: model.Rational{Num: 90000, Den: 1}, FrameType: model.TypeDelta, Payload: []byte("good2")},
	}
	frames, err := NewReader(bytes.NewReader(buildRecords(t, ins)), "rt").ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !frames[0].Valid || frames[1].Valid || !frames[2].Valid {
		t.Fatalf("validity = %v,%v,%v want true,false,true", frames[0].Valid, frames[1].Valid, frames[2].Valid)
	}
	ce := frames[1].Err
	if ce == nil || ce.Kind != model.KindChecksum {
		t.Fatalf("middle frame error = %+v", ce)
	}
	// First error position is the start of that record only, not its neighbor.
	if ce.FirstPos != frames[1].Offset.Start || ce.FirstPos == frames[2].Offset.Start {
		t.Fatalf("FirstPos not isolated: %d", ce.FirstPos)
	}
	// Following valid frame must not be merged into the damage region.
	if frames[2].Err != nil {
		t.Fatal("later valid frame must stay clean")
	}
}

func TestTruncatedTrailingRecord(t *testing.T) {
	good := buildRecords(t, []EncodeInput{
		{Track: 1, Seq: 0, Base: model.Rational{Num: 90000, Den: 1}, FrameType: model.TypeKeyFrame, Payload: []byte("x")},
	})
	// Append a partial header.
	stream := append(good, 0x4D, 0x43, 0x46)
	frames, err := NewReader(bytes.NewReader(stream), "rt").ReadAll()
	if err == nil {
		t.Fatal("expected truncation error")
	}
	ce := model.AsCodeError(err)
	if ce == nil || ce.Kind != model.KindTruncatedBox {
		t.Fatalf("err = %v", err)
	}
	if len(frames) != 1 || !frames[0].Valid {
		t.Fatal("recoverable frame before truncation must be returned")
	}
}

func TestRejectsOversizePayload(t *testing.T) {
	big := make([]byte, model.MaxFramePayload+1)
	if _, err := EncodeRecord(EncodeInput{Payload: big}); err == nil {
		t.Fatal("oversize payload must be rejected at encode time")
	}
}

func TestTruncatedBodyReportsStartOffset(t *testing.T) {
	rec := buildRecords(t, []EncodeInput{
		{Track: 1, Seq: 0, Base: model.Rational{Num: 90000, Den: 1}, FrameType: model.TypeKeyFrame, Payload: []byte("hello")},
	})
	// Keep full header but cut the body halfway.
	stream := rec[:HeaderSize+2]
	_, err := NewReader(bytes.NewReader(stream), "rt").ReadAll()
	ce := model.AsCodeError(err)
	if ce == nil || ce.Kind != model.KindTruncatedBox || ce.FirstPos != 0 {
		t.Fatalf("err = %v firstPos=%d", err, func() int64 {
			if ce != nil {
				return ce.FirstPos
			}
			return -1
		}())
	}
}
