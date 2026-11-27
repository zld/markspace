package ingest

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"testing"

	"mediarebuild/internal/model"
)

func encode(payloads ...[]byte) []byte {
	var buf bytes.Buffer
	for _, p := range payloads {
		var h [4]byte
		binary.BigEndian.PutUint32(h[:], uint32(len(p)))
		buf.Write(h[:])
		buf.Write(p)
	}
	return buf.Bytes()
}

func TestReadRecordsWithOffsets(t *testing.T) {
	data := encode([]byte("aa"), []byte("bbb"), []byte("c"))
	rd := NewReader(bytes.NewReader(data), 0)
	var offs []int64
	var sizes []int
	for {
		rec, err := rd.Next(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		offs = append(offs, rec.Offset)
		sizes = append(sizes, len(rec.Payload))
	}
	wantOff := []int64{0, 6, 13}
	wantSize := []int{2, 3, 1}
	for i := range wantOff {
		if offs[i] != wantOff[i] || sizes[i] != wantSize[i] {
			t.Fatalf("record %d: off=%d size=%d, want off=%d size=%d",
				i, offs[i], sizes[i], wantOff[i], wantSize[i])
		}
	}
}

func TestTruncatedPayloadReportsRecordOffset(t *testing.T) {
	data := encode([]byte("hello"))
	data = data[:len(data)-2] // cut payload
	rd := NewReader(bytes.NewReader(data), 0)
	_, err := rd.Next(context.Background())
	var d model.Diag
	if !errors.As(err, &d) {
		t.Fatalf("err = %v, want model.Diag", err)
	}
	if d.Offset != 0 || d.Recoverable {
		t.Fatalf("diag = %+v, want offset 0, unrecoverable", d)
	}
}

func TestTruncatedHeaderOffset(t *testing.T) {
	data := append(encode([]byte("ok")), 0, 0) // 2 dangling bytes
	rd := NewReader(bytes.NewReader(data), 0)
	if _, err := rd.Next(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err := rd.Next(context.Background())
	var d model.Diag
	if !errors.As(err, &d) {
		t.Fatalf("err = %v, want model.Diag", err)
	}
	if d.Offset != 6 {
		t.Fatalf("offset = %d, want 6", d.Offset)
	}
}

func TestOverLimitRecord(t *testing.T) {
	var h [4]byte
	binary.BigEndian.PutUint32(h[:], MaxRecordSize+1)
	rd := NewReader(bytes.NewReader(h[:]), 0)
	_, err := rd.Next(context.Background())
	var d model.Diag
	if !errors.As(err, &d) || d.Code != "truncated-record" {
		t.Fatalf("err = %v", err)
	}
}

func TestCancelStopsReading(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rd := NewReader(bytes.NewReader(encode([]byte("x"))), 0)
	if _, err := rd.Next(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
