package timeline_test

import (
	"testing"

	pionrtp "github.com/pion/rtp"

	"mediasync/internal/audio"
	"mediasync/internal/model"
	"mediasync/internal/rtp"
	"mediasync/internal/timeline"
)

func opusPacket(t *testing.T, seq uint16, toc byte) model.RawPacket {
	t.Helper()
	pkt := &pionrtp.Packet{
		Header: pionrtp.Header{
			Version: 2, PayloadType: 111, SequenceNumber: seq, SSRC: 1,
			Timestamp: uint32(seq) * 960,
		},
		Payload: []byte{toc, 0x11, 0x22},
	}
	b, err := pkt.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return model.RawPacket{Offset: int(seq), RTP: b}
}

func build(t *testing.T, packets []model.RawPacket) *model.TrackTimeline {
	t.Helper()
	rec, err := rtp.Reconstruct("opus-track", packets)
	if err != nil {
		t.Fatal(err)
	}
	meta := model.TrackMeta{
		TrackID: "opus-track", Codec: model.CodecOpus,
		ClockRate: 48000, Channels: 2, SampleRate: 48000,
	}
	return timeline.New().Build(meta, rec, nil)
}

func TestCleanTimelineZeroDelta(t *testing.T) {
	// config 1 = 20 ms frames, code 0 => one 20 ms frame per packet.
	var pkts []model.RawPacket
	for s := uint16(0); s < 4; s++ {
		pkts = append(pkts, opusPacket(t, s, 0x08)) // config 1 code 0
	}
	tl := build(t, pkts)

	if len(tl.Failures) != 0 {
		t.Fatalf("unexpected failures: %+v", tl.Failures)
	}
	if len(tl.Entries) != 4 {
		t.Fatalf("entries=%d", len(tl.Entries))
	}
	for i, e := range tl.Entries {
		if e.Class != model.EntrySample {
			t.Fatalf("entry %d class=%s", i, e.Class)
		}
		if e.TimeDelta != 0 || e.ByteDelta != 0 {
			t.Fatalf("entry %d has non-zero delta: %+v", i, e)
		}
		if e.Start != model.NS(i)*20_000_000 {
			t.Fatalf("entry %d start=%d", i, e.Start)
		}
		if e.Duration != 20_000_000 {
			t.Fatalf("entry %d dur=%d", i, e.Duration)
		}
	}
	if tl.TotalDuration() != 80_000_000 {
		t.Fatalf("total=%d", tl.TotalDuration())
	}
}

func TestDuplicateAddsZeroTime(t *testing.T) {
	pkts := []model.RawPacket{
		opusPacket(t, 0, 0x08),
		opusPacket(t, 1, 0x08),
		opusPacket(t, 1, 0x08), // repeat
		opusPacket(t, 2, 0x08),
	}
	tl := build(t, pkts)

	var dupes int
	var dupeDur model.NS
	for _, e := range tl.Entries {
		if e.Class == model.EntryDuplicate {
			dupes++
			dupeDur += e.Duration
		}
	}
	if dupes != 1 || dupeDur != 0 {
		t.Fatalf("dupes=%d dupeDur=%d", dupes, dupeDur)
	}
	// Three unique 20 ms samples => 60 ms total real time.
	if tl.TotalDuration() != 60_000_000 {
		t.Fatalf("total=%d", tl.TotalDuration())
	}
	if tl.SampleBytes() != 3*3 {
		t.Fatalf("sample bytes must exclude duplicates: %d", tl.SampleBytes())
	}
}

func TestGapPreservedNoFabrication(t *testing.T) {
	// sequences 0, then 3 => gaps for 1 and 2.
	pkts := []model.RawPacket{
		opusPacket(t, 0, 0x08),
		opusPacket(t, 3, 0x08),
	}
	tl := build(t, pkts)
	var gaps int
	for _, e := range tl.Entries {
		if e.Class == model.EntryGap {
			gaps++
			if e.Bytes != 0 || e.SourceIndex != -1 {
				t.Fatalf("gap must carry no bytes/source: %+v", e)
			}
		}
	}
	if gaps != 2 {
		t.Fatalf("gap entries=%d", gaps)
	}
	// Gap entries occupy the timeline span but represent no real samples.
	if tl.SampleBytes() != 2*3 {
		t.Fatalf("real bytes=%d", tl.SampleBytes())
	}
}

func TestTruncatedPacketLocatedAndIsolated(t *testing.T) {
	good := []model.RawPacket{
		opusPacket(t, 0, 0x08),
		opusPacket(t, 1, 0x08),
	}
	// A packet whose Opus payload is empty: RTP parses, Opus duration fails.
	badPkt := &pionrtp.Packet{
		Header:  pionrtp.Header{Version: 2, SequenceNumber: 2, SSRC: 1},
		Payload: nil,
	}
	bb, _ := badPkt.Marshal()
	bad := model.RawPacket{Offset: 2, RTP: bb}
	after := opusPacket(t, 3, 0x08)

	tl := build(t, append(append(good, bad), after))
	if len(tl.Failures) == 0 {
		t.Fatal("expected a failure for the empty Opus packet")
	}
	found := false
	for _, f := range tl.Failures {
		if f.PacketOffset == 2 && f.Stage == model.StageOpus {
			found = true
		}
	}
	if !found {
		t.Fatalf("failure not located at packet 2: %+v", tl.Failures)
	}
	// The valid samples before and after still produce entries.
	var samples int
	for _, e := range tl.Entries {
		if e.Class == model.EntrySample && e.Failure == nil {
			samples++
		}
	}
	if samples != 3 {
		t.Fatalf("valid samples=%d, want 3", samples)
	}
}

func TestAACFromMP4Table(t *testing.T) {
	// Two 1024-sample frames at 48 kHz, sizes 5 and 9.
	info := &audio.MP4Info{
		Timescale:  48000,
		Channels:   2,
		SampleRate: 48000,
		Samples: []audio.MP4Sample{
			{Index: 0, Size: 5, DeltaNS: 21_333_333},
			{Index: 1, Size: 9, DeltaNS: 21_333_333},
		},
	}
	pkts := []model.RawPacket{
		{Offset: 0, RTP: mustMarshal(t, 10)},
		{Offset: 1, RTP: mustMarshal(t, 11)},
	}
	rec, err := rtp.Reconstruct("aac-track", pkts)
	if err != nil {
		t.Fatal(err)
	}
	meta := model.TrackMeta{TrackID: "aac-track", Codec: model.CodecAAC, SampleRate: 48000}
	tl := timeline.New().Build(meta, rec, info)
	if len(tl.Entries) != 2 || tl.Failures != nil && len(tl.Failures) != 0 {
		t.Fatalf("entries=%d failures=%+v", len(tl.Entries), tl.Failures)
	}
	if tl.Entries[0].Duration != 21_333_333 || tl.Entries[0].Bytes != 5 {
		t.Fatalf("entry0=%+v", tl.Entries[0])
	}
	if tl.Entries[1].Bytes != 9 {
		t.Fatalf("entry1=%+v", tl.Entries[1])
	}
}

func mustMarshal(t *testing.T, seq uint16) []byte {
	t.Helper()
	pkt := &pionrtp.Packet{
		Header:  pionrtp.Header{Version: 2, SequenceNumber: seq, SSRC: 7},
		Payload: []byte{0, 0x10, 0, 0, 1, 2}, // irrelevant when MP4 table drives timing
	}
	b, err := pkt.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return b
}
