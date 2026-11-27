package ingest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"rtp-rebuild/internal/fault"
)

func buildStream(t *testing.T, records []Record) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := NewWriter(&buf, 1_700_000_000_000_000_000)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	for _, r := range records {
		if _, err := w.WriteRecord(r.Kind, r.Data); err != nil {
			t.Fatalf("WriteRecord: %v", err)
		}
	}
	return buf.Bytes()
}

func TestRoundTripOffsets(t *testing.T) {
	payloads := []Record{
		{Kind: KindRTP, Data: []byte{0x80, 0x60}},
		{Kind: KindRTCP, Data: []byte{0x81, 0xc8, 0x00, 0x06}},
		{Kind: KindEvent, Data: []byte("1234 stall begin")},
	}
	raw := buildStream(t, payloads)
	s, err := NewStream(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("NewStream: %v", err)
	}
	if s.Header.StartUnixNano != 1_700_000_000_000_000_000 {
		t.Fatalf("StartUnixNano = %d", s.Header.StartUnixNano)
	}

	var got []Record
	for {
		rec, err := s.ReadRecord()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("ReadRecord: %v", err)
		}
		got = append(got, rec)
	}
	if len(got) != 3 {
		t.Fatalf("read %d records, want 3", len(got))
	}
	wantOffset := int64(HeaderSize)
	for i, rec := range got {
		if rec.Kind != payloads[i].Kind {
			t.Fatalf("record %d kind = %v, want %v", i, rec.Kind, payloads[i].Kind)
		}
		if rec.Offset != wantOffset {
			t.Fatalf("record %d offset = %d, want %d", i, rec.Offset, wantOffset)
		}
		if !bytes.Equal(rec.Data, payloads[i].Data) {
			t.Fatalf("record %d data = %v, want %v", i, rec.Data, payloads[i].Data)
		}
		wantOffset += int64(RecordHeaderSize + len(payloads[i].Data))
	}
}

func TestTruncatedRecordReportsOffset(t *testing.T) {
	raw := buildStream(t, []Record{
		{Kind: KindRTP, Data: []byte{1, 2, 3, 4}},
		{Kind: KindRTP, Data: []byte{5, 6, 7, 8}},
	})
	cut := raw[:len(raw)-3]
	s, err := NewStream(bytes.NewReader(cut))
	if err != nil {
		t.Fatalf("NewStream: %v", err)
	}
	if _, err := s.ReadRecord(); err != nil {
		t.Fatalf("first record: %v", err)
	}
	_, err = s.ReadRecord()
	if err == nil {
		t.Fatal("second record should fail")
	}
	if !errors.Is(err, fault.ErrTruncated) {
		t.Fatalf("error = %v, want truncated", err)
	}
	off, ok := fault.OffsetOf(err)
	if !ok {
		t.Fatal("missing offset")
	}
	wantOffset := int64(HeaderSize + RecordHeaderSize + 4)
	if off != wantOffset {
		t.Fatalf("offset = %d, want %d", off, wantOffset)
	}
}

func TestMaxStreamBytes(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewWriter(&buf, 0)
	if err != nil {
		t.Fatal(err)
	}
	big := make([]byte, 1<<20+1)
	for i := 0; i < 4; i++ {
		if _, err := w.WriteRecord(KindEvent, big); err != nil {
			t.Fatal(err)
		}
	}
	s, err := NewStream(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	var readErr error
	for {
		if _, err := s.ReadRecord(); err != nil {
			readErr = err
			break
		}
	}
	if !errors.Is(readErr, fault.ErrLimit) {
		t.Fatalf("error = %v, want limit", readErr)
	}
}

func TestCorruptHeader(t *testing.T) {
	_, err := NewStream(bytes.NewReader([]byte("XXXX")))
	if !errors.Is(err, fault.ErrTruncated) {
		t.Fatalf("short header error = %v, want truncated", err)
	}
	raw := buildStream(t, nil)
	raw[0] = 'X'
	_, err = NewStream(bytes.NewReader(raw))
	if !errors.Is(err, fault.ErrCorrupt) {
		t.Fatalf("bad magic error = %v, want corrupt", err)
	}
	raw = buildStream(t, nil)
	raw[4] = 9
	_, err = NewStream(bytes.NewReader(raw))
	if !errors.Is(err, fault.ErrUnsupported) {
		t.Fatalf("bad version error = %v, want unsupported", err)
	}
	raw = buildStream(t, []Record{{Kind: 9, Data: nil}})
	s, _ := NewStream(bytes.NewReader(raw))
	if _, err := s.ReadRecord(); !errors.Is(err, fault.ErrCorrupt) {
		t.Fatalf("bad kind error = %v, want corrupt", err)
	}
}

func TestEmitBackpressureAndCancel(t *testing.T) {
	records := make([]Record, 200)
	for i := range records {
		records[i] = Record{Kind: KindEvent, Data: []byte("x")}
	}
	raw := buildStream(t, records)
	s, err := NewStream(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	recs, errs := s.Emit(ctx)
	// Consume slowly; the producer must block on the bounded channel.
	var count int
	for range recs {
		count++
		if count == 5 {
			cancel()
		}
	}
	if err := <-errs; err == nil {
		t.Fatal("expected cancellation error")
	}
	if count >= len(records) {
		t.Fatalf("consumer saw %d records despite early cancel", count)
	}
	if count > ChannelDepth+2 {
		t.Fatalf("producer ran too far ahead: %d records", count)
	}
}

func TestEmitCompletes(t *testing.T) {
	raw := buildStream(t, []Record{{Kind: KindRTP, Data: []byte{1}}, {Kind: KindRTP, Data: []byte{2}}})
	s, err := NewStream(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	recs, errs := s.Emit(context.Background())
	var n int
	for range recs {
		n++
	}
	if err := <-errs; err != nil {
		t.Fatalf("emit error: %v", err)
	}
	if n != 2 {
		t.Fatalf("read %d records, want 2", n)
	}
}

func TestEmitCancelledBeforeStart(t *testing.T) {
	raw := buildStream(t, []Record{{Kind: KindRTP, Data: []byte{1}}})
	s, err := NewStream(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	recs, errs := s.Emit(ctx)
	for range recs {
	}
	select {
	case err := <-errs:
		if err == nil {
			t.Fatal("expected cancellation error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("emit did not report cancellation")
	}
}
