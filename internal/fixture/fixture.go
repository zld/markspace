// Package fixture synthesizes deterministic RTP/RTCP byte streams for tests
// and the CLI demo. No external media files are referenced.
package fixture

import (
	"bytes"
	"encoding/binary"
	"math"

	"github.com/pion/rtp"

	"mediad/internal/ingest"
)

// Params controls a generated stream.
type Params struct {
	Packets    int
	Rate       uint32 // RTP clock rate / source sample rate
	Channels   int
	SamplesPkt int    // L16 source frames per packet
	Seed       uint64
	SSRC       uint32
	// Corruption toggles.
	DuplicateReorder bool
	BurstLoss        bool
	WrapTS           bool
}

// Default returns the demo parameters used by the acceptance commands.
func Default() Params {
	return Params{
		Packets: 12000, Rate: 48000, Channels: 2, SamplesPkt: 48,
		Seed: 1, SSRC: 0xa11ce, DuplicateReorder: true,
	}
}

// lcg is a tiny deterministic PRNG (numerical recipes constants).
type lcg struct{ state uint64 }

func (l *lcg) next() uint64 {
	l.state = l.state*6364136223846793005 + 1442695040888963407
	return l.state
}

// Build renders a framed stream and returns it alongside the ordered logical
// seq list (post-corruption) and total L16 sample count.
func Build(p Params) (stream []byte, seqs []uint16, samples int) {
	rng := &lcg{state: p.Seed}
	var out bytes.Buffer
	step := uint32(p.SamplesPkt)

	// One sender report near the start: NTP 1.0s paired with RTP tick 0.
	writeSR(&out, p.SSRC, uint64(1)<<32, 0)

	for i := 0; i < p.Packets; i++ {
		seq := uint16(i)
		ts := uint32(i) * step
		if p.WrapTS && i > p.Packets-3 {
			ts = uint32(math.MaxUint32) - uint32((p.Packets-i)*int(step)) + step
		}
		payload := l16Payload(rng, p.SamplesPkt, p.Channels, i)
		writeRTP(&out, seq, ts, p.SSRC, payload)
		seqs = append(seqs, seq)
		samples += p.SamplesPkt * p.Channels

		// Inject a duplicate of an earlier packet and reorder two neighbors.
		if p.DuplicateReorder && i > 10 && i%373 == 0 {
			writeRTP(&out, uint16(i-3), uint32(i-3)*step, p.SSRC,
				l16Payload(rng, p.SamplesPkt, p.Channels, i-3))
		}
	}

	// A contiguous burst loss: simply omit a run by rebuilding without those
	// packets is handled by the caller via BurstLoss below.
	if p.BurstLoss {
		return buildWithBurstLoss(p)
	}
	return out.Bytes(), seqs, samples
}

// buildWithBurstLoss omits five consecutive middle packets.
func buildWithBurstLoss(p Params) ([]byte, []uint16, int) {
	rng := &lcg{state: p.Seed}
	var out bytes.Buffer
	var seqs []uint16
	samples := 0
	gapStart, gapEnd := p.Packets/2, p.Packets/2+4
	writeSR(&out, p.SSRC, uint64(1)<<32, 0)
	for i := 0; i < p.Packets; i++ {
		if i >= gapStart && i <= gapEnd {
			continue // unrecoverable burst; sequence numbers keep their gap
		}
		seq := uint16(i)
		payload := l16Payload(rng, p.SamplesPkt, p.Channels, i)
		writeRTP(&out, seq, uint32(i)*uint32(p.SamplesPkt), p.SSRC, payload)
		seqs = append(seqs, seq)
		samples += p.SamplesPkt * p.Channels
	}
	return out.Bytes(), seqs, samples
}

// l16Payload builds big-endian 16-bit PCM for one packet.
func l16Payload(rng *lcg, frames, chans, pkt int) []byte {
	buf := make([]byte, frames*chans*2)
	for f := 0; f < frames; f++ {
		base := math.Sin(2*math.Pi*440*float64(pkt*frames+f)/48000) * 12000
		for c := 0; c < chans; c++ {
			jitter := float64(int64(rng.next()%2001)-1000) * 0.1
			v := int16(base + jitter)
			binary.BigEndian.PutUint16(buf[(f*chans+c)*2:], uint16(v))
		}
	}
	return buf
}

func writeRTP(buf *bytes.Buffer, seq uint16, ts uint32, ssrc uint32, payload []byte) {
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version: 2, PayloadType: 96,
			SequenceNumber: seq, Timestamp: ts, SSRC: ssrc,
		},
		Payload: payload,
	}
	raw, err := pkt.Marshal()
	if err != nil {
		panic(err)
	}
	if err := ingest.WriteFrame(buf, ingest.TagRTP, raw); err != nil {
		panic(err)
	}
}

func writeSR(buf *bytes.Buffer, ssrc uint32, ntp uint64, rtpTS uint32) {
	sr := make([]byte, 28)
	sr[0], sr[1] = 0x80, 200
	sr[2], sr[3] = 0x00, 0x06
	binary.BigEndian.PutUint32(sr[4:8], ssrc)
	binary.BigEndian.PutUint64(sr[8:16], ntp)
	binary.BigEndian.PutUint32(sr[16:20], rtpTS)
	if err := ingest.WriteFrame(buf, ingest.TagRTCP, sr); err != nil {
		panic(err)
	}
}

// Corrupt renders the standard stream then injects a frame whose declared
// length exceeds the remaining bytes (a truncated packet) at a known offset.
func Corrupt() (stream []byte, badOffset int64) {
	p := Default()
	base, _, _ := Build(p)
	// Cut on a frame boundary by walking the framing so the healthy prefix is
	// a whole number of records and badOffset is byte-accurate.
	cut := frameBoundaryAtHalf(base)
	var out bytes.Buffer
	out.Write(base[:cut])
	badOffset = int64(out.Len())
	// Tag RTP, declare 0x1000 bytes but provide only six.
	out.WriteByte(ingest.TagRTP)
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], 0x1000)
	out.Write(hdr[:])
	out.Write([]byte{0x80, 0x60, 0, 1, 0, 0})
	return out.Bytes(), badOffset
}

// MalformedLength injects a zero-length frame header at a known offset.
func MalformedLength() (stream []byte, badOffset int64) {
	p := Default()
	base, _, _ := Build(p)
	cut := frameBoundaryAtHalf(base)
	var out bytes.Buffer
	out.Write(base[:cut])
	badOffset = int64(out.Len())
	out.WriteByte(ingest.TagRTP)
	out.Write([]byte{0, 0, 0, 0}) // zero length -> BAD_LENGTH
	return out.Bytes(), badOffset
}

// frameBoundaryAtHalf returns the largest whole-frame end offset at or after
// the midpoint of a length-framed stream.
func frameBoundaryAtHalf(base []byte) int {
	pos, half := 0, len(base)/2
	for pos < half {
		if pos+5 > len(base) {
			break
		}
		n := int(binary.BigEndian.Uint32(base[pos+1:])) + 5
		pos += n
	}
	return pos
}

// WrapStream builds a stream whose last RTP timestamps cross the 32-bit edge.
func WrapStream() []byte {
	p := Default()
	p.WrapTS = true
	stream, _, _ := Build(p)
	return stream
}
