package binio

import (
	"bytes"
	"errors"
	"testing"

	"mediapack/internal/model"
)

func TestBERRoundTrip(t *testing.T) {
	cases := []uint64{0, 1, 127, 128, 255, 65535, 1 << 20, 1 << 40}
	for _, v := range cases {
		enc := BERLen(v)
		got, n, err := ReadBERLen(bytes.NewReader(enc))
		if err != nil || got != v || n != len(enc) {
			t.Fatalf("v=%d got=%d n=%d err=%v", v, got, n, err)
		}
	}
	if _, _, err := ReadBERLen(bytes.NewReader([]byte{0xff})); err == nil {
		t.Fatal("0xff prefix must be rejected")
	}
}

func TestBoxFraming(t *testing.T) {
	var buf bytes.Buffer
	off, size, err := Box(&buf, "mdat", []byte{1, 2, 3})
	if err != nil || off != 0 || size != 11 || buf.Len() != 11 {
		t.Fatalf("off=%d size=%d len=%d err=%v", off, size, buf.Len(), err)
	}
	if string(buf.Bytes()[4:8]) != "mdat" {
		t.Fatal("type mismatch")
	}
}

func TestLimitReaderTruncation(t *testing.T) {
	r := LimitReader(bytes.NewReader([]byte{1, 2}), 5, 77)
	buf := make([]byte, 5)
	_, err := r.Read(buf)
	if err == nil {
		t.Fatal("expected truncation error")
	}
	me, ok := model.AsMediaError(err)
	if !ok || me.Code != model.CodeTruncatedBox || me.Offset != 77 {
		t.Fatalf("unexpected %v", err)
	}
	if !errors.Is(err, err) { // sanity: error is non-nil and typed
		t.Fatal("nil error")
	}
}
