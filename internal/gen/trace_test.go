package gen

import (
	"bytes"
	"io"
	"testing"

	"mediakernel/internal/rtpin"
)

func TestTraceReassemblesOriginal(t *testing.T) {
	data := BuildMP4(specs())
	trace, err := BuildTrace(data, TraceOptions{
		ChunkSize: 512, DupEvery: 5, LossStart: 10, LossLen: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	r := rtpin.NewReassembler()
	var out []byte
	off := int64(0)
	tr := bytes.NewReader(trace)
	for {
		rec, err := rtpin.ReadRecord(tr, off)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		off += int64(rtpin.RecordHeaderLen) + int64(len(rec.Payload)) + 12 // rtp header, no extensions
		segs, err := r.Add(rec)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range segs {
			out = append(out, s.Data...)
		}
	}
	if !bytes.Equal(out, data) {
		t.Fatalf("reassembled %d bytes, want %d; dups=%d", len(out), len(data), r.Dups)
	}
	if r.Dups == 0 {
		t.Fatal("expected duplicates to be exercised")
	}
	if _, _, ok := r.FirstMissing(); ok {
		t.Fatal("retransmission should close all gaps")
	}
}
