// Package fixture generates every input the kernel and its demo consume.
// Nothing on disk or outside the module is referenced: all WAV buffers and
// RTP packets are synthesized deterministically so tests and the demo share
// exactly the same inputs.
package fixture

import (
	"encoding/binary"

	"github.com/pion/rtp"
)

// Case is one self-contained input block plus its intended timeline anchor.
type Case struct {
	Name        string
	Kind        string
	WAV         []byte
	RTP         [][]byte
	SampleStart int64 // explicit anchor on the unified PCM sample axis
}

// Kinds enumerate the generated damage classes.
const (
	KindOK          = "ok"
	KindTruncated   = "truncated_header"
	KindOOB         = "chunk_out_of_bounds"
	KindPartial     = "partial_frame"
	KindRTPWrap     = "rtp_wrap"
	KindGap         = "interval_gap"
	KindLargeBytes  = "large_bytes"
	KindHeavyBudget = "heavy_budget"
	KindCancel      = "queue_cancel"
)

const (
	// SampleRate/geometry shared by every generated WAV block.
	SampleRate = 48000
	Channels   = 2
	BitsPer    = 16
	FrameBytes = 4
)

// riffPrefix writes the RIFF/WAVE header and an fmt chunk, returning a buffer
// the caller can extend with a data chunk.
func riffPrefix(riffSize uint32) []byte {
	buf := make([]byte, 36)
	copy(buf[0:4], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:8], riffSize)
	copy(buf[8:12], "WAVE")
	copy(buf[12:16], "fmt ")
	binary.LittleEndian.PutUint32(buf[16:20], 16)
	binary.LittleEndian.PutUint16(buf[20:22], 1) // PCM
	binary.LittleEndian.PutUint16(buf[22:24], Channels)
	binary.LittleEndian.PutUint32(buf[24:28], SampleRate)
	binary.LittleEndian.PutUint32(buf[28:32], SampleRate*FrameBytes)
	binary.LittleEndian.PutUint16(buf[32:34], FrameBytes)
	binary.LittleEndian.PutUint16(buf[34:36], BitsPer)
	return buf
}

// WAVE builds a structurally valid WAV whose data payload is n bytes (n need
// not be frame-aligned; callers modeling partial frames rely on that).
func WAVE(n int) []byte {
	buf := riffPrefix(uint32(4 + 24 + 8 + n))
	head := make([]byte, 8)
	copy(head[0:4], "data")
	binary.LittleEndian.PutUint32(head[4:8], uint32(n))
	buf = append(buf, head...)
	buf = append(buf, make([]byte, n)...)
	return buf
}

// TruncatedWAVE returns the first seven bytes of an otherwise valid WAV:
// even the RIFF/WAVE fixed header cannot be read.
func TruncatedWAVE() []byte { return WAVE(16)[:7] }

// OOBWAVE declares a 500-byte data payload while only 100 bytes are present,
// and adjusts the RIFF size to match the false declaration.
func OOBWAVE() []byte {
	buf := WAVE(100)
	binary.LittleEndian.PutUint32(buf[40:44], 500)
	binary.LittleEndian.PutUint32(buf[4:8], uint32(4+24+8+500))
	return buf
}

// PartialFrameWAVE carries 35 payload bytes: eight full 4-byte frames plus
// three trailing bytes that cannot complete a ninth.
func PartialFrameWAVE() []byte { return WAVE(35) }

// rtpPacket marshals one minimal RTP packet.
func rtpPacket(seq uint16, stamp uint32) []byte {
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version: 2, PayloadType: 96,
			SequenceNumber: seq, Timestamp: stamp, SSRC: 7,
		},
		Payload: []byte{0xAA, 0xBB},
	}
	raw, err := pkt.Marshal()
	if err != nil {
		panic(err) // fixture generation is deterministic; a marshal bug is fatal
	}
	return raw
}

// RTPWrap produces four packets crossing both a sequence wrap (65535 -> 0)
// and a timestamp wrap, on a 48kHz clock with 960-tick steps.
func RTPWrap() [][]byte {
	base := uint32(0xFFFFFFF0)
	return [][]byte{
		rtpPacket(65534, base),
		rtpPacket(65535, base+960),
		rtpPacket(0, base+1920), // raw timestamp wraps here
		rtpPacket(1, base+2880),
	}
}

// RTPReorder produces three packets whose arrival order disagrees with
// sequence order, exercising the reorder fault path end to end.
func RTPReorder() [][]byte {
	return [][]byte{
		rtpPacket(100, 0),
		rtpPacket(102, 1920),
		rtpPacket(101, 960),
	}
}

// Dataset returns the canonical demo/test dataset. Sample anchors are chosen
// up front so the timeline is deterministic; one case deliberately opens a
// 200-sample gap to exercise interval analysis.
func Dataset() []Case {
	cursor := int64(0)
	advance := func(frames int) int64 {
		start := cursor
		cursor += int64(frames)
		return start
	}
	cases := []Case{
		{Name: "clean-a", Kind: KindOK, WAV: WAVE(16), SampleStart: advance(4)},
		{Name: "clean-b", Kind: KindOK, WAV: WAVE(32), SampleStart: advance(8)},
		{Name: "truncated-header", Kind: KindTruncated, WAV: TruncatedWAVE(), SampleStart: cursor},
		{Name: "oob-chunk", Kind: KindOOB, WAV: OOBWAVE(), SampleStart: cursor},
		{Name: "partial-frame", Kind: KindPartial, WAV: PartialFrameWAVE(), SampleStart: cursor},
		{Name: "rtp-wrap", Kind: KindRTPWrap, WAV: WAVE(16), RTP: RTPWrap(), SampleStart: advance(4)},
	}
	// gap-segment is structurally fine but anchored 200 samples after the
	// cursor, leaving a hole the analyzer must localize.
	gapStart := cursor + 200
	cases = append(cases, Case{
		Name: "gap-segment", Kind: KindGap, WAV: WAVE(16), SampleStart: gapStart,
	})
	cursor = gapStart + 4
	cases = append(cases,
		Case{Name: "large-1", Kind: KindLargeBytes, WAV: WAVE(3000), SampleStart: advance(750)},
		Case{Name: "large-2", Kind: KindLargeBytes, WAV: WAVE(2000), SampleStart: advance(500)},
		Case{Name: "heavy-budget", Kind: KindHeavyBudget, WAV: WAVE(48000*FrameBytes), SampleStart: advance(48000)},
		Case{Name: "trailing-small", Kind: KindOK, WAV: WAVE(16), SampleStart: advance(4)},
	)
	return cases
}

// CancelDataset returns enough cheap tasks to keep a depth-2 queue saturated
// when workers are intentionally parked, exercising producer cancellation.
// Five thousand tiny blocks are enough that a single worker cannot drain the
// producer before cancellation arrives, while total memory stays small.
func CancelDataset() []Case {
	const n = 5000
	cases := make([]Case, n)
	for i := range cases {
		cases[i] = Case{
			Name:        "cancel-" + itoa(i),
			Kind:        KindCancel,
			WAV:         WAVE(16),
			SampleStart: int64(i) * 4,
		}
	}
	return cases
}

// itoa avoids pulling strconv into fixture-generated names.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}
