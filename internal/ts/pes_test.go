package ts

import (
	"math"
	"testing"

	"example.com/assetpack/internal/fault"
)

// pes builds a complete PES packet carrying PCM payload.
func pes(pts int64, payload []byte) []byte {
	hdr := []byte{0x00, 0x00, 0x01, PESStreamMPEG2Audio, 0x00, 0x00, 0x80, 0x80, 0x05}
	hdr = append(hdr, PTSBytes(pts)...)
	return append(hdr, payload...)
}

// audioTS wraps PES packets into transport packets on one PID.
func audioTS(pid uint16, packets [][]byte) []byte {
	var out []byte
	cc := uint8(0)
	for _, p := range packets {
		// Split into chunk-sized PES fragments across TS packets.
		for i := 0; i < len(p); i += 175 {
			end := i + 175
			if end > len(p) {
				end = len(p)
			}
			out = append(out, buildPESPacket(pid, cc, i == 0, p[i:end])...)
			cc = (cc + 1) & 0x0F
		}
	}
	return out
}

// buildPESPacket packs data into one transport packet, using adaptation-field
// stuffing so no filler bytes are mistaken for elementary payload.
func buildPESPacket(pid uint16, cc uint8, pusi bool, data []byte) []byte {
	if len(data) >= 184 {
		return buildPacketNoAdapt(pktSpec{pid: pid, cc: cc, pusi: pusi, payload: data})
	}
	p := make([]byte, PacketSize)
	p[0] = SyncByte
	p[1] = byte(pid >> 8)
	if pusi {
		p[1] |= 0x40
	}
	p[2] = byte(pid)
	p[3] = 0x30 | cc
	afLen := 183 - len(data)
	p[4] = byte(afLen)
	if afLen > 0 {
		p[5] = 0
		for i := 6; i < 5+afLen; i++ {
			p[i] = 0xFF
		}
	}
	copy(p[5+afLen:], data)
	return p
}

func pcmPayload(frames, channels int, freq float64, rate int) []byte {
	out := make([]byte, frames*channels*2)
	pos := 0
	for i := 0; i < frames; i++ {
		for c := 0; c < channels; c++ {
			v := 0.3 * math.Sin(2*math.Pi*freq*float64(i)/float64(rate))
			s := int16(v * 32767)
			out[pos] = byte(s)
			out[pos+1] = byte(uint16(s) >> 8)
			pos += 2
		}
	}
	return out
}

func TestDemuxAudioPES(t *testing.T) {
	const rate = 48000
	const channels = 2
	frames := 4800
	payload := pcmPayload(frames, channels, 1000, rate)
	var pesPackets [][]byte
	// Two PES packets with a 100 ms PTS cadence.
	pesPackets = append(pesPackets, pes(90000, payload[:len(payload)/2]))
	pesPackets = append(pesPackets, pes(90000+9000, payload[len(payload)/2:]))
	data := audioTS(0x102, pesPackets)

	s := Parse(data)
	if s.Stats.SyncErrors != 0 {
		t.Fatalf("fixture sync broken: %+v", s.Stats)
	}
	el := DemuxAudio(s, 0x102, channels*2)
	if el.Packets == 0 {
		t.Fatal("no audio packets demuxed")
	}
	if got := el.Bytes(); got != frames*channels*2 {
		t.Fatalf("recovered %d bytes, want %d", got, frames*channels*2)
	}
	if el.SampleCount(channels*2) != int64(frames) {
		t.Fatalf("frame count %d", el.SampleCount(channels*2))
	}
	if pts := el.FirstPTS(); pts != 90000 {
		t.Fatalf("first PTS %d", pts)
	}
	raw, err := el.DecodePCM16LE(channels)
	if err != nil || len(raw) != len(payload) {
		t.Fatalf("decode: %v len %d", err, len(raw))
	}
	// Round trip the payload bits exactly.
	for i := range payload {
		if raw[i] != payload[i] {
			t.Fatalf("byte %d differs: %d vs %d", i, raw[i], payload[i])
		}
	}
}

func TestDemuxAudioPTSGapReported(t *testing.T) {
	const rate = 48000
	const channels = 1
	frames := 4800
	first := pcmPayload(frames, channels, 500, rate)
	second := pcmPayload(frames, channels, 500, rate)
	var packets [][]byte
	packets = append(packets, pes(0, first))
	// Skip 4800 frames worth of PTS (100 ms = 9000 ticks at 90 kHz).
	packets = append(packets, pes(9000+9000, second))
	data := audioTS(0x103, packets)
	el := DemuxAudio(Parse(data), 0x103, channels*2)
	gaps := el.SourceGaps(rate, channels*2)
	if len(gaps) == 0 {
		t.Fatalf("PTS jump not reported as a gap: chunks=%d", len(el.Chunks))
	}
	if gaps[0].StartSample != int64(frames) {
		t.Fatalf("gap starts at %d, want %d", gaps[0].StartSample, frames)
	}
	if gaps[0].LengthSample != int64(frames) {
		t.Fatalf("gap length %d, want %d", gaps[0].LengthSample, frames)
	}
}

func TestDemuxAudioDropsUntrustedPackets(t *testing.T) {
	payload := pcmPayload(1000, 1, 300, 48000)
	p1 := buildPESPacket(0x104, 0, true, append([]byte{0, 0, 1, PESStreamMPEG2Audio, 0, 0, 0x80, 0x80, 0x05, 0x21, 0, 0, 0, 0, 1}, payload[:170]...))
	// A duplicated CC packet must not contribute payload twice.
	p2 := buildPESPacket(0x104, 0, false, payload[170:340])
	p3 := buildPESPacket(0x104, 1, false, payload[340:])
	data := append(append(append([]byte{}, p1...), p2...), p3...)
	el := DemuxAudio(Parse(data), 0x104, 2)
	if el.Bytes() > len(payload)+16 {
		t.Fatalf("duplicate payload included: %d bytes", el.Bytes())
	}
}

func TestDemuxAudioPartialTrailingFrame(t *testing.T) {
	payload := []byte{0x01, 0x02, 0x03} // odd number of bytes for stereo 16-bit
	var packets [][]byte
	packets = append(packets, pes(0, payload))
	el := DemuxAudio(Parse(audioTS(0x105, packets)), 0x105, 4)
	raw, err := el.DecodePCM16LE(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 0 {
		t.Fatalf("partial frame included: %d bytes", len(raw))
	}
	if findFault(el.Faults, fault.KindTruncated) == nil {
		t.Fatal("partial frame not reported")
	}
}

func TestPTSBytesRoundTrip(t *testing.T) {
	for _, pts := range []int64{0, 1, 90000, 1 << 32, (1 << 33) - 1} {
		b := PTSBytes(pts)
		got := int64(b[0]>>1&0x07)<<30 | int64(b[1])<<22 | int64(b[2]>>1)<<15 |
			int64(b[3])<<7 | int64(b[4]>>1)
		if got != pts {
			t.Fatalf("PTS %d round tripped to %d", pts, got)
		}
	}
}
