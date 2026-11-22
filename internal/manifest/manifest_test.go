package manifest

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"testing"

	"github.com/pion/rtp"

	"mediacore/internal/audio"
	"mediacore/internal/core"
	"mediacore/internal/timeline"
)

func testBuffer(t *testing.T, frames, rate int) *audio.Buffer {
	t.Helper()
	b, err := audio.NewBuffer(audio.Stereo, frames)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < frames; i++ {
		v := 0.25 * math.Sin(2*math.Pi*440*float64(i)/float64(rate))
		b.Data[i*2] = v
		b.Data[i*2+1] = v * 0.8
	}
	return b
}

func testConfig(rate int) Config {
	return Config{
		File: "fixture.mp4", TargetKbps: 128, SampleRate: rate, Channels: 2,
		PacketMs: 20, PayloadType: 96, SSRC: 0x0BADCAFE,
	}
}

func TestPayloadBytesHitsTargetBitrate(t *testing.T) {
	// 128 kbps at 48 kHz over 20 ms is 960 frames and exactly 320 bytes.
	// Include the packet header so the budget reflects the real wire size.
	got, err := payloadBytes(testConfig(48000), 960)
	if err != nil {
		t.Fatal(err)
	}
	if got != 320 {
		t.Fatalf("bytes = %d, want 320", got)
	}
	// 64 kbps at 44.1 kHz over 20 ms is 882 frames and exactly 160 bytes.
	cfg := testConfig(44100)
	cfg.TargetKbps = 64
	got, err = payloadBytes(cfg, 882)
	if err != nil {
		t.Fatal(err)
	}
	if got != 160 {
		t.Fatalf("bytes = %d, want 160", got)
	}
}

func TestShapePayloadIsDeterministicAndSized(t *testing.T) {
	b := testBuffer(t, 960, 48000)
	a, err := Shape(b, 128, 48000)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Shape(b, 128, 48000)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 320 {
		t.Fatalf("payload = %d bytes, want 320", len(a))
	}
	for i := range a {
		if a[i] != c[i] {
			t.Fatalf("payload differs at %d", i)
		}
	}
	if binary.BigEndian.Uint16(a[0:]) != 960 {
		t.Fatalf("frame header = %d", binary.BigEndian.Uint16(a[0:]))
	}
	if binary.BigEndian.Uint16(a[2:]) != 480 {
		t.Fatalf("rate header = %d", binary.BigEndian.Uint16(a[2:]))
	}
	if a[4] != 2 {
		t.Fatalf("channel header = %d", a[4])
	}
}

func TestBuilderProducesOrderedPacketsAndBitrate(t *testing.T) {
	rate := 48000
	bufs := []*audio.Buffer{testBuffer(t, 9600, rate)}
	b, err := New(testConfig(rate), audio.Stereo)
	if err != nil {
		t.Fatal(err)
	}
	for _, buf := range bufs {
		if err := b.AddSegment(Segment{Audio: buf, StartTicks: 0}); err != nil {
			t.Fatal(err)
		}
	}
	m, err := b.Finalize(200)
	if err != nil {
		t.Fatal(err)
	}
	if m.PacketCount != 10 {
		t.Fatalf("packets = %d, want 10", m.PacketCount)
	}
	for i, p := range m.Packets {
		if int(p.Sequence) != i {
			t.Fatalf("packet %d sequence = %d", i, p.Sequence)
		}
		if p.Timestamp != uint32(i*960) {
			t.Fatalf("packet %d timestamp = %d", i, p.Timestamp)
		}
		if p.Bytes != 320 {
			t.Fatalf("packet %d bytes = %d", i, p.Bytes)
		}
	}
	if math.Abs(m.BitrateErrorPct) > 5 {
		t.Fatalf("bitrate error %0.3f%% exceeds 5%% (actual %0.2f)", m.BitrateErrorPct, m.ActualKbps)
	}
	if m.ValidMs != 200 {
		t.Fatalf("valid ms = %d, want 200", m.ValidMs)
	}
	if m.AlignmentMs > 2 {
		t.Fatalf("alignment error %0.3f ms exceeds 2 ms", m.AlignmentMs)
	}
}

func TestBuilderTimestampsSurviveWraparound(t *testing.T) {
	rate := 48000
	b, err := New(testConfig(rate), audio.Stereo)
	if err != nil {
		t.Fatal(err)
	}
	// Start just below 2^32 so the run crosses it.
	start := timeline.Ticks(math.MaxUint32 - 500)
	if err := b.AddSegment(Segment{Audio: testBuffer(t, 4800, rate), StartTicks: start}); err != nil {
		t.Fatal(err)
	}
	m, err := b.Finalize(100)
	if err != nil {
		t.Fatal(err)
	}
	var wraps int
	for i, p := range m.Packets {
		if i > 0 && p.Timestamp < m.Packets[i-1].Timestamp {
			wraps++
			if int(p.Sequence) != i {
				t.Fatalf("sequence order broken at wrap: %d", p.Sequence)
			}
			if !p.Wrapped {
				t.Fatalf("packet %d not marked wrapped", i)
			}
		}
	}
	if wraps != 1 {
		t.Fatalf("wraps = %d, want 1", wraps)
	}
	SortPacketsBySequence(m.Packets)
	for i := 1; i < len(m.Packets); i++ {
		if m.Packets[i].Sequence != m.Packets[i-1].Sequence+1 {
			t.Fatalf("sequence gap after sort at %d", i)
		}
	}
}

func TestBuilderSegmentGapKeepsTimelineHole(t *testing.T) {
	rate := 48000
	b, err := New(testConfig(rate), audio.Stereo)
	if err != nil {
		t.Fatal(err)
	}
	// First 200 ms, a gap, then another 200 ms starting at 400 ms.
	if err := b.AddSegment(Segment{Audio: testBuffer(t, 9600, rate), StartTicks: 0}); err != nil {
		t.Fatal(err)
	}
	restart := timeline.Ticks(400 * rate / 1000)
	if err := b.AddSegment(Segment{Audio: testBuffer(t, 9600, rate), StartTicks: restart, GapsBefore: 1, Discontinuity: true}); err != nil {
		t.Fatal(err)
	}
	b.SetGaps([]Gap{{StartMs: 200, LengthMs: 200, Reason: "truncated sample", Offset: 4096}})
	m, err := b.Finalize(600)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Gaps) != 1 || m.Gaps[0].LengthMs != 200 {
		t.Fatalf("gaps = %+v", m.Gaps)
	}
	last := m.Packets[len(m.Packets)-1]
	if last.StartMs < 400 {
		t.Fatalf("restarted packet starts at %d ms, want >= 400", last.StartMs)
	}
	if m.TotalMs != 600 || m.ValidMs != 400 {
		t.Fatalf("valid/total = %d/%d", m.ValidMs, m.TotalMs)
	}
}

func TestManifestMarshalsAndIsDeterministic(t *testing.T) {
	rate := 48000
	build := func() *Manifest {
		b, err := New(testConfig(rate), audio.Stereo)
		if err != nil {
			t.Fatal(err)
		}
		if err := b.AddSegment(Segment{Audio: testBuffer(t, 4800, rate), StartTicks: 0}); err != nil {
			t.Fatal(err)
		}
		b.SetLoudness(-23, -16, 7, false)
		m, err := b.Finalize(100)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	first, err := Marshal(build())
	if err != nil {
		t.Fatal(err)
	}
	second, err := Marshal(build())
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("manifest bytes differ between identical runs")
	}
	if SummaryDigest(build()) != SummaryDigest(build()) {
		t.Fatal("summary digest is nondeterministic")
	}
	var round Manifest
	if err := json.Unmarshal(first, &round); err != nil {
		t.Fatalf("manifest is not valid JSON: %v", err)
	}
	if round.PacketCount != 5 || round.Summary == "" {
		t.Fatalf("round trip lost fields: %+v", round)
	}
}

func TestBuilderRejectsBadConfigAndEmptyAudio(t *testing.T) {
	cfg := testConfig(48000)
	cfg.Channels = 6
	if _, err := New(cfg, audio.Stereo); !core.IsCode(err, core.CodeParamConflict) {
		t.Fatalf("channel mismatch accepted: %v", err)
	}
	cfg = testConfig(48000)
	cfg.PacketMs = 0
	if _, err := New(cfg, audio.Stereo); !core.IsCode(err, core.CodeParamConflict) {
		t.Fatalf("bad packet duration accepted: %v", err)
	}
	// An empty run is not distributable.
	b, err := New(testConfig(48000), audio.Stereo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Finalize(0); !core.IsCode(err, core.CodeUnrecoverable) {
		t.Fatalf("empty manifest accepted: %v", err)
	}
}

func TestPacketsAreValidRTP(t *testing.T) {
	rate := 48000
	b, err := New(testConfig(rate), audio.Stereo)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.AddSegment(Segment{Audio: testBuffer(t, 960, rate), StartTicks: 12345}); err != nil {
		t.Fatal(err)
	}
	m, err := b.Finalize(20)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := Shape(testBuffer(t, 960, rate), 128, rate)
	if err != nil {
		t.Fatal(err)
	}
	pkt := rtp.Packet{Header: rtp.Header{
		Version: 2, PayloadType: 96, SequenceNumber: m.Packets[0].Sequence,
		Timestamp: m.Packets[0].Timestamp, SSRC: 0x0BADCAFE,
	}, Payload: payload}
	raw, err := pkt.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var decoded rtp.Packet
	if err := decoded.Unmarshal(raw); err != nil {
		t.Fatalf("pion rejected the packet: %v", err)
	}
	if decoded.Timestamp != m.Packets[0].Timestamp || len(decoded.Payload) != m.Packets[0].Bytes {
		t.Fatalf("round trip mismatch: %+v", decoded.Header)
	}
}
