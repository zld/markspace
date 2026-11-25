package continuity

import (
	"bytes"
	"testing"

	"github.com/abema/go-mp4"
)

func videoTrack() TrackSpec {
	return TrackSpec{
		ID: 1, Kind: MediaVideo, Timescale: 90000, Handler: "vide",
		SampleEntry: "avc1", CodecConfig: avcCConfig(), Width: 1280, Height: 720,
	}
}

func audioTrack() TrackSpec {
	return TrackSpec{
		ID: 2, Kind: MediaAudio, Timescale: 48000, Handler: "soun",
		SampleEntry: "mp4a", CodecConfig: []byte{0x12, 0x10}, SampleRate: 48000, ChannelCount: 2,
	}
}

func TestBuildInitParsesWithGoMP4(t *testing.T) {
	data := BuildInit(InitSpec{Tracks: []TrackSpec{videoTrack(), audioTrack()}})
	boxes, err := Boxes(data)
	if err != nil {
		t.Fatalf("top-level: %v", err)
	}
	if len(boxes) != 2 || boxes[0].Type != "ftyp" || boxes[1].Type != "moov" {
		t.Fatalf("boxes=%v", boxes)
	}
	info, err := mp4.Probe(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if len(info.Tracks) != 2 {
		t.Fatalf("tracks=%d", len(info.Tracks))
	}
	if info.Tracks[0].TrackID != 1 || info.Tracks[0].Timescale != 90000 || info.Tracks[0].Codec != mp4.CodecAVC1 {
		t.Fatalf("video track=%+v", info.Tracks[0])
	}
	if info.Tracks[1].TrackID != 2 || info.Tracks[1].Timescale != 48000 || info.Tracks[1].Codec != mp4.CodecMP4A {
		t.Fatalf("audio track=%+v", info.Tracks[1])
	}
	if info.MajorBrand != mp4.StrToBoxType("iso6") {
		t.Fatalf("major brand=%v", info.MajorBrand)
	}
}

func TestBuildFragmentParsesWithGoMP4(t *testing.T) {
	data := BuildFragment(FragmentSpec{
		BaseDecodeTime: 90000,
		TrackID:        1,
		Timescale:      90000,
		Samples: []SampleSpec{
			{Data: pattern(1, 64), Duration: 3000, Sync: true},
			{Data: pattern(2, 48), Duration: 3000, CompositionOffset: 1500},
		},
	})
	boxes, err := Boxes(data)
	if err != nil {
		t.Fatalf("top-level: %v", err)
	}
	if len(boxes) != 2 || boxes[0].Type != "moof" || boxes[1].Type != "mdat" {
		t.Fatalf("boxes=%+v", boxes)
	}
	info, err := mp4.Probe(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if len(info.Segments) != 1 {
		t.Fatalf("segments=%+v", info.Segments)
	}
	seg := info.Segments[0]
	if seg.TrackID != 1 || seg.BaseMediaDecodeTime != 90000 || seg.SampleCount != 2 {
		t.Fatalf("segment=%+v", seg)
	}
}

func TestBuildFragmentDataOffsetsAreCorrect(t *testing.T) {
	samples := []SampleSpec{{Data: pattern(3, 10), Duration: 1, Sync: true}, {Data: pattern(4, 7), Duration: 1}}
	data := BuildFragment(FragmentSpec{TrackID: 1, Timescale: 1000, Samples: samples})
	boxes, err := Boxes(data)
	if err != nil {
		t.Fatal(err)
	}
	var moof, mdat *Box
	for i := range boxes {
		switch boxes[i].Type {
		case "moof":
			moof = &boxes[i]
		case "mdat":
			mdat = &boxes[i]
		}
	}
	if moof == nil || mdat == nil {
		t.Fatalf("missing moof/mdat: %+v", boxes)
	}
	// The first sample must start at moof.End()+8 (mdat header).
	trunDataOffset := scanTrunDataOffset(t, data, moof)
	if want := moof.End() + 8; moof.Offset+int64(trunDataOffset) != want {
		t.Fatalf("data offset resolves to %d want %d", moof.Offset+int64(trunDataOffset), want)
	}
	total := 0
	for _, s := range samples {
		total += len(s.Data)
	}
	if got := int(mdat.Size) - int(mdat.Header); got != total {
		t.Fatalf("mdat payload=%d want %d", got, total)
	}
}

func scanTrunDataOffset(t *testing.T, data []byte, moof *Box) int32 {
	t.Helper()
	idx := bytes.Index(moof.Payload, []byte("trun"))
	if idx < 0 {
		t.Fatal("no trun")
	}
	p := int(moof.Header) + idx + 4 + 4 + 4
	return int32(be32(data[int(moof.Offset)+p:]))
}

func TestBuildFragmentTruncation(t *testing.T) {
	full := BuildFragment(FragmentSpec{TrackID: 1, Timescale: 1000, Samples: []SampleSpec{{Data: pattern(5, 32), Duration: 1}}})
	trunc := BuildFragment(FragmentSpec{TrackID: 1, Timescale: 1000, Samples: []SampleSpec{{Data: pattern(5, 32), Duration: 1}}, TruncateBytes: 8})
	if len(trunc) != len(full)-8 {
		t.Fatalf("truncated length=%d want %d", len(trunc), len(full)-8)
	}
	if _, err := Boxes(trunc); err == nil {
		t.Fatal("truncated segment must fail box scanning")
	}
}

func TestPatternDeterministic(t *testing.T) {
	a := pattern(7, 32)
	b := pattern(7, 32)
	if !bytes.Equal(a, b) {
		t.Fatal("pattern must be deterministic")
	}
	if bytes.Equal(a, pattern(8, 32)) {
		t.Fatal("distinct seeds must produce distinct data")
	}
}
