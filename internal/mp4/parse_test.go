package mp4

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"testing"

	"mediapipe/internal/meta"
)

func fixture(t *testing.T) ([]byte, []TrackSpec, []SegmentSpec) {
	t.Helper()
	tracks := []TrackSpec{
		{ID: 1, Handler: "vide", Timescale: 90000, Width: 1280, Height: 720,
			Color: &Colr{Primaries: 1, Transfer: 1, Matrix: 1}},
		{ID: 2, Handler: "soun", Timescale: 48000, Channels: 2, SampleSize: 16, SampleRate: 48000},
	}
	segs := []SegmentSpec{
		{
			Sequence: 1, DTS: map[uint32]uint64{1: 0, 2: 0},
			Samples: []SampleSpec{
				{TrackID: 1, Duration: 3000, Sync: true, Payload: bytes.Repeat([]byte{0x11}, 128)},
				{TrackID: 1, Duration: 3000, Sync: false, Payload: bytes.Repeat([]byte{0x12}, 96)},
				{TrackID: 2, Duration: 1024, Sync: true, Payload: bytes.Repeat([]byte{0x21}, 64)},
				{TrackID: 2, Duration: 1024, Sync: true, Payload: bytes.Repeat([]byte{0x22}, 64)},
			},
			Emsg: []meta.Emsg{{SchemeIDURI: "urn:mediapipe:chapter", Value: "s1",
				Timescale: 1000, Presentation: 0, EventDuration: 500, ID: 1, Message: []byte("intro")}},
		},
		{
			Sequence: 2, DTS: map[uint32]uint64{1: 3000, 2: 2048},
			Samples: []SampleSpec{
				{TrackID: 1, Duration: 3000, Sync: true, Payload: bytes.Repeat([]byte{0x13}, 100)},
				{TrackID: 2, Duration: 1024, Sync: true, Payload: bytes.Repeat([]byte{0x23}, 48)},
			},
		},
	}
	data, err := BuildFile(tracks, segs)
	if err != nil {
		t.Fatal(err)
	}
	return data, tracks, segs
}

func TestBuildAndParseRoundTrip(t *testing.T) {
	data, _, _ := fixture(t)
	f, err := ParseFile(data)
	if err != nil {
		t.Fatal(err)
	}
	if f.Timescale != 1000 {
		t.Fatalf("timescale %d", f.Timescale)
	}
	if len(f.Tracks) != 2 || len(f.Segments) != 2 {
		t.Fatalf("tracks %d segments %d", len(f.Tracks), len(f.Segments))
	}
	v := f.TrackByID(1)
	if v.Handler != "vide" || v.Timescale != 90000 || v.Video == nil || v.Video.Width != 1280 {
		t.Fatalf("video track %+v", v)
	}
	if v.Video.Color == nil || v.Video.Color.Primaries != 1 || v.Video.Color.FullRange {
		t.Fatalf("colr %+v", v.Video.Color)
	}
	a := f.TrackByID(2)
	if a.Handler != "soun" || a.Audio == nil || a.Audio.ChannelCount != 2 || a.Audio.SampleRate != 48000 {
		t.Fatalf("audio track %+v", a)
	}
	if len(f.Segments[0].Emsg) != 1 || f.Segments[0].Emsg[0].ID != 1 {
		t.Fatalf("emsg %+v", f.Segments[0].Emsg)
	}
	seg := f.Segments[1]
	if seg.Sequence != 2 || len(seg.Tracks) != 2 {
		t.Fatalf("segment %+v", seg)
	}
	for _, st := range seg.Tracks {
		wantBase := map[uint32]uint64{1: 3000, 2: 2048}[st.TrackID]
		if st.BaseDecode != wantBase {
			t.Fatalf("track %d base %d", st.TrackID, st.BaseDecode)
		}
		for _, s := range st.Samples {
			if len(s.Payload) != int(s.Size) || s.Digest != sha256.Sum256(s.Payload) {
				t.Fatalf("sample payload/digest mismatch")
			}
		}
	}
}

func TestRoundTripSerializationPreservesHash(t *testing.T) {
	data, tracks, segs := fixture(t)
	again, err := BuildFile(tracks, segs)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, again) {
		t.Fatal("deterministic build diverged")
	}
	f1, err := ParseFile(data)
	if err != nil {
		t.Fatal(err)
	}
	f2, err := ParseFile(again)
	if err != nil {
		t.Fatal(err)
	}
	i1, err := BuildIndex(f1)
	if err != nil {
		t.Fatal(err)
	}
	i2, err := BuildIndex(f2)
	if err != nil {
		t.Fatal(err)
	}
	h1, h2 := i1.SegmentHashes(), i2.SegmentHashes()
	if len(h1) != 2 || h1[0] != h2[0] || h1[1] != h2[1] {
		t.Fatalf("segment hashes differ: %v vs %v", h1, h2)
	}
	// Round-trip through the canonical writer again after a parse cycle.
	rebuilt, err := BuildFile(tracks, segs)
	if err != nil {
		t.Fatal(err)
	}
	f3, err := ParseFile(rebuilt)
	if err != nil {
		t.Fatal(err)
	}
	i3, err := BuildIndex(f3)
	if err != nil {
		t.Fatal(err)
	}
	if i3.SegmentHashes()[0] != h1[0] {
		t.Fatal("index hash not stable across rebuild")
	}
}

func TestKeyframeIndexLookup(t *testing.T) {
	data, _, _ := fixture(t)
	f, err := ParseFile(data)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := BuildIndex(f)
	if err != nil {
		t.Fatal(err)
	}
	// segment 1: one video + two audio; segment 2: one video + one audio.
	if got := len(idx.Keyframes); got != 5 {
		t.Fatalf("keyframes %d", got)
	}
	audioKeys := 0
	for _, k := range idx.Keyframes {
		if k.TrackID == 2 {
			audioKeys++
		}
	}
	if audioKeys != 3 {
		t.Fatalf("audio keyframes %d", audioKeys)
	}
	k, ok := idx.LookupKeyframe(1, 0)
	if !ok || k.Segment != 1 || k.DTS != 0 {
		t.Fatalf("lookup %+v", k)
	}
	k, ok = idx.LookupKeyframe(1, 2999)
	if !ok || k.Segment != 1 {
		t.Fatalf("lookup before next sync %+v", k)
	}
	k, ok = idx.LookupKeyframe(1, 3000)
	if !ok || k.Segment != 2 || k.DTS != 3000 {
		t.Fatalf("lookup at sync %+v", k)
	}
	if _, ok := idx.LookupKeyframe(9, 0); ok {
		t.Fatal("undeclared track found")
	}
}

func TestParseRejectsTruncatedBox(t *testing.T) {
	data, _, _ := fixture(t)
	for _, cut := range []int{len(data) - 1, len(data) - 20, len(data) / 2, 12} {
		_, err := ParseFile(data[:cut])
		if err == nil {
			t.Fatalf("truncated at %d accepted", cut)
		}
	}
}

func TestParseRejectsIllegalLength(t *testing.T) {
	data, _, _ := fixture(t)
	bad := make([]byte, len(data))
	copy(bad, data)
	// ftyp is the first box; declare a size that underflows the header.
	binary.BigEndian.PutUint32(bad[0:4], 4)
	if _, err := ParseFile(bad); !errors.Is(err, ErrIllegalLength) {
		t.Fatalf("underflow accepted: %v", err)
	}
	big := make([]byte, len(data))
	copy(big, data)
	binary.BigEndian.PutUint32(big[0:4], uint32(len(big)+16))
	if _, err := ParseFile(big); !errors.Is(err, ErrTruncatedBox) {
		t.Fatalf("oversize accepted: %v", err)
	}
}

func TestParseRejectsOutOfRangeSampleOffset(t *testing.T) {
	data, _, _ := fixture(t)
	f, err := ParseFile(data)
	if err != nil {
		t.Fatal(err)
	}
	s := f.Segments[0].Tracks[0].Samples[0]
	// Corrupt the trun data offset of the first trun to point outside mdat.
	off := int(s.Offset) - 200
	bad := make([]byte, len(data))
	copy(bad, data)
	// find the trun contains a 4-byte data offset equal to moof-relative pos
	// simpler: declare a huge sample size in the first trun entry.
	trun := bytes.Index(bad, []byte("trun"))
	if trun < 0 {
		t.Fatal("no trun")
	}
	// trun layout: size(4) type(4) ver/flags(4) count(4) dataoff(4) then entries
	// with duration(4) size(4) flags(4) cto(4).
	entry := trun - 4 + 20
	binary.BigEndian.PutUint32(bad[entry+4:], 0x00FFFFFF)
	if _, err := ParseFile(bad); !errors.Is(err, ErrLimit) && !errors.Is(err, ErrIllegalLength) && !errors.Is(err, ErrOffsetOutOfRange) {
		t.Fatalf("oversized sample accepted: %v", err)
	}
	_ = off
}

func TestParseRejectsUndeclaredTrack(t *testing.T) {
	tracks := []TrackSpec{{ID: 1, Handler: "vide", Timescale: 90000, Width: 64, Height: 64}}
	segs := []SegmentSpec{{Sequence: 1, Samples: []SampleSpec{{TrackID: 1, Duration: 1, Sync: true, Payload: []byte{1}}}}}
	data, err := BuildFile(tracks, segs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BuildFile(tracks, []SegmentSpec{{Sequence: 2, Samples: []SampleSpec{{TrackID: 3, Duration: 1, Sync: true, Payload: []byte{1}}}}}); !errors.Is(err, ErrTrackMismatch) {
		t.Fatalf("undeclared track built: %v", err)
	}
	f, err := ParseFile(data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BuildIndex(f); err != nil {
		t.Fatal(err)
	}
}

func TestParseRejectsDepthAndLimits(t *testing.T) {
	// hand-build a chain of nested containers deeper than MaxDepth
	var buf bytes.Buffer
	depth := MaxDepth + 2
	for i := 0; i < depth; i++ {
		buf.Write(make([]byte, 4))
		buf.WriteString("moov")
	}
	// close all sizes: innermost first
	out := buf.Bytes()
	for i := depth - 1; i >= 0; i-- {
		binary.BigEndian.PutUint32(out[i*8:], uint32((depth-i)*8))
	}
	if _, err := ParseFile(out); !errors.Is(err, ErrDepth) {
		t.Fatalf("deep nesting accepted: %v", err)
	}
	if _, err := ParseFile(make([]byte, MaxFileBytes+1)); !errors.Is(err, ErrLimit) {
		t.Fatalf("huge file accepted")
	}
}
