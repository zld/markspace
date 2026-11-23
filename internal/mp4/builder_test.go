package mp4

import (
	"bytes"
	"crypto/sha256"
	"io"
	"testing"

	"mediatool/internal/timeline"
)

func TestFragmentBuildParseRoundTrip(t *testing.T) {
	payload := []byte("compressed-sample-bytes-0123456789x") // 35 bytes
	sizes := []uint32{20, 15}
	spec := FragmentSpec{
		Seq:             5,
		TrackID:         3,
		BaseMediaDecode: 123456,
		SampleDurations: []uint32{3003, 3003},
		SampleSizes:     sizes,
		SamplePayload:   payload,
	}
	data := BuildFragment(spec)

	frag, err := ParseFragment(bytes.NewReader(data), 0)
	if err != nil || frag.Rejected {
		t.Fatalf("round-trip parse failed: err=%v errs=%+v", err, frag.Errors)
	}
	if frag.Seq != 5 || len(frag.Runs) != 1 {
		t.Fatalf("header wrong: seq=%d runs=%d", frag.Seq, len(frag.Runs))
	}
	run := frag.Runs[0]
	if run.TrackID != 3 || run.BaseMediaDecode != 123456 {
		t.Fatalf("traf header wrong: %+v", run)
	}
	if len(run.SampleSizes) != 2 || run.SampleSizes[0] != 20 || run.SampleSizes[1] != 15 {
		t.Fatalf("trun sizes wrong: %v", run.SampleSizes)
	}
	if run.SampleDurations[0] != 3003 || run.SampleDurations[1] != 3003 {
		t.Fatalf("trun durations wrong: %v", run.SampleDurations)
	}
	// The whole payload must survive byte-identical.
	if !bytes.Equal(frag.MdatPayload, payload) {
		t.Fatal("mdat payload changed during round trip")
	}
	if sha256.Sum256(frag.MdatPayload) != sha256.Sum256(payload) {
		t.Fatal("payload hash mismatch")
	}
	// Individual sample slices stay addressable.
	s0, _, err := frag.SamplePayload(0, 0)
	if err != nil || string(s0) != "compressed-sample-by" {
		t.Fatalf("sample 0 wrong: %q err=%v", s0, err)
	}
	s1, _, err := frag.SamplePayload(0, 1)
	if err != nil || string(s1) != "tes-0123456789x" {
		t.Fatalf("sample 1 wrong: %q err=%v", s1, err)
	}
	// data-offset must point at the mdat payload start.
	if !run.HasDataOffset || run.DataOffset <= 0 {
		t.Fatalf("trun data offset missing: %+v", run)
	}
}

func TestFragmentDataOffsetPointsAtMdat(t *testing.T) {
	spec := FragmentSpec{
		Seq: 0, TrackID: 1, BaseMediaDecode: 0,
		SampleDurations: []uint32{100},
		SampleSizes:     []uint32{4},
		SamplePayload:   []byte("abcd"),
	}
	data := BuildFragment(spec)
	// Find the mdat header and confirm trun data-offset lands on its payload.
	var mdatHdr BoxHeader
	if err := ScanBoxes(bytes.NewReader(data), 0, MaxFragmentSize,
		func(b BoxHeader, r io.Reader) error {
			if b.Type.String() == "mdat" {
				mdatHdr = b
			}
			return nil
		}); err != nil {
		t.Fatal(err)
	}
	frag, _ := ParseFragment(bytes.NewReader(data), 0)
	moofSize := mdatHdr.Offset
	if int64(frag.Runs[0].DataOffset) != int64(moofSize)+8 {
		t.Fatalf("data offset %d != mdat payload %d",
			frag.Runs[0].DataOffset, moofSize+8)
	}
}

func TestInitBuildParseRoundTrip(t *testing.T) {
	tracks := []TrackInfo{
		{TrackID: 1, Timescale: 90000, MediaType: "video", CodecKey: "avc1"},
		{TrackID: 2, Timescale: 48000, MediaType: "audio", CodecKey: "mp4a"},
	}
	data := BuildInit(tracks)
	got, err := ParseInit(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("init round trip failed: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("tracks = %d, want 2", len(got))
	}
	if got[0].TrackID != 1 || got[0].Timescale != 90000 ||
		got[0].MediaType != "video" || got[0].CodecKey != "avc1" {
		t.Fatalf("video track round trip wrong: %+v", got[0])
	}
	if got[1].TrackID != 2 || got[1].Timescale != 48000 ||
		got[1].MediaType != "audio" || got[1].CodecKey != "mp4a" {
		t.Fatalf("audio track round trip wrong: %+v", got[1])
	}
}

func TestBuildBoxRejectsOversize(t *testing.T) {
	defer func() {
		r := recover()
		me, ok := r.(*timeline.MediaError)
		if !ok || me.Class != timeline.ClassOverflow {
			t.Fatalf("want overflow panic, got %v", r)
		}
	}()
	checkBoxSize(0xFFFFFFFF) // header would not fit in uint32 length
}
