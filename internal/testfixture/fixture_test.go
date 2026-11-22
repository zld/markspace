package testfixture

import (
	"bytes"
	"testing"

	mp4 "github.com/abema/go-mp4"
	"github.com/pion/rtp"
)

func TestGenerateMP4IsProbeableWithKeyframes(t *testing.T) {
	spec := MP4Spec{
		Seed: 73, Timescale: 48000, SampleCount: 12, SampleDelta: 960,
		SampleSize: 200, KeyframeEvery: 4, StartOffset: 4800,
	}
	data, err := GenerateMP4(spec)
	if err != nil {
		t.Fatalf("GenerateMP4: %v", err)
	}
	info, err := mp4.Probe(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if len(info.Tracks) != 1 {
		t.Fatalf("tracks = %d, want 1", len(info.Tracks))
	}
	tr := info.Tracks[0]
	if tr.Timescale != 48000 || len(tr.Samples) != 12 {
		t.Fatalf("track ts=%d samples=%d", tr.Timescale, len(tr.Samples))
	}
	if tr.Samples[0].Size != 200 {
		t.Fatalf("sample size = %d, want 200", tr.Samples[0].Size)
	}
	// stss keyframes must be recoverable independently of Probe.
	keys, err := extractKeyframes(data)
	if err != nil {
		t.Fatalf("extractKeyframes: %v", err)
	}
	wantKeys := []uint32{1, 5, 9}
	if len(keys) != len(wantKeys) {
		t.Fatalf("keyframes = %v, want %v", keys, wantKeys)
	}
	for i := range wantKeys {
		if keys[i] != wantKeys[i] {
			t.Fatalf("keyframes = %v, want %v", keys, wantKeys)
		}
	}
}

func TestGenerateMP4Deterministic(t *testing.T) {
	spec := MP4Spec{Seed: 73, Timescale: 90000, SampleCount: 6, SampleDelta: 3000, SampleSize: 64, KeyframeEvery: 2}
	a, err := GenerateMP4(spec)
	if err != nil {
		t.Fatal(err)
	}
	b, err := GenerateMP4(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("same seed must produce byte-identical MP4")
	}
}

func TestGenerateMP4WithoutStss(t *testing.T) {
	spec := MP4Spec{Seed: 1, Timescale: 48000, SampleCount: 4, SampleDelta: 960, SampleSize: 100}
	data, err := GenerateMP4(spec)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := extractKeyframes(data)
	if err != nil {
		t.Fatalf("extractKeyframes: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("expected no stss, got %v", keys)
	}
}

func TestRTPCaptureRoundTrip(t *testing.T) {
	spec := RTPSpec{Seed: 73, ClockRate: 48000, PacketCount: 20, BaseSeq: 100, BaseTS: 4800, PayloadSize: 160}
	packets, err := GenerateRTP(spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(packets) != 20 {
		t.Fatalf("packets = %d", len(packets))
	}
	raw := Flatten(packets)
	split, err := SplitFlattened(raw)
	if err != nil {
		t.Fatalf("SplitFlattened: %v", err)
	}
	if len(split) != 20 {
		t.Fatalf("split count = %d", len(split))
	}
	var p rtp.Packet
	if err := p.Unmarshal(split[0]); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.SequenceNumber != 100 || p.Timestamp != 4800 {
		t.Fatalf("first packet seq=%d ts=%d", p.SequenceNumber, p.Timestamp)
	}
	if err := p.Unmarshal(split[1]); err != nil {
		t.Fatal(err)
	}
	if p.SequenceNumber != 101 || p.Timestamp != 4800+48000/50 {
		t.Fatalf("second packet seq=%d ts=%d", p.SequenceNumber, p.Timestamp)
	}
}

func TestSplitFlattenedRejectsTruncation(t *testing.T) {
	spec := RTPSpec{Seed: 1, ClockRate: 48000, PacketCount: 3, PayloadSize: 50}
	packets, _ := GenerateRTP(spec)
	raw := Flatten(packets)
	if _, err := SplitFlattened(raw[:len(raw)-3]); err == nil {
		t.Fatal("truncated capture must be rejected")
	}
}

func TestCorruptMP4ShrinksButKeepsMoov(t *testing.T) {
	spec := MP4Spec{Seed: 5, Timescale: 48000, SampleCount: 8, SampleDelta: 960, SampleSize: 300, KeyframeEvery: 2}
	data, _ := GenerateMP4(spec)
	corrupt := CorruptMP4(data)
	if len(corrupt) >= len(data) {
		t.Fatal("corrupt fixture must be shorter")
	}
	// moov precedes mdat, so the index boxes remain inside the retained prefix.
	if _, err := mp4.Probe(bytes.NewReader(corrupt)); err != nil {
		t.Fatalf("index must remain parseable after tail truncation: %v", err)
	}
}

// extractKeyframes reads stss sample numbers directly via box extraction.
func extractKeyframes(data []byte) ([]uint32, error) {
	r := bytes.NewReader(data)
	paths := []mp4.BoxPath{{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(),
		mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStss()}}
	bis, err := mp4.ExtractBoxesWithPayload(r, nil, paths)
	if err != nil {
		return nil, err
	}
	if len(bis) == 0 {
		return nil, nil
	}
	stss, ok := bis[0].Payload.(*mp4.Stss)
	if !ok {
		return nil, nil
	}
	return stss.SampleNumber, nil
}
