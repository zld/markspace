package manifest

import (
	"bytes"
	"strings"
	"testing"
)

func sampleManifest() *Manifest {
	return &Manifest{
		Version: SchemaVersion,
		TrackID: 42,
		Identity: Identity{
			ContentHash:   "sha256:content",
			ContainerHash: "sha256:container",
			MetadataHash:  "sha256:meta",
		},
		Summary: Summary{
			Version:       SchemaVersion,
			Codec:         "H264",
			SampleCount:   2,
			KeyFrameCount: 1,
			FaultCount:    1,
			FaultsByClass: map[string]int{"missing_packet": 1},
			ClockRate:     90000,
			TimeTrusted:   true,
		},
		Samples: []SampleRow{
			{Index: 0, Codec: "H264", KeyFrame: true, FirstSeq: 0, LastSeq: 0,
				ByteOffset: 0, ByteLength: 4, TimeStart: 0, TimeEnd: 0, Duration: 3600},
			{Index: 1, Codec: "H264", FirstSeq: 1, LastSeq: 1,
				ByteOffset: 4, ByteLength: 3, TimeStart: 3600, TimeEnd: 3600, Duration: 3600},
		},
		Faults: []FaultRow{
			{Class: "missing_packet", SeqFirst: 9, SeqLast: 9,
				ByteOffset: 40, ByteLength: 1, TimeStart: 100, TimeEnd: 100},
		},
	}
}

func TestWriteReadRoundTrip(t *testing.T) {
	a := NewAdapter()
	m := sampleManifest()
	buf := &bytes.Buffer{}
	if err := a.Write(buf, m); err != nil {
		t.Fatal(err)
	}
	got, err := a.Read(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != SchemaVersion || got.TrackID != 42 {
		t.Fatalf("round trip lost header: %+v", got)
	}
	if len(got.Samples) != 2 || !got.Samples[0].KeyFrame {
		t.Fatalf("round trip lost samples: %+v", got.Samples)
	}
	// Deterministic serialization: writing twice yields identical bytes.
	buf2 := &bytes.Buffer{}
	if err := a.Write(buf2, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf.Bytes(), buf2.Bytes()) {
		t.Fatal("manifest serialization is not deterministic")
	}
}

func TestReadUpgradesV1(t *testing.T) {
	v1 := `{
	  "version": 1,
	  "track_ssrc": 7,
	  "content_id": "sha256:old",
	  "meta_id": "sha256:oldmeta",
	  "frames": [
	    {"n":0,"codec":"H265","key":true,"seq_start":0,"seq_end":0,
	     "byte_start":0,"byte_len":5,"media_start":0,"media_end":0},
	    {"n":1,"codec":"H265","key":false,"seq_start":1,"seq_end":1,
	     "byte_start":5,"byte_len":2,"media_start":3000,"media_end":3000}
	  ],
	  "errors": [
	    {"code":"GAP","seq_start":3,"seq_end":3,"byte_start":7,"byte_len":1},
	    {"code":"BADLEN","seq_start":4,"seq_end":4,"byte_start":8,"byte_len":1}
	  ],
	  "digest": {"frames":2}
	}`
	got, err := NewAdapter().Read(strings.NewReader(v1))
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != SchemaVersion {
		t.Fatalf("upgraded version = %d", got.Version)
	}
	if got.Identity.ContentHash != "sha256:old" || got.Identity.ContainerHash == "" {
		t.Fatalf("identity upgrade wrong: %+v", got.Identity)
	}
	if got.Samples[1].TimeStart != 3000 || got.Samples[1].Duration != 0 {
		t.Fatalf("media->time mapping wrong: %+v", got.Samples[1])
	}
	if got.Summary.KeyFrameCount != 1 || got.Summary.FaultCount != 2 {
		t.Fatalf("summary wrong: %+v", got.Summary)
	}
	if got.Faults[0].Class != "missing_packet" || got.Faults[1].Class != "illegal_length" {
		t.Fatalf("legacy code mapping wrong: %+v", got.Faults)
	}
	// The upgraded manifest must itself be writable and re-readable.
	buf := &bytes.Buffer{}
	if err := NewAdapter().Write(buf, got); err != nil {
		t.Fatal(err)
	}
	if _, err := NewAdapter().Read(buf); err != nil {
		t.Fatalf("upgraded manifest not reusable: %v", err)
	}
}

func TestRejectsUnsupportedAndMalformed(t *testing.T) {
	a := NewAdapter()
	for _, in := range []string{
		`{"version":99}`,
		`{"version":0}`,
		`not-json`,
	} {
		if _, err := a.Read(strings.NewReader(in)); err == nil {
			t.Fatalf("input %q accepted", in)
		}
	}
	bad := sampleManifest()
	bad.Samples[1].FirstSeq = 5
	bad.Samples[1].LastSeq = 4 // first > last is malformed
	if err := NewAdapter().Write(&bytes.Buffer{}, bad); err == nil {
		t.Fatal("invalid manifest must not serialize")
	}
}
