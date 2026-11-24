package output

import (
	"bytes"
	"testing"

	gomp4 "github.com/abema/go-mp4"

	"medcheck/internal/adapter"
)

func testDocument() Document {
	audio := make([]Sample, 0, 3)
	for i := 0; i < 3; i++ {
		audio = append(audio, Sample{PTSNS: int64(i) * 20_000_000, DurationNS: 20_000_000, Payload: []byte{0, 1, 2, byte(i)}})
	}
	video := []Sample{{PTSNS: 0, DurationNS: 40_000_000, Payload: []byte{0, 0, 0, 1, 0x67}, Keyframe: true}}
	return Document{
		AudioSamples: audio,
		Audio: adapter.AudioSpec{Channels: 2, Layout: "stereo", SampleRate: 48000,
			ChannelMap: []string{"FL", "FR"}, Version: adapter.VersionAACV1},
		VideoSamples: video,
		Video: adapter.VideoSpec{Width: 640, Height: 360, Version: adapter.VersionH264V1,
			Color: adapter.DefaultColorPolicy().Default},
	}
}

func TestBuildValidatesWithGoMP4(t *testing.T) {
	doc := testDocument()
	data, sum, err := Build(doc)
	if err != nil {
		t.Fatal(err)
	}
	info, err := Validate(data)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(info.Tracks) != 2 {
		t.Fatalf("tracks=%d info=%+v", len(info.Tracks), info)
	}
	if sum != SHA256(data) || len(sum) != 64 {
		t.Fatal("bad checksum")
	}
}

func TestBuildIsDeterministicAndKeepsMetadata(t *testing.T) {
	doc := testDocument()
	a, sa, err := Build(doc)
	if err != nil {
		t.Fatal(err)
	}
	b, sb, err := Build(doc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) || sa != sb {
		t.Fatal("output is not deterministic")
	}
	if !bytes.Contains(a, []byte("FL")) || !bytes.Contains(a, []byte("policy:default-bt709-v1")) {
		t.Fatal("target channel map and color source are absent")
	}
	if _, err := gomp4.Probe(bytes.NewReader(a)); err != nil {
		t.Fatal(err)
	}
}
