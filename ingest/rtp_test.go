package ingest

import (
	"bytes"
	"testing"
	"time"

	"github.com/pion/rtp"

	"rtpreplay/clock"
	"rtpreplay/internal/rtplog"
	"rtpreplay/media"
)

func buildLog(t *testing.T, pkts []*rtp.Packet, arrivals []time.Time) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := rtplog.WriteHeader(&buf); err != nil {
		t.Fatal(err)
	}
	for i, p := range pkts {
		raw, err := p.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		if err := rtplog.WriteRecord(&buf, arrivals[i], raw); err != nil {
			t.Fatal(err)
		}
	}
	return buf.Bytes()
}

func makePacket(seq uint16, ts uint32, ssrc uint32) *rtp.Packet {
	return &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    8,
			SequenceNumber: seq,
			Timestamp:      ts,
			SSRC:           ssrc,
		},
		Payload: []byte{0xAA, 0xBB},
	}
}

func TestParseRTPStreamNormal(t *testing.T) {
	const ssrc = 42
	base := time.Unix(1_700_000_000, 0).UTC()
	var pkts []*rtp.Packet
	var arrivals []time.Time
	for i := 0; i < 20; i++ {
		pkts = append(pkts, makePacket(uint16(i), uint32(i*160), ssrc))
		arrivals = append(arrivals, base.Add(time.Duration(i)*20*time.Millisecond))
	}
	data := buildLog(t, pkts, arrivals)

	s, err := ParseRTPStream(bytes.NewReader(data), int64(len(data)), media.AudioClockRate, 32)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Packets) != 20 || s.SSRC != ssrc || s.BitrateKbps != 32 {
		t.Fatalf("stream basics wrong: %+v", s)
	}
	if s.TimeBase.ClockRate != media.AudioClockRate || s.TimeBase.BaseRTP != 0 {
		t.Fatalf("time base wrong: %+v", s.TimeBase)
	}
	// 160 ticks per 20 ms at 8000 Hz is a perfect clock: drift ~ 0.
	if s.DriftPPM > 1 || s.DriftPPM < -1 {
		t.Fatalf("drift = %.4f ppm, want ~0", s.DriftPPM)
	}
	if len(s.Duplicates) != 0 || len(s.IsolatedRanges()) != 0 {
		t.Fatal("healthy stream must have no anomalies")
	}
	if len(s.HealthyPackets()) != 20 {
		t.Fatal("all packets must be healthy")
	}
}

func TestParseRTPStreamDrift(t *testing.T) {
	const ssrc = 7
	base := time.Unix(1_700_000_000, 0).UTC()
	var pkts []*rtp.Packet
	var arrivals []time.Time
	// Receiver sees a packet every second; sender advances 8001 ticks each
	// time -> exactly 125 ppm fast at 8000 Hz.
	for i := 0; i < 30; i++ {
		pkts = append(pkts, makePacket(uint16(i), uint32(i*8001), ssrc))
		arrivals = append(arrivals, base.Add(time.Duration(i) * time.Second))
	}
	data := buildLog(t, pkts, arrivals)
	s, err := ParseRTPStream(bytes.NewReader(data), int64(len(data)), media.AudioClockRate, 16)
	if err != nil {
		t.Fatal(err)
	}
	if s.DriftPPM < 124 || s.DriftPPM > 126 {
		t.Fatalf("drift = %.4f ppm, want ~125", s.DriftPPM)
	}
}

func TestParseRTPStreamDuplicateAndGap(t *testing.T) {
	const ssrc = 9
	base := time.Unix(1_700_000_000, 0).UTC()
	seqs := []uint16{0, 1, 1, 4, 5} // duplicate at record 2, gap 2-3
	var pkts []*rtp.Packet
	var arrivals []time.Time
	for i, sq := range seqs {
		pkts = append(pkts, makePacket(sq, uint32(i*160), ssrc))
		arrivals = append(arrivals, base.Add(time.Duration(i)*20*time.Millisecond))
	}
	data := buildLog(t, pkts, arrivals)
	s, err := ParseRTPStream(bytes.NewReader(data), int64(len(data)), media.AudioClockRate, 16)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Duplicates) != 1 || s.Duplicates[0] != 2 {
		t.Fatalf("duplicates = %v", s.Duplicates)
	}
	if s.Packets[3].Clock.Anomaly != clock.AnomSeqGap {
		t.Fatalf("want seq_gap, got %s", s.Packets[3].Clock.Anomaly)
	}
}

func TestParseRTPStreamSSRCChangeRejected(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	pkts := []*rtp.Packet{makePacket(0, 0, 1), makePacket(1, 160, 2)}
	var arrivals []time.Time
	for i := range pkts {
		arrivals = append(arrivals, base.Add(time.Duration(i)*20*time.Millisecond))
	}
	data := buildLog(t, pkts, arrivals)
	_, err := ParseRTPStream(bytes.NewReader(data), int64(len(data)), media.AudioClockRate, 16)
	le, ok := media.AsLocatable(err)
	if !ok || le.Code != "SSRC_CHANGED" {
		t.Fatalf("want SSRC_CHANGED, got %v", err)
	}
}

func TestParseRTPStreamMalformedPacket(t *testing.T) {
	var buf bytes.Buffer
	_ = rtplog.WriteHeader(&buf)
	if err := rtplog.WriteRecord(&buf, time.Now(), []byte{0x00, 0x01}); err != nil {
		t.Fatal(err)
	}
	_, err := ParseRTPStream(bytes.NewReader(buf.Bytes()), int64(buf.Len()), media.AudioClockRate, 16)
	le, ok := media.AsLocatable(err)
	if !ok || le.Code != "RTP_UNMARSHAL_FAILED" {
		t.Fatalf("want RTP_UNMARSHAL_FAILED, got %v", err)
	}
}

func TestParseRTPStreamUnsupportedRate(t *testing.T) {
	_, err := ParseRTPStream(bytes.NewReader(nil), 0, 44100, 16)
	if le, ok := media.AsLocatable(err); !ok || le.Kind != media.KindUnsupported {
		t.Fatalf("want unsupported error, got %v", err)
	}
}

func TestParseRTPStreamEmpty(t *testing.T) {
	var buf bytes.Buffer
	_ = rtplog.WriteHeader(&buf)
	_, err := ParseRTPStream(bytes.NewReader(buf.Bytes()), int64(buf.Len()), media.AudioClockRate, 16)
	if le, ok := media.AsLocatable(err); !ok || le.Code != "EMPTY_STREAM" {
		t.Fatalf("want EMPTY_STREAM, got %v", err)
	}
}
