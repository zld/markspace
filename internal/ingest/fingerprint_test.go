package ingest

import (
	"testing"

	"mediad/internal/model"
)

func sampleIndex() *Index {
	idx, err := ValidateIndex(CodecH264, []IndexEntry{
		{Offset: 0, Length: 4, Key: true},
		{Offset: 4, Length: 4, Key: true},
	}, 8)
	if err != nil {
		panic(err)
	}
	return idx
}

func TestFingerprintStableAndSensitive(t *testing.T) {
	stream := []byte{0, 0, 0, 1, 0x65, 0, 0, 0}
	idx := sampleIndex()

	fp1, err := FingerprintBytes(CodecH264, stream, idx)
	if err != nil {
		t.Fatal(err)
	}
	fp2, err := FingerprintBytes(CodecH264, stream, sampleIndex())
	if err != nil {
		t.Fatal(err)
	}
	if fp1.SourceSHA != fp2.SourceSHA {
		t.Fatal("identical inputs must produce identical source digest")
	}
	if len(fp1.SourceSHA) != 64 || fp1.StreamSHA == fp1.SourceSHA {
		t.Fatal("source digest malformed or not derived")
	}

	// A single flipped stream byte changes the source digest.
	changed := append([]byte{}, stream...)
	changed[0] = 1
	fp3, err := FingerprintBytes(CodecH264, changed, idx)
	if err != nil {
		t.Fatal(err)
	}
	if fp3.SourceSHA == fp1.SourceSHA || fp3.StreamSHA == fp1.StreamSHA {
		t.Fatal("stream change must change the fingerprint")
	}

	// An index-only change also changes the source digest while the stream
	// digest stays equal.
	idx2, err := ValidateIndex(CodecH264, []IndexEntry{{Offset: 0, Length: 8, Key: true}}, 8)
	if err != nil {
		t.Fatal(err)
	}
	fp4, err := FingerprintBytes(CodecH264, stream, idx2)
	if err != nil {
		t.Fatal(err)
	}
	if fp4.SourceSHA == fp1.SourceSHA {
		t.Fatal("index change must change the source digest")
	}
	if fp4.StreamSHA != fp1.StreamSHA {
		t.Fatal("unchanged stream must keep its stream digest")
	}

	// Codec change changes the fingerprint.
	idxH265, err := ValidateIndex(CodecH265, idx.Entries, 8)
	if err != nil {
		t.Fatal(err)
	}
	fp5, err := FingerprintBytes(CodecH265, stream, idxH265)
	if err != nil {
		t.Fatal(err)
	}
	if fp5.SourceSHA == fp1.SourceSHA {
		t.Fatal("codec change must change the fingerprint")
	}
}

func TestDedupeTableRejectsSecondAppend(t *testing.T) {
	d := NewDedupeTable()
	if d.Seen("abc") {
		t.Fatal("empty table reports a seen digest")
	}
	if err := d.Register("abc"); err != nil {
		t.Fatalf("first register: %v", err)
	}
	err := d.Register("abc")
	se, ok := model.AsStructured(err)
	if !ok || se.Code != model.ErrDuplicateManifest {
		t.Fatalf("want DUPLICATE_MANIFEST_ENTRY, got %v", err)
	}
	if d.Size() != 1 {
		t.Fatal("duplicate must not grow the table")
	}
	if err := d.Register("def"); err != nil {
		t.Fatalf("distinct digest should register: %v", err)
	}
	if d.Size() != 2 {
		t.Fatal("table size mismatch")
	}
}
