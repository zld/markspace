package mp4writer_test

import (
	"bytes"
	"testing"

	"github.com/abema/go-mp4"

	"media-check/internal/fault"
	"media-check/internal/fixture"
	"media-check/internal/mp4writer"
	"media-check/internal/rtcp"
	rtpbuf "media-check/internal/rtp"
	"media-check/internal/stream"
)

func buildInputs(t *testing.T) ([]mp4writer.TrackInput, *stream.Report) {
	t.Helper()
	fm := fault.NewModel()
	s, err := fixture.Build(17, fixture.DefaultVideoConfig(), fixture.DefaultAudioConfig(), fixture.DamageConfig{})
	if err != nil {
		t.Fatal(err)
	}
	mapper := rtcp.NewTimeMapper(fm)
	for _, r := range s.Reports {
		if err := mapper.AddReport(r); err != nil {
			t.Fatal(err)
		}
	}
	meta := map[uint32]stream.TrackMeta{}
	for _, c := range s.Tracks {
		meta[c.SSRC] = stream.TrackMeta{
			TrackID: c.ID, Kind: c.Kind, ClockRate: c.ClockRate, KeyEvery: c.KeyEvery,
		}
	}
	buffers := map[uint32]*rtpbuf.ReorderBuffer{}
	var ordered []rtpbuf.OrderedPacket
	for _, rp := range s.Packets {
		b, ok := buffers[rp.Packet.SSRC]
		if !ok {
			b = rtpbuf.NewReorderBuffer(rp.TrackID, rp.Packet.SSRC, rtpbuf.DefaultWindowSize, fm)
			buffers[rp.Packet.SSRC] = b
		}
		ordered = append(ordered, b.Push(rp.Packet)...)
	}
	for _, b := range buffers {
		ordered = append(ordered, b.Flush()...)
	}
	rep, err := stream.NewInspector(mapper, fm, meta).Inspect(ordered)
	if err != nil {
		t.Fatal(err)
	}
	// Map ordered payloads back per track for mdat.
	byTrack := map[uint32][][]byte{}
	for _, op := range ordered {
		byTrack[op.Packet.SSRC] = append(byTrack[op.Packet.SSRC], op.Packet.Payload)
	}
	var inputs []mp4writer.TrackInput
	for _, c := range s.Tracks {
		tr := rep.ByTrackID(c.ID)
		in := mp4writer.TrackInput{Report: tr, SampleData: byTrack[c.SSRC]}
		if c.Kind == fixture.TrackVideo {
			in.Width, in.Height = 320, 240
		} else {
			in.Channels, in.SampleRate = 2, c.ClockRate
		}
		inputs = append(inputs, in)
	}
	return inputs, rep
}

func TestWriteAndProbeRoundTrip(t *testing.T) {
	inputs, rep := buildInputs(t)
	data, err := mp4writer.Write(inputs)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Fatal("mp4 output empty")
	}
	info, err := mp4.Probe(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("probe generated mp4: %v", err)
	}
	if len(info.Tracks) != 2 {
		t.Fatalf("probed tracks = %d, want 2", len(info.Tracks))
	}
	var sawVideo, sawAudio bool
	for _, tr := range info.Tracks {
		switch tr.Codec {
		case mp4.CodecAVC1:
			sawVideo = true
			if tr.AVC == nil || tr.AVC.Width != 320 || tr.AVC.Height != 240 {
				t.Fatalf("video config wrong: %+v", tr.AVC)
			}
			if uint32(len(tr.Samples)) != uint32(len(rep.VideoTrack().Samples)) {
				t.Fatalf("video sample count = %d, want %d", len(tr.Samples), len(rep.VideoTrack().Samples))
			}
		case mp4.CodecMP4A:
			sawAudio = true
			if tr.MP4A == nil || tr.MP4A.ChannelCount != 2 {
				t.Fatalf("audio config wrong: %+v", tr.MP4A)
			}
			if uint32(len(tr.Samples)) != uint32(len(rep.AudioTrack().Samples)) {
				t.Fatalf("audio sample count = %d, want %d", len(tr.Samples), len(rep.AudioTrack().Samples))
			}
		default:
			t.Fatalf("unexpected codec: %v", tr.Codec)
		}
		if len(tr.Chunks) != 1 {
			t.Fatalf("track %d chunks = %d, want 1", tr.TrackID, len(tr.Chunks))
		}
	}
	if !sawVideo || !sawAudio {
		t.Fatal("both AVC1 and MP4A tracks must be present")
	}
}

func TestDeterministicOutput(t *testing.T) {
	inputs, _ := buildInputs(t)
	a, err := mp4writer.Write(inputs)
	if err != nil {
		t.Fatal(err)
	}
	b, err := mp4writer.Write(inputs)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("same inputs must produce byte-identical MP4 output")
	}
}
