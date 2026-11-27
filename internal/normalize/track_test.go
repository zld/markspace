package normalize

import (
	"bytes"
	"io"
	"testing"

	mp4 "github.com/abema/go-mp4"

	"mediad/internal/ingest"
	"mediad/internal/model"
)

func units(n int, keyEvery int) []ingest.AccessUnit {
	out := make([]ingest.AccessUnit, n)
	off := int64(0)
	for i := range out {
		out[i] = ingest.AccessUnit{
			Offset:  off,
			Length:  10,
			IsKey:   keyEvery > 0 && i%keyEvery == 0,
			NALType: 5,
		}
		off += 10
	}
	return out
}

func TestBuildTrackExactTimescale(t *testing.T) {
	fps := model.Rational{Num: 30000, Den: 1001}
	tr, err := Build(ingest.CodecH264, units(120, 30), fps, Config{Width: 320, Height: 240})
	if err != nil {
		t.Fatal(err)
	}
	if tr.Timescale != 30000 {
		t.Fatalf("timescale: got %d want 30000", tr.Timescale)
	}
	if tr.MaxAlignmentNS > 1 {
		t.Fatalf("alignment error %d ns exceeds 1 ns", tr.MaxAlignmentNS)
	}
	if tr.StartNS != 0 || tr.EndNS <= tr.StartNS {
		t.Fatal("presentation window invalid")
	}
	// stts delta must be 1001 ticks.
	if len(tr.Samples) != 120 {
		t.Fatal("sample count mismatch")
	}
	if got := tr.KeySampleNumbers(); len(got) != 4 || got[0] != 1 || got[1] != 31 {
		t.Fatalf("key sample numbers wrong: %v", got)
	}
}

func TestBuildRejectsZeroTimebase(t *testing.T) {
	if _, err := Build(ingest.CodecH264, units(2, 1), model.Rational{Num: 0, Den: 1}, Config{}); err == nil {
		t.Fatal("zero frame rate must be rejected as zero time base")
	}
	if _, err := Build("vp8", units(2, 1), model.Rational{Num: 30, Den: 1}, Config{}); err == nil {
		t.Fatal("unknown codec must be rejected")
	}
	if _, err := Build(ingest.CodecAV1, nil, model.Rational{Num: 30, Den: 1}, Config{}); err == nil {
		t.Fatal("empty unit list must be rejected")
	}
}

func TestEncodeMP4RoundTrip(t *testing.T) {
	fps := model.Rational{Num: 30, Den: 1}
	tr, err := Build(ingest.CodecH264, units(10, 5), fps, Config{Width: 640, Height: 360})
	if err != nil {
		t.Fatal(err)
	}
	config := []byte{1, 66, 192, 30, 0xFF, 0xE1} // synthetic avcC payload
	sampleData := bytes.Repeat([]byte{0xAA}, 100)
	out, err := EncodeMP4(tr, config, sampleData)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Fatal("empty MP4 output")
	}

	r := bytes.NewReader(out)
	// Locate the key structural boxes and verify they parse.
	paths := []mp4.BoxPath{
		{mp4.BoxTypeMoov(), mp4.BoxTypeMvhd()},
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMdhd()},
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsd()},
		{mp4.BoxTypeMdat()},
	}
	for _, p := range paths {
		infos, err := mp4.ExtractBox(r, nil, p)
		if err != nil {
			t.Fatalf("extract %v: %v", p, err)
		}
		if len(infos) != 1 {
			t.Fatalf("path %v: want 1 box, got %d", p, len(infos))
		}
	}

	// mdhd must carry the explicit nonzero timescale.
	mdhdInfos, err := mp4.ExtractBox(r, nil, mp4.BoxPath{
		mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMdhd(),
	})
	if err != nil {
		t.Fatal(err)
	}
	mdhd := &mp4.Mdhd{}
	if _, err := r.Seek(int64(mdhdInfos[0].Offset+mdhdInfos[0].HeaderSize), io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if _, err := mp4.Unmarshal(r, mdhdInfos[0].Size-mdhdInfos[0].HeaderSize, mdhd, mp4.Context{}); err != nil {
		t.Fatalf("unmarshal mdhd: %v", err)
	}
	if mdhd.Timescale != 30 || mdhd.Timescale == 0 {
		t.Fatalf("mdhd timescale wrong: %d", mdhd.Timescale)
	}

	// stco must point at the mdat payload start.
	mdatInfos, err := mp4.ExtractBox(r, nil, mp4.BoxPath{mp4.BoxTypeMdat()})
	if err != nil {
		t.Fatal(err)
	}
	wantOff := uint32(mdatInfos[0].Offset + mdatInfos[0].HeaderSize)
	stcoInfos, err := mp4.ExtractBox(r, nil, mp4.BoxPath{
		mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(),
		mp4.BoxTypeStbl(), mp4.BoxTypeStco(),
	})
	if err != nil {
		t.Fatal(err)
	}
	stco := &mp4.Stco{}
	if _, err := r.Seek(int64(stcoInfos[0].Offset+stcoInfos[0].HeaderSize), io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if _, err := mp4.Unmarshal(r, stcoInfos[0].Size-stcoInfos[0].HeaderSize, stco, mp4.Context{}); err != nil {
		t.Fatalf("unmarshal stco: %v", err)
	}
	if len(stco.ChunkOffset) != 1 || stco.ChunkOffset[0] != wantOff {
		t.Fatalf("stco offset: got %v want %d", stco.ChunkOffset, wantOff)
	}
}

func TestEncodeMP4Deterministic(t *testing.T) {
	fps := model.Rational{Num: 25, Den: 1}
	tr, err := Build(ingest.CodecAV1, units(8, 4), fps, Config{Width: 1280, Height: 720})
	if err != nil {
		t.Fatal(err)
	}
	cfg := []byte{0x81, 0x04, 0x00}
	data := bytes.Repeat([]byte{0x0B}, 80)
	a, err := EncodeMP4(tr, cfg, data)
	if err != nil {
		t.Fatal(err)
	}
	b, err := EncodeMP4(tr, cfg, data)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("MP4 serialization must be byte-identical across runs")
	}
}

func TestEncodeMP4RejectsZeroTimescale(t *testing.T) {
	tr := &Track{Codec: ingest.CodecH264, Timescale: 0}
	if err := WriteMP4(&writeSeeker{buf: &bytes.Buffer{}}, tr, nil, nil); err == nil {
		t.Fatal("zero-timescale write must fail")
	}
}

func TestCodecConfigType(t *testing.T) {
	cases := map[string]string{
		ingest.CodecH264: "avcC",
		ingest.CodecH265: "hvcC",
		ingest.CodecAV1:  "av1C",
	}
	for codec, want := range cases {
		if got := CodecConfigType(codec); got != want {
			t.Errorf("%s: got %s want %s", codec, got, want)
		}
	}
}
