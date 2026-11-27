package rtpreplay

import (
	"encoding/json"
	"testing"
)

func opusPayload(config, code uint8) []byte {
	return []byte{config<<3 | code, 0xAA}
}

func parsedStream(t *testing.T, tr *Trace) ParsedStream {
	t.Helper()
	b, err := json.Marshal(tr)
	if err != nil {
		t.Fatal(err)
	}
	pt, err := ParseTrace(b)
	if err != nil {
		t.Fatal(err)
	}
	return pt.Streams[0]
}

func TestBuildStreamStateCleanOpus(t *testing.T) {
	// 20ms Opus (config 1 -> 960 samples at 48kHz), packets every 20ms.
	tr := &Trace{
		Version: "1", Seed: 1,
		Clock:   TraceClock{RTPRateHz: 48000, EventRateHz: 1000},
		Streams: []Stream{{Track: "opus", Codec: CodecOpus, ClockHz: 48000, DeclaredDurationMs: 100}},
	}
	for i := 0; i < 5; i++ {
		tr.Streams[0].Packets = append(tr.Streams[0].Packets, TracePacket{
			RTPHex:    marshalRTP(t, uint16(100+i), uint32(i*960), i == 4, opusPayload(1, 0)),
			ArrivalMs: int64(20 + i*20),
		})
	}
	ss, err := BuildStreamState(parsedStream(t, tr), 8, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ss.States) != 5 {
		t.Fatalf("states=%d", len(ss.States))
	}
	for i, st := range ss.States {
		if st.MediaStartTicks != uint64(i*960) || st.DurationTicks != 960 {
			t.Fatalf("state %d: %+v", i, st)
		}
		if st.LatencyTicks != 0 {
			t.Fatalf("state %d latency=%d", i, st.LatencyTicks)
		}
		if st.DurationSource != SourceOpusTOC {
			t.Fatalf("source=%q", st.DurationSource)
		}
	}
	if ss.MediaTicks != 4800 || len(ss.Gaps) != 0 {
		t.Fatalf("media=%d gaps=%v", ss.MediaTicks, ss.Gaps)
	}
}

func TestBuildStreamStateMalformedOpusPropagates(t *testing.T) {
	tr := &Trace{
		Version: "1", Seed: 1,
		Clock:   TraceClock{RTPRateHz: 48000, EventRateHz: 1000},
		Streams: []Stream{{Track: "opus", Codec: CodecOpus, ClockHz: 48000}},
	}
	tr.Streams[0].Packets = []TracePacket{
		{RTPHex: marshalRTP(t, 1, 0, false, opusPayload(1, 0)), ArrivalMs: 20},
		{RTPHex: marshalRTP(t, 2, 960, false, nil), ArrivalMs: 40},
	}
	ss, err := BuildStreamState(parsedStream(t, tr), 8, nil)
	if err == nil {
		t.Fatalf("expected error, got states=%d", len(ss.States))
	}
	se := AsError(err)
	if se.Code != CodeDuration || se.Track != "opus" || se.PacketSeq == nil || *se.PacketSeq != 2 {
		t.Fatalf("err=%+v", se)
	}
}

func TestBuildStreamStateRejectsTimestampJump(t *testing.T) {
	tr := &Trace{
		Version: "1", Seed: 1,
		Clock:   TraceClock{RTPRateHz: 48000, EventRateHz: 1000},
		Streams: []Stream{{Track: "opus", Codec: CodecOpus, ClockHz: 48000}},
	}
	tr.Streams[0].Packets = []TracePacket{
		{RTPHex: marshalRTP(t, 1, 0, false, opusPayload(1, 0)), ArrivalMs: 20},
		{RTPHex: marshalRTP(t, 2, 5000, false, opusPayload(1, 0)), ArrivalMs: 40},
	}
	_, err := BuildStreamState(parsedStream(t, tr), 8, nil)
	if err == nil || AsError(err).Code != CodeTimestampJump {
		t.Fatalf("err=%v", err)
	}
}

func TestBuildStreamStateGapTimeline(t *testing.T) {
	tr := &Trace{
		Version: "1", Seed: 1,
		Clock:   TraceClock{RTPRateHz: 48000, EventRateHz: 1000},
		Streams: []Stream{{Track: "opus", Codec: CodecOpus, ClockHz: 48000}},
	}
	// Seqs 1,2,3 then 6,7: 4 and 5 missing. Timestamps keep advancing.
	base := uint16(4000)
	for i, seq := range []uint16{1, 2, 3, 6, 7} {
		tr.Streams[0].Packets = append(tr.Streams[0].Packets, TracePacket{
			RTPHex:    marshalRTP(t, base+seq, uint32((int(seq)-1)*960), false, opusPayload(1, 0)),
			ArrivalMs: int64(20 + i*20),
		})
	}
	ss, err := BuildStreamState(parsedStream(t, tr), 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ss.Stats.GapPackets != 2 {
		t.Fatalf("gaps=%d", ss.Stats.GapPackets)
	}
	// Media time of seq 6 must stay anchored to its RTP timestamp.
	last := ss.States[len(ss.States)-1]
	if last.MediaStartTicks != 6*960 {
		t.Fatalf("last media start=%d want %d", last.MediaStartTicks, 6*960)
	}
}

func TestBuildStreamStateDedupsDuplicate(t *testing.T) {
	tr := &Trace{
		Version: "1", Seed: 1,
		Clock:   TraceClock{RTPRateHz: 48000, EventRateHz: 1000},
		Streams: []Stream{{Track: "opus", Codec: CodecOpus, ClockHz: 48000}},
	}
	tr.Streams[0].Packets = []TracePacket{
		{RTPHex: marshalRTP(t, 1, 0, false, opusPayload(1, 0)), ArrivalMs: 20},
		{RTPHex: marshalRTP(t, 2, 960, false, opusPayload(1, 0)), ArrivalMs: 40},
		{RTPHex: marshalRTP(t, 2, 960, false, opusPayload(1, 0)), ArrivalMs: 41},
		{RTPHex: marshalRTP(t, 3, 1920, false, opusPayload(1, 0)), ArrivalMs: 60},
	}
	ss, err := BuildStreamState(parsedStream(t, tr), 8, nil)
	if err != nil {
		t.Fatal(err)
	}
	delivered := 0
	for _, st := range ss.States {
		if st.Kind == "packet" {
			delivered++
		}
	}
	if delivered != 3 || ss.Stats.Duplicates != 1 || ss.Stats.Delivered != 3 {
		t.Fatalf("delivered=%d stats=%+v", delivered, ss.Stats)
	}
}
