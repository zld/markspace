// Package fixture synthesizes all sample media in-process: no external media
// files are referenced. It produces healthy RTP sequences plus every damaged
// variant the pipeline must reject with a stable error, and a lightweight
// hand-encoded MP4 whose time base is read back through go-mp4.
package fixture

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"

	"github.com/pion/rtp"

	"mediad/internal/ingest"
	"mediad/internal/model"
)

// Scenario names used by the demo command and tests.
const (
	ScenHealthy   = "healthy"
	ScenDuplicate = "duplicate"
	ScenMissing   = "missing"
	ScenTruncAAC  = "truncated-aac"
	ScenBadOpus   = "bad-opus-toc"
	ScenMP4       = "mp4-timebase"
)

// FrameCount is the number of Opus frames in a healthy generated sequence.
const FrameCount = 50

// Packet is one generated RTP datagram plus its expected sequence/timestamp.
type Packet struct {
	Raw      []byte
	Sequence uint16
	TS       uint32
}

func rtpPacket(pt uint8, seq uint16, ts uint32, payload []byte) Packet {
	wire := &rtp.Packet{
		Header:  rtp.Header{Version: 2, PayloadType: pt, SequenceNumber: seq, Timestamp: ts, SSRC: 4242},
		Payload: payload,
	}
	raw, err := wire.Marshal()
	if err != nil {
		panic(err)
	}
	return Packet{Raw: raw, Sequence: seq, TS: ts}
}

// opusPayload builds a legal code-0 Opus packet for a 20 ms frame. The frame
// is sized so 50 packets over one second carry 10000 payload bytes, matching
// the 80 kbps demo target bitrate within the 5% gate.
func opusPayload() []byte {
	p := make([]byte, 200)
	p[0] = 0x00 // TOC: config 0, code 0 (one frame)
	for i := 1; i < len(p); i++ {
		p[i] = 0xAA
	}
	return p
}

// OpusTrack generates a healthy sequence of 20 ms Opus RTP packets. Each RTP
// timestamp advances 960 ticks at the 48 kHz clock.
func OpusTrack() []Packet {
	out := make([]Packet, 0, FrameCount)
	for i := 0; i < FrameCount; i++ {
		seq := uint16(i)
		ts := uint32(i * 960)
		out = append(out, rtpPacket(96, seq, ts, opusPayload()))
	}
	return out
}

// WithDuplicate returns the healthy sequence with packet 7 delivered twice.
// Total media duration must not increase.
func WithDuplicate() []Packet {
	base := OpusTrack()
	out := make([]Packet, 0, len(base)+1)
	out = append(out, base[:8]...)
	out = append(out, base[7]) // exact re-delivery of seq 7
	out = append(out, base[8:]...)
	return out
}

// WithMissing returns the healthy sequence with seq 20 removed.
func WithMissing() []Packet {
	base := OpusTrack()
	out := make([]Packet, 0, len(base)-1)
	out = append(out, base[:20]...)
	out = append(out, base[21:]...)
	return out
}

// BadOpusTOCPacket returns one packet whose TOC uses reserved config 31; the
// ingest layer must reject it with ERR_OPUS_TOC.
func BadOpusTOCPacket() Packet {
	return rtpPacket(96, 5, 5*960, []byte{31 << 3, 0xAA})
}

// aacPayload builds an RFC 3640 payload for one AU of frameSize bytes.
func aacPayload(frameSize int) []byte {
	word := uint16(frameSize) << 3
	hdr := []byte{0x00, 0x02, byte(word >> 8), byte(word)}
	data := bytes.Repeat([]byte{0xAB}, frameSize)
	return append(hdr, data...)
}

// AACTrack generates healthy AAC RTP packets at 48 kHz (1024-sample frames).
func AACTrack(count int) []Packet {
	out := make([]Packet, 0, count)
	for i := 0; i < count; i++ {
		seq := uint16(i)
		ts := uint32(i * 1024)
		out = append(out, rtpPacket(97, seq, ts, aacPayload(40)))
	}
	return out
}

// TruncatedAACPacket returns an AAC packet whose AU header claims more bytes
// than the payload contains.
func TruncatedAACPacket() Packet {
	// Header says 300 bytes; only two follow.
	word := uint16(300) << 3
	payload := []byte{0x00, 0x02, byte(word >> 8), byte(word), 0xAB, 0xCD}
	return rtpPacket(97, 11, 11*1024, payload)
}

// box encodes a length-prefixed MP4 box.
func box(fourcc string, payload []byte) []byte {
	var b bytes.Buffer
	size := uint32(8 + len(payload))
	_ = binary.Write(&b, binary.BigEndian, size)
	b.WriteString(fourcc)
	b.Write(payload)
	return b.Bytes()
}

func fullBox(ver byte, flags uint32) []byte {
	return []byte{ver, byte(flags >> 16), byte(flags >> 8), byte(flags)}
}

func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

// MP4TimeBase builds a minimal ftyp+moov/trak/mdia/mdhd + stbl(stts,stsz)
// sample. It is hand-encoded so the project depends on no external assets;
// ingest.ReadMP4TimeBase parses it via go-mp4.
func MP4TimeBase(timescale uint32, sampleCount, delta, constSize uint32) []byte {
	mdhd := append(fullBox(0, 0), append(u32(0), u32(0)...)...)
	mdhd = append(mdhd, u32(timescale)...)
	mdhd = append(mdhd, u32(delta*sampleCount)...)
	mdhd = append(mdhd, 0x55, 0xC4, 0x00, 0x00) // language und + pre-defined
	stts := append(fullBox(0, 0), u32(1)...)
	stts = append(stts, u32(sampleCount)...)
	stts = append(stts, u32(delta)...)
	stsz := append(fullBox(0, 0), u32(constSize)...)
	stsz = append(stsz, u32(sampleCount)...)
	stbl := append(box("stts", stts), box("stsz", stsz)...)
	minf := box("minf", stbl)
	mdia := append(box("mdhd", mdhd), minf...)
	trak := box("trak", box("mdia", mdia))
	moov := box("moov", trak)
	ftyp := box("ftyp", []byte("isom\x00\x00\x00\x00isom"))
	return append(ftyp, moov...)
}

// WriteAll materializes every fixture under dir/fixtures and returns the
// directory containing them.
func WriteAll(dir string) (string, error) {
	out := filepath.Join(dir, "fixtures")
	if err := os.MkdirAll(out, 0o755); err != nil {
		return "", err
	}
	writers := map[string][]byte{
		ScenHealthy + ".rtp":   joinRaw(OpusTrack()),
		ScenDuplicate + ".rtp": joinRaw(WithDuplicate()),
		ScenMissing + ".rtp":   joinRaw(WithMissing()),
		ScenBadOpus + ".rtp":   joinRaw([]Packet{BadOpusTOCPacket()}),
		ScenTruncAAC + ".rtp":  joinRaw([]Packet{TruncatedAACPacket()}),
		ScenMP4 + ".mp4":       MP4TimeBase(48000, FrameCount, 960, 97),
	}
	for name, data := range writers {
		if err := os.WriteFile(filepath.Join(out, name), data, 0o644); err != nil {
			return "", err
		}
	}
	return out, nil
}

// joinRaw serializes packets with the MDC1 length-prefixed capture framing.
func joinRaw(pkts []Packet) []byte {
	var b bytes.Buffer
	b.WriteString(CaptureHeader)
	for _, p := range pkts {
		var ln [2]byte
		binary.BigEndian.PutUint16(ln[:], uint16(len(p.Raw)))
		b.Write(ln[:])
		b.Write(p.Raw)
	}
	return b.Bytes()
}

// CaptureHeader marks length-prefixed capture files written by WriteAll.
const CaptureHeader = "MDC1"

// SplitDatagrams splits a length-prefixed capture (MDC1 header, then each
// datagram as uint16 length + bytes) back into individual RTP packets.
func SplitDatagrams(raw []byte) ([][]byte, error) {
	if len(raw) < 4 || string(raw[:4]) != CaptureHeader {
		return nil, model.ErrRTPTruncated.At("", 0, "fixture", 0,
			"missing MDC1 capture header")
	}
	var out [][]byte
	off := 4
	for off < len(raw) {
		if off+2 > len(raw) {
			return nil, model.ErrRTPTruncated.At("", 0, "fixture", int64(off),
				"truncated datagram length prefix")
		}
		n := int(binary.BigEndian.Uint16(raw[off : off+2]))
		off += 2
		if n == 0 || off+n > len(raw) {
			return nil, model.ErrRTPTruncated.At("", 0, "fixture", int64(off),
				"datagram length exceeds capture")
		}
		out = append(out, raw[off:off+n])
		off += n
	}
	return out, nil
}

// Specs returns the default track specs used with the generated fixtures.
func Specs() []ingest.TrackSpec {
	return []ingest.TrackSpec{
		{ID: "opus-a", Codec: ingest.CodecOpus, ClockRate: 48000},
		{ID: "aac-a", Codec: ingest.CodecAAC, ClockRate: 48000, AACSampleRate: 48000},
	}
}
