package rtpreplay

import "testing"

func TestManifestAlignmentExactOnCleanStream(t *testing.T) {
	ss := cleanStream(t) // 50 x 20ms = 1000ms exactly
	m, err := BuildManifest([]*StreamState{ss}, map[string]uint64{"opus": 1000})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Entries) != 1 {
		t.Fatalf("entries=%d", len(m.Entries))
	}
	e := m.Entries[0]
	if e.StartMs != 0 || e.EndMs != 1000 || e.MediaDurationMs != 1000 {
		t.Fatalf("entry=%+v", e)
	}
	if e.AlignmentErrorMs != 0 || m.AlignmentErrorMs != 0 {
		t.Fatalf("alignment=%v max=%v", e.AlignmentErrorMs, m.AlignmentErrorMs)
	}
	if e.Status != StatusOK || e.PacketCount != 50 {
		t.Fatalf("entry=%+v", e)
	}
	if e.FirstExtSeq != 1 || e.LastExtSeq != 50 {
		t.Fatalf("seq range=%d..%d", e.FirstExtSeq, e.LastExtSeq)
	}
}

func TestManifestReportsGapWindow(t *testing.T) {
	ss := burstyStream(t)
	m, err := BuildManifest([]*StreamState{ss}, map[string]uint64{"opus": 600})
	if err != nil {
		t.Fatal(err)
	}
	e := m.Entries[0]
	if e.Status != StatusGap || e.GapCount != 6 {
		t.Fatalf("entry=%+v", e)
	}
	if len(e.Gaps) != 1 {
		t.Fatalf("gaps=%+v", e.Gaps)
	}
	g := e.Gaps[0]
	if g.Count != 6 || g.StartMs != 200 || g.EndMs != 320 {
		t.Fatalf("gap=%+v", g)
	}
	if e.AlignmentErrorMs != 0 {
		t.Fatalf("alignment=%v (loss must not shift the media boundary)", e.AlignmentErrorMs)
	}
}

func TestManifestDetectsDeclaredMismatch(t *testing.T) {
	ss := cleanStream(t)
	m, err := BuildManifest([]*StreamState{ss}, map[string]uint64{"opus": 999})
	if err != nil {
		t.Fatal(err)
	}
	if m.AlignmentErrorMs != 1 {
		t.Fatalf("alignment=%v", m.AlignmentErrorMs)
	}
}

func burstyStream(t *testing.T) *StreamState {
	t.Helper()
	tr := &Trace{
		Version: "1", Seed: 1,
		Clock:   TraceClock{RTPRateHz: 48000, EventRateHz: 1000},
		Streams: []Stream{{Track: "opus", Codec: CodecOpus, ClockHz: 48000}},
	}
	for i := 0; i < 30; i++ {
		if i >= 10 && i < 16 {
			continue
		}
		tr.Streams[0].Packets = append(tr.Streams[0].Packets, TracePacket{
			RTPHex:    marshalRTP(t, uint16(1+i), uint32(i*960), false, opusPayload(1, 0)),
			ArrivalMs: int64(20 + i*20),
		})
	}
	ss, err := BuildStreamState(parsedStream(t, tr), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	return ss
}
