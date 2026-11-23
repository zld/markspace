package adapter

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"mediad/timeline"
)

func TestRegistryVersionRejection(t *testing.T) {
	reg := NewRegistry()
	reg.MustRegister(MetadataAdapter{})
	if _, err := reg.Get("metadata", Version{Major: 1, Minor: 0}); err != nil {
		t.Fatalf("same major must be accepted: %v", err)
	}
	if _, err := reg.Get("metadata", Version{Major: 2, Minor: 0}); err == nil ||
		!strings.Contains(err.Error(), "incompatible") {
		t.Fatalf("major mismatch must be rejected, got %v", err)
	}
	if _, err := reg.Get("missing", Version{Major: 1, Minor: 0}); err == nil {
		t.Fatal("unknown adapter name must error")
	}
	if err := reg.Register(MetadataAdapter{}); err == nil {
		t.Fatal("duplicate registration must error")
	}
}

func TestMetadataUnknownPreservedRaw(t *testing.T) {
	w := NewMetadataWriter()
	w.WriteRecord(3, 100, kindSubtitle, 0, "webvtt", []byte("hello"))
	w.WriteRecord(3, 200, 211, 0, "x-custom", []byte{0xde, 0xad, 0xbe, 0xef})
	res, err := metaA().Parse(bytes.NewReader(w.Bytes()), Options{Timescale: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Unknown) != 1 || !bytes.Equal(res.Unknown[0].Raw, []byte{0xde, 0xad, 0xbe, 0xef}) {
		t.Fatalf("unknown metadata not preserved raw: %+v", res.Unknown)
	}
	if res.Tracks[0].Events[1].Kind != timeline.KindUnknown {
		t.Fatal("unknown event kind not tagged")
	}
}

func TestMetadataTruncatedAndIllegalLength(t *testing.T) {
	w := NewMetadataWriter()
	w.WriteRecord(1, 0, kindID3, 0, "id3", []byte("tag"))
	good := w.Bytes()
	// Truncate inside the declared record: remaining bytes < recLen.
	if _, err := metaA().Parse(bytes.NewReader(good[:12]), Options{}); err == nil ||
		!strings.Contains(err.Error(), "truncated") {
		t.Fatalf("truncation must propagate, got %v", err)
	}
	// Truncated length field itself (header promises another record).
	hdrOnly := append([]byte(nil), good[:8]...)
	binary.BigEndian.PutUint32(hdrOnly[4:8], 1)
	if _, err := metaA().Parse(bytes.NewReader(hdrOnly), Options{}); err == nil ||
		!strings.Contains(err.Error(), "truncated") {
		t.Fatalf("truncated length field must propagate, got %v", err)
	}
	bad := append([]byte(nil), good[:8]...)
	bad = append(bad, 0, 0, 0, 9) // length 9 < 20 minimum
	if _, err := metaA().Parse(bytes.NewReader(bad), Options{}); err == nil ||
		!strings.Contains(err.Error(), "illegal length") {
		t.Fatalf("illegal length must be rejected, got %v", err)
	}
}

func metaA() MetadataAdapter { return MetadataAdapter{} }
