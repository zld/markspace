package ts

import (
	"bytes"
	"testing"

	"github.com/pion/rtp"
)

// marshalRTP builds a back-to-back RTP capture from TS packets.
func marshalRTP(t *testing.T, payloads [][]byte, seqStart uint16, ssrc uint32, skip func(seq uint16) bool) []byte {
	t.Helper()
	var out bytes.Buffer
	seq := seqStart
	for _, p := range payloads {
		if skip != nil && skip(seq) {
			seq++ // lost packet: advance the counter without emitting it
			continue
		}
		pkt := &rtp.Packet{Header: rtp.Header{
			Version: 2, PayloadType: RTPPayloadTypeMP2T,
			SequenceNumber: seq, SSRC: ssrc, Timestamp: uint32(seq) * 3000,
		}, Payload: p}
		b, err := pkt.Marshal()
		if err != nil {
			t.Fatalf("marshal rtp: %v", err)
		}
		out.Write(b)
		seq++
	}
	return out.Bytes()
}

func TestRTPDepacketisesTS(t *testing.T) {
	path := writeFixture(t, 21)
	var payloads [][]byte
	if _, _, err := ReadAll(path, DefaultConfig(), func(p *Packet) error {
		payloads = append(payloads, append([]byte(nil), p.Raw[:]...))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	capture := marshalRTP(t, payloads, 0xFFF0, 0xDEADBEEF, nil)
	res, err := AnalyzeRTP(bytes.NewReader(capture))
	if err != nil {
		t.Fatalf("AnalyzeRTP: %v", err)
	}
	if res.SSRC != 0xDEADBEEF {
		t.Errorf("SSRC=0x%08x", res.SSRC)
	}
	if res.Packets != len(payloads) {
		t.Errorf("packets=%d want %d", res.Packets, len(payloads))
	}
	if len(res.SequenceGaps) != 0 {
		t.Fatalf("unexpected gaps in a clean capture: %+v", res.SequenceGaps)
	}
	// The de-packetised payload must be a contiguous run of 188-byte packets.
	if len(res.Payload)%PacketSize != 0 {
		t.Fatalf("payload %d bytes is not a multiple of %d", len(res.Payload), PacketSize)
	}
}

func TestRTPSequenceGapReported(t *testing.T) {
	path := writeFixture(t, 22)
	var payloads [][]byte
	if _, _, err := ReadAll(path, DefaultConfig(), func(p *Packet) error {
		payloads = append(payloads, append([]byte(nil), p.Raw[:]...))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Drop one RTP packet (sequence 0x1001) to simulate capture loss.
	skip := func(seq uint16) bool { return seq == 0x1001 }
	capture := marshalRTP(t, payloads[:20], 0x1000, 0x11223344, skip)
	res, err := AnalyzeRTP(bytes.NewReader(capture))
	if err != nil {
		t.Fatalf("AnalyzeRTP: %v", err)
	}
	if len(res.SequenceGaps) != 1 {
		t.Fatalf("gaps=%d, want 1", len(res.SequenceGaps))
	}
	g := res.SequenceGaps[0]
	if g.Lost != 1 || g.Reordered {
		t.Fatalf("gap=%+v, want one lost packet", g)
	}
	if g.PrevSeq != 0x1000 || g.Seq != 0x1002 {
		t.Fatalf("gap boundaries prev=%d seq=%d", g.PrevSeq, g.Seq)
	}
}

func TestRTPSequenceWrapNoGap(t *testing.T) {
	path := writeFixture(t, 23)
	var payloads [][]byte
	if _, _, err := ReadAll(path, DefaultConfig(), func(p *Packet) error {
		payloads = append(payloads, append([]byte(nil), p.Raw[:]...))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Start just below the 16-bit wrap so the sequence rolls 0xFFFF -> 0.
	capture := marshalRTP(t, payloads[:20], 0xFFFC, 0x1, nil)
	res, err := AnalyzeRTP(bytes.NewReader(capture))
	if err != nil {
		t.Fatalf("AnalyzeRTP: %v", err)
	}
	if len(res.SequenceGaps) != 0 {
		t.Fatalf("wrap reported as a gap: %+v", res.SequenceGaps)
	}
	var start uint16 = 0xFFFC
	want := start + 19 // wraps past 0xFFFF to 0x000F
	if res.LastSeq != want {
		t.Errorf("last seq=%d want %d", res.LastSeq, want)
	}
}

func TestRTPEmptyCaptureFails(t *testing.T) {
	if _, err := AnalyzeRTP(bytes.NewReader(nil)); err == nil {
		t.Fatal("empty capture accepted")
	}
}
